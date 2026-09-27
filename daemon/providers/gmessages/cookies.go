// Google retired QR pairing, so the only remaining way in is "pair with your
// Google account": the daemon has to present itself as a signed-in
// messages.google.com web client, which means it needs that site's cookies.
// There are two ways to get them, and pairing tries them in this order:
//
//   - lifted out of a local Chromium-family profile, which needs nothing from
//     the user beyond being signed in to Google in that browser;
//   - pasted in from browser devtools (the panel's "Paste cookies" step), which always works
//     but has to be repeated whenever the Google session is replaced.
//
// Cookie values are secrets: they go to Google and into session.json (0600),
// and are never logged or written to state.json.
package gmessages

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Cookies Google's sign-in endpoint rejects the pairing request without.
var requiredCookies = []string{"SID", "HSID", "SSID", "APISID", "SAPISID", "OSID"}

// Cookies that are forwarded when present. libgm sends the whole set with
// every request and writes back whatever Google rotates, so handing over more
// than the minimum makes the session last longer.
var optionalCookies = []string{
	"__Secure-1PSID", "__Secure-1PSIDTS", "__Secure-1PSIDCC",
	"__Secure-3PSID", "__Secure-3PSIDTS", "__Secure-3PSIDCC",
	"SIDCC", "NID", "COMPASS", "AEC", "SEARCH_SAMESITE",
}

func wantedCookie(name string) bool {
	return slices.Contains(requiredCookies, name) || slices.Contains(optionalCookies, name)
}

// missingCookies names the required cookies that are absent, so the panel can
// say what is wrong instead of just "pairing failed".
func missingCookies(cookies map[string]string) []string {
	var missing []string
	for _, name := range requiredCookies {
		if strings.TrimSpace(cookies[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func cookieNames(cookies map[string]string) []string {
	names := make([]string, 0, len(cookies))
	for name := range cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- pasted cookies ---------------------------------------------------------

// One pattern per quoting style: Go's regexp has no backreferences, so the
// closing quote cannot be matched against the opening one.
var curlCookieRes = []*regexp.Regexp{
	regexp.MustCompile(`(?is)(?:-H|--header)\s+'\s*cookie\s*:\s*([^']*)'`),
	regexp.MustCompile(`(?is)(?:-H|--header)\s+"\s*cookie\s*:\s*([^"]*)"`),
	regexp.MustCompile(`(?is)(?:-b|--cookie)\s+'([^']*)'`),
	regexp.MustCompile(`(?is)(?:-b|--cookie)\s+"([^"]*)"`),
}

// curlCookieHeader pulls the Cookie header out of a copied cURL command.
func curlCookieHeader(blob string) string {
	for _, re := range curlCookieRes {
		if m := re.FindStringSubmatch(blob); m != nil {
			return m[1]
		}
	}
	return ""
}

// parseCookieBlob accepts the shapes a browser will hand you: a JSON object of
// cookies, a cURL command from devtools' "Copy as cURL", or a bare Cookie
// header. Cookies that pairing has no use for are dropped rather than stored.
func parseCookieBlob(blob string) (map[string]string, error) {
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return nil, errors.New("no cookies given")
	}
	if strings.HasPrefix(blob, "{") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(blob), &raw); err != nil {
			return nil, fmt.Errorf("that looks like JSON but does not parse: %w", err)
		}
		// Tolerate a wrapper object as well as a bare map of cookies.
		if inner, ok := raw["cookies"].(map[string]any); ok {
			raw = inner
		}
		cookies := map[string]string{}
		for name, value := range raw {
			if s, ok := value.(string); ok && wantedCookie(name) && s != "" {
				cookies[name] = s
			}
		}
		return cookies, nil
	}
	if header := curlCookieHeader(blob); header != "" {
		blob = header
	}
	return parseCookiePairs(blob), nil
}

// parseCookiePairs reads "name=value; name=value", one pair per line, or the
// tab-separated rows devtools' cookie table copies.
func parseCookiePairs(s string) map[string]string {
	cookies := map[string]string{}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' })
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		var name, value string
		if i := strings.IndexByte(field, '='); i > 0 {
			name, value = field[:i], field[i+1:]
		} else if parts := strings.Fields(field); len(parts) >= 2 {
			name, value = parts[0], parts[1]
		} else {
			continue
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if name == "" || value == "" || !wantedCookie(name) {
			continue
		}
		cookies[name] = value
	}
	return cookies
}

// --- cookies from a browser profile -----------------------------------------

type browserProfile struct {
	Label   string // what to show the user, e.g. "chromium (Default)"
	DB      string // path to the profile's Cookies database
	Keyring string // Secret Service "application" attribute for the v11 key
}

var chromiumFamily = []struct {
	dir     string
	label   string
	keyring string
}{
	{"chromium", "chromium", "chromium"},
	{"google-chrome", "chrome", "chrome"},
	{"google-chrome-beta", "chrome-beta", "chrome"},
	{"google-chrome-unstable", "chrome-unstable", "chrome"},
	{"BraveSoftware/Brave-Browser", "brave", "brave"},
	{"vivaldi", "vivaldi", "vivaldi"},
	{"microsoft-edge", "edge", "chromium"},
}

// browserProfiles lists every Chromium-family profile on this machine that has
// a cookie database, newest-used first so the profile the user actually browses
// in is tried before stale ones.
func browserProfiles() []browserProfile {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(os.Getenv("HOME"), ".config")
	}
	var profiles []browserProfile
	mtimes := map[string]int64{}
	for _, browser := range chromiumFamily {
		matches, _ := filepath.Glob(filepath.Join(configHome, browser.dir, "*", "Cookies"))
		for _, db := range matches {
			profile := filepath.Base(filepath.Dir(db))
			label := browser.label
			if profile != "Default" {
				label += " (" + profile + ")"
			}
			profiles = append(profiles, browserProfile{Label: label, DB: db, Keyring: browser.keyring})
			if st, err := os.Stat(db); err == nil {
				mtimes[db] = st.ModTime().UnixNano()
			}
		}
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		return mtimes[profiles[i].DB] > mtimes[profiles[j].DB]
	})
	return profiles
}

var (
	errNoBrowserProfile = errors.New("no Chromium-family browser profile found in ~/.config")
	errNoGoogleSession  = errors.New("the browser has no Google session")
)

// browserCookies lifts the Google cookies out of the best local browser
// profile. It returns the profile label so the panel can say where the session
// came from, and on failure an error that says what the user has to do.
func browserCookies() (map[string]string, string, error) {
	profiles := browserProfiles()
	if len(profiles) == 0 {
		return nil, "", errNoBrowserProfile
	}
	var (
		bestCookies map[string]string
		bestLabel   string
		firstErr    error
	)
	for _, profile := range profiles {
		cookies, err := profileCookies(profile)
		if err != nil {
			logger.Debug().Err(err).Str("profile", profile.Label).Msg("Could not read cookies from browser profile")
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", profile.Label, err)
			}
			continue
		}
		if len(missingCookies(cookies)) == 0 {
			return cookies, profile.Label, nil
		}
		if len(cookies) > len(bestCookies) {
			bestCookies, bestLabel = cookies, profile.Label
		}
	}
	if len(bestCookies) > 0 {
		return nil, bestLabel, fmt.Errorf("%s is signed in to Google but %w (missing %s)",
			bestLabel, errNoGoogleSession, strings.Join(missingCookies(bestCookies), ", "))
	}
	if firstErr != nil {
		return nil, "", firstErr
	}
	return nil, profiles[0].Label, fmt.Errorf("%w: open https://messages.google.com/web in %s and sign in",
		errNoGoogleSession, profiles[0].Label)
}

type cookieRow struct {
	Host  string `json:"host_key"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Enc   string `json:"enc"`
}

const cookieQuery = `SELECT host_key, name, value, hex(encrypted_value) AS enc FROM cookies
WHERE host_key IN ('.google.com', 'google.com', 'messages.google.com', '.messages.google.com')`

func profileCookies(profile browserProfile) (map[string]string, error) {
	rows, err := readCookieRows(profile.DB)
	if err != nil {
		return nil, err
	}
	keys := map[string][]byte{"v10": chromiumKey("peanuts")}
	// v11 values are encrypted with a password the browser keeps in the
	// keyring; look it up lazily so a profile that only uses v10 still works
	// with no Secret Service running.
	var keyringChecked bool
	var keyringErr error
	best := map[string]int{}
	cookies := map[string]string{}
	for _, row := range rows {
		if !wantedCookie(row.Name) {
			continue
		}
		value := row.Value
		if row.Enc != "" {
			enc, err := hex.DecodeString(row.Enc)
			if err != nil {
				continue
			}
			if len(enc) >= 3 && string(enc[:3]) == "v11" && !keyringChecked {
				keyringChecked = true
				password, err := keyringPassword(profile.Keyring)
				if err != nil {
					keyringErr = err
				} else {
					keys["v11"] = chromiumKey(password)
				}
			}
			decrypted, err := decryptCookie(enc, row.Host, keys)
			if err != nil {
				logger.Debug().Err(err).Str("cookie", row.Name).Msg("Could not decrypt cookie")
				continue
			}
			value = decrypted
		}
		if value == "" {
			continue
		}
		// messages.google.com wins over .google.com when a name is on both:
		// pairing is a Messages-for-web client.
		score := hostScore(row.Host)
		if prev, seen := best[row.Name]; seen && prev >= score {
			continue
		}
		best[row.Name] = score
		cookies[row.Name] = value
	}
	if len(cookies) == 0 && keyringErr != nil {
		return nil, fmt.Errorf("cookies are keyring-encrypted and the keyring password could not be read: %w", keyringErr)
	}
	return cookies, nil
}

func hostScore(host string) int {
	switch host {
	case "messages.google.com":
		return 3
	case ".messages.google.com":
		return 2
	case ".google.com":
		return 1
	default:
		return 0
	}
}

// readCookieRows queries a copy of the database: the browser holds a lock on
// the original while it runs, and the write-ahead log has to come along or
// recent sign-ins are missing.
func readCookieRows(dbPath string) ([]cookieRow, error) {
	tmp, err := os.MkdirTemp("", "gmessages-cookies-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	copyPath := filepath.Join(tmp, "Cookies")
	if err := copyFile(dbPath, copyPath); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = copyFile(dbPath+suffix, copyPath+suffix)
	}
	out, err := exec.Command("sqlite3", "-json", copyPath, cookieQuery).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("sqlite3: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("sqlite3 is not installed (pacman -S sqlite)")
		}
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var rows []cookieRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("unexpected sqlite3 output: %w", err)
	}
	return rows, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// keyringPassword asks the Secret Service for the browser's "Safe Storage"
// password. Shelling out to secret-tool keeps a whole D-Bus client out of the
// daemon for something that runs once per pairing.
func keyringPassword(app string) (string, error) {
	out, err := exec.Command("secret-tool", "lookup", "application", app).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("secret-tool is not installed (pacman -S libsecret)")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("no keyring entry for %q (is the keyring unlocked?)", app)
		}
		return "", err
	}
	password := string(out)
	if password == "" {
		return "", fmt.Errorf("keyring returned no password for %q", app)
	}
	return password, nil
}

// chromiumKey derives the AES key Chromium encrypts cookie values with on
// Linux: PBKDF2-SHA1 over the safe-storage password, one iteration.
func chromiumKey(password string) []byte {
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1, 16)
	if err != nil {
		// Only reachable in FIPS-only mode, where this scheme is unavailable.
		logger.Debug().Err(err).Msg("Cannot derive Chromium cookie key")
		return nil
	}
	return key
}

var cookieIV = bytes.Repeat([]byte{' '}, aes.BlockSize)

func decryptCookie(enc []byte, host string, keys map[string][]byte) (string, error) {
	if len(enc) < 3 {
		return "", errors.New("value too short")
	}
	version := string(enc[:3])
	key, ok := keys[version]
	if !ok || key == nil {
		return "", fmt.Errorf("no key for %s cookies", version)
	}
	body := enc[3:]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext is not a whole number of blocks")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, cookieIV).CryptBlocks(plain, body)
	plain, err = stripPKCS7(plain)
	if err != nil {
		return "", err
	}
	return string(stripDomainPrefix(plain, host)), nil
}

func stripPKCS7(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty plaintext")
	}
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return nil, errors.New("bad padding (wrong key?)")
	}
	return plain[:len(plain)-pad], nil
}

// stripDomainPrefix undoes Chromium 130+'s domain binding, which prepends
// SHA256(cookie domain) to the plaintext before encrypting. Older versions
// store the bare value, so the prefix has to be detected rather than assumed.
func stripDomainPrefix(plain []byte, host string) []byte {
	if len(plain) <= sha256.Size {
		return plain
	}
	for _, candidate := range []string{host, strings.TrimPrefix(host, "."), "." + strings.TrimPrefix(host, ".")} {
		want := sha256.Sum256([]byte(candidate))
		if bytes.Equal(plain[:sha256.Size], want[:]) {
			return plain[sha256.Size:]
		}
	}
	// A hash we cannot reproduce still is not a cookie value: those are
	// printable ASCII, so unprintable leading bytes mean a prefix is there.
	if !isPrintableASCII(plain[:sha256.Size]) {
		return plain[sha256.Size:]
	}
	return plain
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// sha256Sum is the domain hash Chromium prepends to cookie plaintext.
func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
