package core

import "testing"

func TestJoinSplitID(t *testing.T) {
	cases := []struct {
		p      ProviderID
		native string
	}{
		{GMessages, "42"},
		{Telegram, "-100123"},
		{WhatsApp, "4915112345678@s.whatsapp.net"},
		{"fake", "odd:native:id"},
	}
	for _, c := range cases {
		joined := JoinID(c.p, c.native)
		p, native, err := SplitID(joined)
		if err != nil {
			t.Fatalf("SplitID(%q): %v", joined, err)
		}
		if p != c.p || native != c.native {
			t.Errorf("SplitID(%q) = %q, %q; want %q, %q", joined, p, native, c.p, c.native)
		}
	}
	for _, bad := range []string{"", "42", ":42", "telegram:"} {
		if _, _, err := SplitID(bad); err == nil {
			t.Errorf("SplitID(%q) succeeded; want an error", bad)
		}
	}
}
