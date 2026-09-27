// omamessagesd bridges several messaging services (Google Messages, WhatsApp,
// Telegram) to JSON files that the Omarchy shell plugin renders. Run
// `omamessagesd serve` in the background; every other subcommand talks to that
// daemon over a unix socket.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"omarchy-omamessages/core"
	"omarchy-omamessages/providers/fake"
	"omarchy-omamessages/providers/gmessages"
	"omarchy-omamessages/providers/telegram"
	"omarchy-omamessages/providers/whatsapp"
)

var logger zerolog.Logger = zerolog.Nop()

// factories lists every service this binary can run. `serve --providers`
// picks which of them start. The fake is added only when asked for, so it
// doesn't show up in Accounts. Google Messages is added at serve time: live,
// or as a read-only preview of the old plugin (`--preview gmessages`, or
// whenever the old daemon is still running, see liveGMessages).
// demoFactories is set only in `-tags demo` builds (main_demo.go): made-up
// services for screenshots, started with `serve --demo`.
var demoFactories func() map[core.ProviderID]core.Factory

var factories = map[core.ProviderID]core.Factory{
	telegram.ID: telegram.New,
	whatsapp.ID: whatsapp.New,
}

func stateDir() string {
	if d := os.Getenv("OMAMESSAGES_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "omarchy", "omamessages")
}

func cacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "omarchy", "omamessages")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: omamessagesd <command> [args]

  serve [--providers a,b] [--preview gmessages] [--verbose] [--no-notify]
                                    run the daemon (exits at once if one is already running);
                                    --preview shows the old plugin's cached state read-only
  status                            print each service's status and the unread total
  connect <service> [method]        start signing in (method picks an alternative, e.g. qr)
  connect-input <service> [value]   answer the current step; value is read from stdin if omitted
  cancel-connect <service>          abandon signing in
  disconnect <service>              sign out and remove that service's chats from this computer
  refresh [service]                 re-list conversations for one service, or all
  open <service:id> [read]          fetch the latest messages; read=false leaves them unread
  more <service:id>                 fetch an older page
  typing <service:id> on|off        show (or stop showing) that you're typing
  send <service:id> [--file path]... <text...>
                                    send a message; files only where the service takes them
  new <service> <to> [text...]      start a chat (a number, or for Telegram @username or id:<n>); prints its id
  contacts <service> [query...]     list or search contacts as JSON
  fetch-media <service:id> <msg> <idx>
                                    download an attachment (once) and print its path
  pick-files [--multiple]           open the desktop file chooser; prints one path per line
  gmessages-cookies [--check]       which browser profile Google Messages would sign in from;
                                    --check also asks Google to accept it (never touches the phone)
  quit                              stop the daemon`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	if cmd == "serve" {
		serve(args)
		return
	}
	if cmd == "pick-files" {
		pickFiles(args)
		return
	}
	if cmd == "gmessages-cookies" {
		gmessages.ShowCookieSources(os.Stdout)
		if len(args) > 0 && args[0] == "--check" {
			if err := gmessages.CheckGoogleSession(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
		}
		return
	}
	req := Request{Cmd: cmd, Args: map[string]any{}}
	switch cmd {
	case "status", "quit", "ping":
	case "refresh":
		if len(args) > 0 {
			req.Args["provider"] = args[0]
		}
	case "connect", "cancel-connect", "disconnect":
		if len(args) < 1 {
			usage()
		}
		req.Args["provider"] = args[0]
		if cmd == "connect" && len(args) > 1 {
			req.Args["method"] = args[1]
		}
	case "connect-input":
		if len(args) < 1 {
			usage()
		}
		req.Args["provider"] = args[0]
		// Codes and passwords come on stdin so they never show in the
		// process list; a second argument is accepted for scripting.
		if len(args) > 1 {
			req.Args["value"] = strings.Join(args[1:], " ")
		} else {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			req.Args["value"] = strings.TrimRight(string(data), "\r\n")
		}
	case "new":
		if len(args) < 2 {
			usage()
		}
		req.Args["provider"] = args[0]
		req.Args["to"] = args[1]
		req.Args["text"] = strings.Join(args[2:], " ")
	case "contacts":
		if len(args) < 1 {
			usage()
		}
		req.Args["provider"] = args[0]
		req.Args["query"] = strings.Join(args[1:], " ")
	case "fetch-media":
		if len(args) < 3 {
			usage()
		}
		req.Args["id"], req.Args["msg"], req.Args["idx"] = args[0], args[1], args[2]
	case "typing":
		if len(args) < 2 {
			usage()
		}
		req.Args["id"] = args[0]
		req.Args["on"] = fmt.Sprint(args[1] == "on")
	case "open", "more":
		if len(args) < 1 {
			usage()
		}
		req.Args["id"] = args[0]
		if len(args) > 1 {
			req.Args["read"] = args[1]
		}
	case "send":
		if len(args) < 2 {
			usage()
		}
		req.Args["id"] = args[0]
		var files []string
		rest := args[1:]
		for len(rest) >= 2 && rest[0] == "--file" {
			files = append(files, rest[1])
			rest = rest[2:]
		}
		if files != nil {
			req.Args["files"] = files
		}
		req.Args["text"] = strings.Join(rest, " ")
	default:
		if extraCLI(cmd, args, &req) {
			break
		}
		usage()
	}
	resp, err := call(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !resp.OK {
		// A partial success (e.g. a chat created, its first message not
		// sent) still reports what was done.
		if v, ok := resp.Result.(string); ok {
			fmt.Println(v)
		}
		fmt.Fprintln(os.Stderr, "error:", resp.Error)
		os.Exit(1)
	}
	if resp.Result != nil {
		switch v := resp.Result.(type) {
		case string:
			fmt.Println(v)
		default:
			out, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(string(out))
		}
	}
}

func serve(args []string) {
	verbose := false
	notify := true
	var enabled, preview []core.ProviderID
	demo := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--no-notify":
			notify = false
		case a == "--demo":
			demo = true
		case a == "--providers" && i+1 < len(args):
			i++
			enabled = parseProviders(args[i])
		case strings.HasPrefix(a, "--providers="):
			enabled = parseProviders(strings.TrimPrefix(a, "--providers="))
		case a == "--preview" && i+1 < len(args):
			i++
			preview = parseProviders(args[i])
		case strings.HasPrefix(a, "--preview="):
			preview = parseProviders(strings.TrimPrefix(a, "--preview="))
		}
	}
	level := zerolog.InfoLevel
	if verbose {
		level = zerolog.DebugLevel
	}
	logger = zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stderr
		w.TimeFormat = time.Stamp
	})).Level(level).With().Timestamp().Logger()
	core.Log = logger

	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Fatal().Err(err).Msg("Cannot create state dir")
	}
	ln, err := listen()
	if err != nil {
		if err == errAlreadyRunning {
			logger.Info().Msg("Daemon already running")
			os.Exit(0)
		}
		logger.Fatal().Err(err).Msg("Cannot bind control socket")
	}
	if demo {
		if demoFactories == nil {
			logger.Fatal().Msg("--demo needs a build with -tags demo")
		}
		factories = demoFactories()
		enabled = []core.ProviderID{core.GMessages, core.Telegram, core.WhatsApp}
		preview = nil
	}
	for _, id := range preview {
		if id != gmessages.ID {
			logger.Warn().Str("provider", string(id)).Msg("Only gmessages has a preview, ignoring")
			continue
		}
		factories[id] = gmessages.NewPreview(gmessages.OldStateDir())
	}
	for _, id := range enabled {
		if id == fake.ID {
			factories[id] = fake.New
			continue
		}
		if id == gmessages.ID && factories[id] == nil && !demo {
			factories[id] = liveGMessages(dir)
			continue
		}
		if _, ok := factories[id]; !ok {
			logger.Warn().Str("provider", string(id)).Msg("Unknown service in --providers, ignoring")
		}
	}
	if n := cleanOutbox(cacheDir(), 7*24*time.Hour); n > 0 {
		logger.Info().Int("files", n).Msg("Cleared old pasted images from the outbox")
	}
	hub := NewHub(dir, cacheDir(), factories, enabled, notify)
	quit := make(chan struct{})
	go serveSocket(ln, hub, quit)
	hub.Start(context.Background())
	logger.Info().Interface("providers", enabled).Interface("preview", preview).Msg("Started")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-quit:
	}
	logger.Info().Msg("Shutting down")
	hub.Stop()
	_ = ln.Close()
	_ = os.Remove(socketPath())
}

func parseProviders(list string) []core.ProviderID {
	var out []core.ProviderID
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, core.ProviderID(s))
		}
	}
	return out
}

// pickFiles runs the portal file chooser in this process (the daemon isn't
// involved) and prints the chosen paths, one per line.
func pickFiles(args []string) {
	multiple := false
	for _, a := range args {
		if a == "--multiple" {
			multiple = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	paths, err := PickFiles(ctx, "Attach files", multiple)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	for _, p := range paths {
		fmt.Println(p)
	}
}

// liveGMessages is the Google Messages factory for a live start. Two clients
// on one Google pairing fight, so while the older gmessages plugin's daemon still
// answers on its socket this falls back to the read-only preview. Otherwise
// it brings the old pairing across (once) and runs the real provider.
func liveGMessages(stateDir string) core.Factory {
	if oldGMessagesRunning() {
		logger.Error().Err(errOldDaemon).Msg("Running Google Messages as a read-only preview; disable the older gmessages plugin to take it over")
		return gmessages.NewPreview(gmessages.OldStateDir())
	}
	migrated, err := MigrateFromGMessages(gmessages.OldStateDir(), filepath.Join(stateDir, string(gmessages.ID)))
	switch {
	case err != nil:
		logger.Error().Err(err).Msg("Could not bring the old Google Messages pairing across")
	case migrated:
		logger.Info().Str("from", gmessages.OldStateDir()).Msg("Brought the Google Messages pairing across from the old plugin")
	}
	return gmessages.New
}

// oldGMessagesRunning reports whether the old plugin's daemon answers.
func oldGMessagesRunning() bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "omarchy-gmessages.sock"), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
