package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
)

// PickFiles asks the desktop's file chooser (xdg-desktop-portal) for files
// and returns their local paths. It runs in the CLI process, not the daemon:
// the panel calls `omamessagesd pick-files` and reads one path per line. An
// empty result means the user cancelled.
func PickFiles(ctx context.Context, title string, multiple bool) ([]string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("no session bus: %w", err)
	}
	defer conn.Close()

	// The portal answers on a Request object whose path is derived from our
	// unique name and a token we choose; subscribe before asking, so the
	// answer can't slip past.
	token := "omamessages" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return nil, err
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"multiple":     dbus.MakeVariant(multiple),
	}
	var handle dbus.ObjectPath
	portal := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	if err := portal.CallWithContext(ctx, "org.freedesktop.portal.FileChooser.OpenFile", 0, "", title, options).Store(&handle); err != nil {
		return nil, fmt.Errorf("the file chooser portal isn't available: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sig := <-signals:
			if sig.Path != handle || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			if code != 0 {
				return nil, nil // 1 = cancelled, 2 = closed another way
			}
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			uris, _ := results["uris"].Value().([]string)
			var paths []string
			for _, u := range uris {
				p, err := uriToPath(u)
				if err != nil {
					return nil, err
				}
				paths = append(paths, p)
			}
			return paths, nil
		}
	}
}

// uriToPath turns a file:// URI from the portal into a local path.
func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", errors.New("only local files can be sent: " + uri)
	}
	if u.Path == "" {
		return "", errors.New("empty file URI")
	}
	return u.Path, nil
}
