package gmessages

import "testing"

// The pasted blob is whatever the browser's devtools produced, so the parser
// has to cope with all three shapes without ever inventing a cookie.
func TestParseCookieBlob(t *testing.T) {
	tests := []struct {
		name string
		blob string
		want map[string]string
	}{{
		name: "curl with single quotes",
		blob: `curl 'https://messages.google.com/web/config' \
  -H 'accept: */*' \
  -H 'cookie: SID=sid-value; HSID=hsid-value; SSID=ssid-value; APISID=api-value; SAPISID=sapi-value; OSID=osid-value; IGNORED=nope' \
  -H 'user-agent: Mozilla/5.0'`,
		want: map[string]string{
			"SID": "sid-value", "HSID": "hsid-value", "SSID": "ssid-value",
			"APISID": "api-value", "SAPISID": "sapi-value", "OSID": "osid-value",
		},
	}, {
		name: "curl with double quotes and capitalised header",
		blob: `curl "https://messages.google.com/" -H "Cookie: SID=a; SAPISID=b"`,
		want: map[string]string{"SID": "a", "SAPISID": "b"},
	}, {
		name: "bare cookie header",
		blob: `SID=a; HSID=b; __Secure-1PSIDTS=c`,
		want: map[string]string{"SID": "a", "HSID": "b", "__Secure-1PSIDTS": "c"},
	}, {
		name: "json object",
		blob: `{"SID": "a", "SAPISID": "b", "junk": "c"}`,
		want: map[string]string{"SID": "a", "SAPISID": "b"},
	}, {
		name: "json wrapped in a cookies key",
		blob: `{"cookies": {"SID": "a"}}`,
		want: map[string]string{"SID": "a"},
	}, {
		name: "devtools cookie table rows",
		blob: "SID\ta-value\t.google.com\t/\nHSID\tb-value\t.google.com\t/",
		want: map[string]string{"SID": "a-value", "HSID": "b-value"},
	}, {
		// Cookie values are base64-ish and contain padding.
		name: "values containing equals signs",
		blob: `SID=g.a000abc==; HSID=x=y`,
		want: map[string]string{"SID": "g.a000abc==", "HSID": "x=y"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCookieBlob(tt.blob)
			if err != nil {
				t.Fatalf("parseCookieBlob: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d cookies %v, want %d %v", len(got), cookieNames(got), len(tt.want), cookieNames(tt.want))
			}
			for name, want := range tt.want {
				if got[name] != want {
					t.Errorf("%s = %q, want %q", name, got[name], want)
				}
			}
		})
	}
}

func TestParseCookieBlobRejectsJunk(t *testing.T) {
	if _, err := parseCookieBlob("   "); err == nil {
		t.Error("empty blob should be an error")
	}
	if _, err := parseCookieBlob(`{"SID": `); err == nil {
		t.Error("broken JSON should be an error")
	}
	// Valid input that simply has nothing useful in it is not an error here;
	// the caller reports which required cookies are missing.
	got, err := parseCookieBlob("hello world")
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want no cookies and no error", got, err)
	}
}

func TestMissingCookies(t *testing.T) {
	full := map[string]string{"SID": "a", "HSID": "b", "SSID": "c", "APISID": "d", "SAPISID": "e", "OSID": "f"}
	if missing := missingCookies(full); len(missing) != 0 {
		t.Errorf("complete set reported missing: %v", missing)
	}
	delete(full, "OSID")
	full["SID"] = "  "
	missing := missingCookies(full)
	if len(missing) != 2 || missing[0] != "SID" || missing[1] != "OSID" {
		t.Errorf("got %v, want [SID OSID]", missing)
	}
}

// Chromium 130+ prepends SHA256(domain) to the plaintext; older versions do
// not, so the prefix has to be detected rather than assumed.
func TestStripDomainPrefix(t *testing.T) {
	value := []byte("g.a000-cookie-value")
	if got := string(stripDomainPrefix(value, ".google.com")); got != string(value) {
		t.Errorf("unprefixed value was altered: %q", got)
	}
	prefixed := append(sha256Sum(".google.com"), value...)
	if got := string(stripDomainPrefix(prefixed, ".google.com")); got != string(value) {
		t.Errorf("prefixed value = %q, want %q", got, value)
	}
	// The host spelling in the database may differ by the leading dot.
	if got := string(stripDomainPrefix(prefixed, "google.com")); got != string(value) {
		t.Errorf("dot-mismatched host = %q, want %q", got, value)
	}
}
