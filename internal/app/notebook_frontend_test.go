package app

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/store"
)

func TestNotebookSettingsPresentation(t *testing.T) {
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Connection{ID: "connection-id", Name: "<storage>", ReadOnly: true}
	n := store.Notebook{ID: "notebook-id", ConnectionID: c.ID, Name: "<notebook>", Bucket: "test", Prefix: "private", Encrypted: true, SecretID: "recovery-id", Revision: 7}
	for _, tc := range []struct {
		name                string
		populated, explicit bool
	}{
		{"first-use", false, false}, {"list", true, false}, {"fallback", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := View{NotebooksPage: true, Connections: []store.Connection{c}, EditNotebook: &n, NotebookConnection: c, NotebookForm: tc.explicit}
			if tc.populated {
				v.NotebookCards = []NotebookCard{{Notebook: n, ConnectionName: c.Name, ConnectionReadOnly: true, EffectiveReadOnly: true}}
			} else {
				v.EditNotebook = &store.Notebook{}
			}
			var out bytes.Buffer
			if err := a.templates.ExecuteTemplate(&out, "page", v); err != nil {
				t.Fatal(err)
			}
			html := out.String()
			hasForm := strings.Contains(html, "data-notebook-form")
			if hasForm != (!tc.populated || tc.explicit) {
				t.Fatalf("form visibility: %s", html)
			}
			if strings.Contains(html, "<notebook>") || strings.Contains(html, "<storage>") {
				t.Fatal("unescaped metadata")
			}
			if strings.Count(html, `aria-current="page"`) != 1 || !strings.Contains(html, `href="/notebooks" aria-current="page"`) {
				t.Fatal("incorrect sidebar selection")
			}
			if tc.populated && !tc.explicit {
				for _, want := range []string{`hx-target="#notebook-dialog-content"`, `href="/recovery/recovery-id"`, "Read-only access is set by the connection."} {
					if !strings.Contains(html, want) {
						t.Fatal("missing", want)
					}
				}
			}
			if tc.explicit {
				if strings.Contains(html, `id="add-notebook"`) {
					t.Fatal("fallback form also offers a modal form")
				}
				for _, want := range []string{`name="revision" value="7"`, `hx-target="#notebook-feedback"`, "Location and encryption are fixed."} {
					if !strings.Contains(html, want) {
						t.Fatal("missing", want)
					}
				}
				for _, bad := range []string{`name="connection_id"`, `name="crypt_password"`, `name="bucket"`} {
					if strings.Contains(html, bad) {
						t.Fatal("mutable edit location", bad)
					}
				}
			}
			if !tc.populated && !strings.Contains(html, `name="connection_id"`) {
				t.Fatal("first-use form cannot select a connection")
			}
		})
	}
	req := httptest.NewRequest("GET", "/notebooks?notebook=new", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "notebook-dialog-content")
	out := httptest.NewRecorder()
	a.renderNotebooks(out, req, View{Connections: []store.Connection{c}, EditNotebook: &store.Notebook{}}, nil)
	html := out.Body.String()
	if out.Code != 200 || strings.Contains(html, "<html") || !strings.Contains(html, "data-close-notebook") {
		t.Fatal("invalid fragment", html)
	}
	for _, name := range []string{"connection_id", "bucket", "prefix", "encrypted", "crypt_password", "crypt_password2", "filename_encryption", "filename_encoding", "directory_encryption", "suffix", "new_crypt_prefix", "verify_path"} {
		if !strings.Contains(html, `name="`+name+`"`) {
			t.Fatal("missing creation field", name)
		}
	}
}

func TestNotebookErrorsKeepDraftInBrowser(t *testing.T) {
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"notebook-feedback", "notebook-dialog-content"} {
		req := httptest.NewRequest("POST", "/notebooks", strings.NewReader("crypt_password=submitted-secret"))
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", target)
		out := httptest.NewRecorder()
		a.fail(out, req, store.ErrConflict)
		want := "#notebook-feedback"
		if target == "notebook-dialog-content" {
			want = "#notebook-list-feedback"
		}
		html := out.Body.String()
		if out.Code != 409 || out.Header().Get("HX-Retarget") != want || !strings.Contains(html, `role="alert"`) || strings.Contains(html, "<form") || strings.Contains(html, "submitted-secret") {
			t.Fatalf("invalid error: %d %s", out.Code, html)
		}
	}
}
