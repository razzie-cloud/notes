package app

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/store"
	"golang.org/x/net/html"
)

// Navigable pages must update notebook context and selected sidebar entries
// together, while dialogs, downloads and native mutations retain their targets.
func TestPanelNavigationDestinations(t *testing.T) {
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	book := store.Notebook{ID: "book-id", Name: "Notebook", SecretID: "recovery-id"}
	connection := store.Connection{ID: "connection-id", Name: "S3"}
	views := map[string]View{
		"library":     {Notebook: book, Notebooks: []store.Notebook{book, {ID: "other-book"}}, Writable: true, Query: "search", Tag: "tag", Tags: []TagCount{{Name: "tag"}}, Notes: []Note{{Key: "note.md", Title: "Note", Tags: []string{"tag"}}}, HasNext: true, Offset: 50},
		"reader":      {Notebook: book, Reading: true, Writable: true, Key: "note.md", Title: "Note", NoteTags: []string{"tag"}, Attachments: []Attachment{{Key: "note.md/file.pdf"}}, Rendered: renderMarkdown("[Note](other.md) [File](file.pdf) [External](https://example.com) [Section](#heading)", book.ID, "note.md")},
		"editor":      {Notebook: book, Editing: true, Writable: true, Key: "note.md", ETag: `"etag"`, Attachments: []Attachment{{Key: "note.md/file.pdf", WriteETag: `"etag"`}}},
		"connections": {StoragePage: true, Connections: []store.Connection{connection}, EditConnection: &connection, ConnectionForm: true},
		"notebooks":   {NotebooksPage: true, NotebookCards: []NotebookCard{{Notebook: book}}, Connections: []store.Connection{connection}, EditNotebook: &book, NotebookForm: true},
		"first-use":   {StorageNeeded: true},
	}
	for name, view := range views {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := a.templates.ExecuteTemplate(&out, "page", view); err != nil {
				t.Fatal(err)
			}
			root, err := html.Parse(&out)
			if err != nil {
				t.Fatal(err)
			}
			var visit func(*html.Node)
			visit = func(n *html.Node) {
				attr := func(key string) string {
					for _, a := range n.Attr {
						if a.Key == key {
							return a.Val
						}
					}
					return ""
				}
				boost := attr("hx-boost") == "true"
				if n.Type == html.ElementNode && n.Data == "a" {
					u, err := url.Parse(attr("href"))
					if err != nil {
						t.Fatal(err)
					}
					panel := !u.IsAbs() && u.Path != "" && !strings.HasPrefix(u.Path, "/v1/") && !strings.HasPrefix(u.Path, "/recovery/") && attr("target") == "" && attr("download") == "" && attr("class") != "brand" && attr("class") != "account-link"
					// A download attribute may have no value.
					for _, a := range n.Attr {
						if a.Key == "download" {
							panel = false
						}
					}
					if attr("hx-get") != "" {
						if boost || !strings.HasSuffix(attr("hx-target"), "-dialog-content") || attr("hx-push-url") == "true" {
							t.Errorf("dialog link became page navigation: %v", n.Attr)
						}
					} else if panel != boost {
						t.Errorf("wrong navigation behavior for %s: boost=%t", u, boost)
					}
				}
				if n.Type == html.ElementNode && n.Data == "form" {
					if (attr("method") == "get") != boost {
						t.Errorf("only navigational GET forms should be boosted: %v", n.Attr)
					}
				}
				if boost {
					if attr("hx-target") != "#app-layout" || attr("hx-select") != "#app-layout" || !strings.HasPrefix(attr("hx-swap"), "outerHTML") || attr("hx-push-url") != "true" {
						t.Errorf("navigation lost context/history contract: %v", n.Attr)
					}
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(root)
		})
	}
}
