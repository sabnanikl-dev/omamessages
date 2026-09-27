package gmessages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// Diagnostics for the Google sign-in, from the CLI (`omamessagesd
// gmessages-cookies [--check]`). They print cookie names only: the values
// are credentials.

// ShowCookieSources reports which browser profile pairing would use.
func ShowCookieSources(w io.Writer) {
	profiles := browserProfiles()
	if len(profiles) == 0 {
		fmt.Fprintln(w, "No Chromium-family browser profile found in ~/.config.")
		fmt.Fprintln(w, `Use the panel's "Paste cookies" step instead.`)
		return
	}
	for _, profile := range profiles {
		cookies, err := profileCookies(profile)
		missing := missingCookies(cookies)
		switch {
		case err != nil:
			fmt.Fprintf(w, "%-22s unreadable: %v\n", profile.Label, err)
		case len(cookies) == 0:
			fmt.Fprintf(w, "%-22s no Google cookies (not signed in)\n", profile.Label)
		case len(missing) > 0:
			fmt.Fprintf(w, "%-22s incomplete, missing %s\n", profile.Label, strings.Join(missing, ", "))
		default:
			fmt.Fprintf(w, "%-22s usable: %s\n", profile.Label, strings.Join(cookieNames(cookies), " "))
		}
	}
}

// CheckGoogleSession confirms Google accepts the browser's cookies and says
// which account they belong to. It only signs in; no pairing request reaches
// the phone, so it tells a cookie problem apart from a phone problem.
func CheckGoogleSession(w io.Writer) error {
	cookies, source, err := browserCookies()
	if err != nil {
		return err
	}
	auth := libgm.NewAuthData()
	auth.SetCookies(cookies)
	cli := libgm.NewClient(auth, nil, zerolog.New(io.Discard), exhttp.SensibleClientSettings)
	cli.SetEventHandler(func(any) {})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cli.FetchConfig(ctx); err != nil {
		return fmt.Errorf("Google rejected the session from %s: %w", source, err)
	}
	email := cli.Config.GetDeviceInfo().GetEmail()
	if email == "" {
		return errors.New("Google accepted the request but reported no account; sign in again at https://messages.google.com/web")
	}
	fmt.Fprintf(w, "\nGoogle accepts the session from %s: signed in as %s\n", source, email)
	return nil
}
