package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Qt's default Text format guesses whether a string is rich text, so a
// sender name like `<img src="https://…">` would be rendered, and fetch a
// remote image when a thread opens. Every Text in the panel must say how its
// text is read; anything showing remote data must be plain.
func TestEveryQMLTextDeclaresItsFormat(t *testing.T) {
	var files []string
	for _, pat := range []string{"../*.qml", "../views/*.qml", "../components/*.qml"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	if len(files) < 5 {
		t.Fatalf("found only %d QML files; run from daemon/", len(files))
	}
	open := regexp.MustCompile(`^(\s*)Text\s*\{\s*$`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			m := open.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			own := regexp.MustCompile(`^` + regexp.QuoteMeta(m[1]) + `  textFormat\s*:`)
			depth, found := 0, false
			for j := i; j < len(lines); j++ {
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if j > i && own.MatchString(lines[j]) {
					found = true
				}
				if depth == 0 {
					break
				}
			}
			if !found {
				t.Errorf("%s:%d: Text without textFormat", filepath.Base(f), i+1)
			}
		}
	}
}
