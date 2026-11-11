package storage_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestPaths(t *testing.T) {
	for _, key := range []string{"a.md", "nested/árvíz.md"} {
		if !storage.Path(key) {
			t.Fatal(key)
		}
	}
	for _, key := range []string{"", "/a.md", "../a.md", "a/../b.md", "a//b.md", "a\\b.md", "a/", "a\n.md", ".", ".."} {
		if storage.Path(key) {
			t.Fatal(key)
		}
	}
	root := "notes/" + testutil.Owner + "/"
	for _, key := range []string{root + "notes/nested/a.md", root + "attachments/nested/a.md/report.pdf"} {
		o, _, _, _, ok := storage.Split("notes/", key)
		if !ok || o != testutil.Owner {
			t.Fatal(key)
		}
	}
	for _, key := range []string{root + "notes/a.pdf", root + "attachments/unowned/file.pdf", root + "notes/../a.md", "notes/not-a-user/notes/a.md", "other/" + testutil.Owner + "/notes/a.md"} {
		if _, _, _, _, ok := storage.Split("notes/", key); ok {
			t.Fatal(key)
		}
	}
}
func TestDestinationPolicy(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.3", "64:ff9b::a00:1"} {
		if storage.Public(netip.MustParseAddr(s)) {
			t.Fatal(s)
		}
	}
	if !storage.Public(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public blocked")
	}
	for _, url := range []string{"http://127.0.0.1:9000", "https://name/path", "https://user:secret@name", "https://name?secret=value"} {
		if _, e := storage.HTTPClient(url, "", false); e == nil {
			t.Fatal(url)
		}
	}
	client, e := storage.HTTPClient("https://127.0.0.1", "", false)
	if e != nil {
		t.Fatal(e)
	}
	resp, e := client.Get("https://127.0.0.1")
	if e == nil {
		resp.Body.Close()
		t.Fatal("private destination accepted")
	}
	if !strings.Contains(e.Error(), "blocked") {
		t.Fatal(e)
	}
}
func TestS3Preconditions(t *testing.T) {
	f, _ := testutil.ObjectStore(t)
	s := f.Store
	ctx := context.Background()
	key := "notes/" + testutil.Owner + "/notes/a.md"
	first, e := s.Put(ctx, key, []byte("first"), "*", "text/markdown")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Put(ctx, key, []byte("duplicate"), "*", "text/markdown"); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	second, e := s.Put(ctx, key, []byte("second"), first.ETag, "text/markdown")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Put(ctx, key, []byte("stale"), first.ETag, "text/markdown"); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, key, first.ETag); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	if _, _, e = s.Get(ctx, key, first.ETag, 100); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	b, meta, e := s.Get(ctx, key, second.ETag, 100)
	if e != nil || string(b) != "second" || meta.ETag != second.ETag {
		t.Fatal(string(b), meta, e)
	}
	if _, _, e = s.Get(ctx, key, "", 1); !errors.Is(e, storage.ErrTooLarge) {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, key, second.ETag); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Head(ctx, key); !errors.Is(e, storage.ErrNotFound) {
		t.Fatal(e)
	}
}
