package main

import "testing"

func TestNotificationBodyIsLiteral(t *testing.T) {
	for in, want := range map[string]string{
		`<img src="https://x.example/p.png">`:         `&lt;img src="https://x.example/p.png"&gt;`,
		`<a href="https://evil.example">bank.com</a>`: `&lt;a href="https://evil.example"&gt;bank.com&lt;/a&gt;`,
		"Tom & Jerry":         "Tom &amp; Jerry",
		"plain text, no tags": "plain text, no tags",
	} {
		if got := escapeMarkup(in); got != want {
			t.Errorf("escapeMarkup(%q) = %q; want %q", in, got, want)
		}
	}
}
