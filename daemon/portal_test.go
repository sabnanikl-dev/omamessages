package main

import "testing"

func TestPickFilesURIToPath(t *testing.T) {
	good := map[string]string{
		"file:///home/x/a%20b.png":            "/home/x/a b.png",
		"file:///home/x/%C3%A9t%C3%A9.jpg":    "/home/x/été.jpg",
		"file://localhost/tmp/notes.md":       "/tmp/notes.md",
		"file:///home/x/100%25%20done%23.txt": "/home/x/100% done#.txt",
	}
	for in, want := range good {
		got, err := uriToPath(in)
		if err != nil || got != want {
			t.Errorf("uriToPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"https://example.com/a.png", "smb://server/share/a.png", "file://otherhost/a.png", "file://", "a.png"} {
		if got, err := uriToPath(bad); err == nil {
			t.Errorf("uriToPath(%q) = %q; want an error", bad, got)
		}
	}
}
