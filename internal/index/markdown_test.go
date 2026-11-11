package index

import (
	"strings"
	"testing"
)

func TestMarkdown(t *testing.T) {
	d, e := Parse("import.md", []byte("---\nid: stable\ntitle: Árvíztűrő tükörfúrógép\ntags: [munka, magyar, munka]\ncreated: 2026-11-10T12:00:00Z\n---\n# Heading\nŐrzött gondolatok\n"))
	if e != nil || d.ID != "stable" || d.Title != "Árvíztűrő tükörfúrógép" || len(d.Tags) != 2 || d.Created.IsZero() || !strings.Contains(d.Body, "Őrzött") {
		t.Fatal(d, e)
	}
	for _, test := range []struct{ body, title string }{{"# Imported\nBody", "Imported"}, {"plain text", "fallback"}, {"---\ntitle: Final\n---", "Final"}, {"---\r\ntitle: Windows\r\n---\r\nText", "Windows"}} {
		d, e := Parse("fallback.md", []byte(test.body))
		if e != nil || d.Title != test.title {
			t.Fatal(d, e)
		}
	}
	for _, b := range [][]byte{[]byte("---\ntitle: invalid"), []byte("---\ntags: invalid\n---\ntext"), {0xff}, {0}} {
		if _, e := Parse("bad.md", b); e == nil {
			t.Fatal("accepted malformed Markdown")
		}
	}
}
