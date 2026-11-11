package index_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func fixture(t *testing.T) (*index.Engine, *testutil.S3, config.Config) {
	t.Helper()
	pool := testutil.Database(t)
	s, c := testutil.ObjectStore(t)
	registry, n := testutil.Attach(t, pool, s, c, testutil.Owner, "data", false, "")
	root, err := index.New(pool, registry, c)
	if err != nil {
		t.Fatal(err)
	}
	e, err := root.Bind(context.Background(), testutil.Owner, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = &storage.Mapped{Raw: s.Store, Owner: testutil.Owner, Prefix: "data/"}
	return e, s, c
}
func put(t *testing.T, s storage.Store, key, body string) {
	t.Helper()
	ctx := context.Background()
	etag := "*"
	m, e := s.Head(ctx, key)
	if e == nil {
		etag = m.ETag
	} else if !errors.Is(e, storage.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = s.Put(ctx, key, []byte(body), etag, "text/markdown"); e != nil {
		t.Fatal(e)
	}
}
func runScan(t *testing.T, e *index.Engine) {
	t.Helper()
	for i := 0; i < 100; i++ {
		done, err := e.ScanPage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("scan did not finish")
}
func drain(t *testing.T, e *index.Engine) {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(), `SELECT args->>'key' FROM river_job WHERE kind='index_object' AND args->>'scope'=$1 ORDER BY id`, e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for rows.Next() {
		var k string
		if err = rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err = e.Index(context.Background(), k); err != nil {
			t.Fatal(k, err)
		}
	}
	if _, err = e.Pool.Exec(context.Background(), `DELETE FROM river_job WHERE kind='index_object' AND args->>'scope'=$1`, e.Scope); err != nil {
		t.Fatal(err)
	}
}
func count(t *testing.T, e *index.Engine, want int) {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM objects WHERE scope=$1`, e.Scope).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("objects=%d want %d", n, want)
	}
}
func TestRebuildAndOutOfBandChanges(t *testing.T) {
	e, s, _ := fixture(t)
	root := e.Prefix + testutil.Owner + "/"
	a := root + "notes/a.md"
	attachment := root + "attachments/a.md/report.pdf"
	put(t, s.Store, a, "---\ntags: [magyar]\n---\n# Árvíztűrő\nTükörfúrógép őrzött jegyzet")
	put(t, s.Store, attachment, "binary")

	put(t, s.Raw, "unrelated.md", "outside")
	runScan(t, e)
	drain(t, e)
	count(t, e, 2)
	gets := s.Gets.Load()
	runScan(t, e)
	drain(t, e)
	if s.Gets.Load() != gets {
		t.Fatal("unchanged content was downloaded")
	}
	put(t, s.Store, a, "# Updated\nLatest")
	put(t, s.Store, root+"notes/new.md", "# New")
	runScan(t, e)
	drain(t, e)
	count(t, e, 3)
	m, _ := s.Store.Head(context.Background(), a)
	if err := s.Store.Delete(context.Background(), a, m.ETag); err != nil {
		t.Fatal(err)
	}
	put(t, s.Store, root+"notes/moved.md", "# Updated\nLatest")
	runScan(t, e)
	drain(t, e)
	count(t, e, 3)
	// Wiping the complete derived state preserves S3. The next scan rebuilds it.
	if _, err := e.Pool.Exec(context.Background(), `TRUNCATE objects,index_scans,scan_seen,river_job`); err != nil {
		t.Fatal(err)
	}
	runScan(t, e)
	drain(t, e)
	count(t, e, 3)
	if _, err := s.Store.Head(context.Background(), attachment); err != nil {
		t.Fatal("attachment lost", err)
	}
}

type faulty struct {
	storage.Store
	fail bool
}

func (f *faulty) List(ctx context.Context, prefix, cursor string) (storage.Page, error) {
	if f.fail {
		return storage.Page{}, errors.New("transient")
	}
	return f.Store.List(ctx, prefix, cursor)
}
func TestInterruptedScanAndConcurrentChanges(t *testing.T) {
	e, s, _ := fixture(t)
	root := e.Prefix + testutil.Owner + "/notes/"
	// More than one S3 page exercises persisted continuation and no early deletes.
	for i := 0; i < 205; i++ {
		put(t, s.Store, fmt.Sprintf("%s%03d.md", root, i), "# Existing")
	}
	runScan(t, e)
	drain(t, e)
	count(t, e, 205)
	if done, err := e.ScanPage(context.Background()); err != nil || done {
		t.Fatal(done, err)
	}
	f := &faulty{Store: s.Store, fail: true}
	e.Storage = f
	if _, err := e.ScanPage(context.Background()); err == nil {
		t.Fatal("failed scan succeeded")
	}
	count(t, e, 205)
	var status string
	e.Pool.QueryRow(context.Background(), `SELECT status FROM index_scans WHERE scope=$1`, e.Scope).Scan(&status)
	if status != "listing" {
		t.Fatal("failed scan advanced", status)
	}
	f.fail = false
	// A new key sorts before the saved listing cursor. Index it concurrently;
	// it will be missing from scan_seen, but sweep must retain its current S3 data.
	concurrent := root + "000a.md"
	put(t, s.Store, concurrent, "# During scan")
	if err := e.Index(context.Background(), concurrent); err != nil {
		t.Fatal(err)
	}
	// Delete an object before its later LIST page. Only the completed scan can
	// enqueue the absence check for its previously indexed row.
	missing := root + "204.md"
	m, _ := s.Store.Head(context.Background(), missing)
	if err := s.Store.Delete(context.Background(), missing, m.ETag); err != nil {
		t.Fatal(err)
	}
	runScan(t, e)
	drain(t, e)
	count(t, e, 205)
	var kept int
	if err := e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM objects WHERE scope=$1 AND key=$2`, e.Scope, concurrent).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("concurrent object removed", kept, err)
	}
	// Start another scan, then update an existing key during its listing.
	if _, err := e.ScanPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	runScan(t, e)
	key := root + "000.md"
	put(t, s.Store, key, "# Concurrent update")
	if err := e.Index(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	count(t, e, 205)
	// A transient HEAD failure must not be interpreted as authoritative absence.
	s.FailHead.Store(true)
	if err := e.Index(context.Background(), key); err == nil {
		t.Fatal("transient HEAD accepted")
	}
	count(t, e, 205)
	s.FailHead.Store(false)
	if err := e.Index(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	// Duplicate scans from multiple replicas share one durable progress record.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := e.ScanPage(context.Background()); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	runScan(t, e)
	drain(t, e)
	count(t, e, 205)
}

type racing struct {
	storage.Store
	key  string
	once sync.Once
}

func (s *racing) Get(ctx context.Context, key, etag string, limit int64) ([]byte, storage.Meta, error) {
	b, m, e := s.Store.Get(ctx, key, etag, limit)
	if e == nil && key == s.key {
		s.once.Do(func() { _, e = s.Store.Put(ctx, key, []byte("# Newest"), m.ETag, "text/markdown") })
	}
	return b, m, e
}
func TestStaleJobsAndGetRace(t *testing.T) {
	e, s, _ := fixture(t)
	key := e.Prefix + testutil.Owner + "/notes/a.md"
	put(t, s.Store, key, "# Old")
	e.Storage = &racing{Store: s.Store, key: key}
	if err := e.Index(context.Background(), key); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("GET race not detected", err)
	}
	count(t, e, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.Index(context.Background(), key) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var title string
	if err := e.Pool.QueryRow(context.Background(), `SELECT title FROM objects WHERE key=$1 AND scope=$2`, key, e.Scope).Scan(&title); err != nil || title != "Newest" {
		t.Fatal(title, err)
	}
	m, _ := s.Store.Head(context.Background(), key)
	s.Store.Delete(context.Background(), key, m.ETag)
	if err := e.Index(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	count(t, e, 0)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Index(canceled, key); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestRiverReplicasAndRestart(t *testing.T) {
	e, s, c := fixture(t)
	other, err := index.New(e.Pool, e.Registry, c)
	if err != nil {
		t.Fatal(err)
	}
	key := e.Prefix + testutil.Owner + "/notes/river.md"
	put(t, s.Store, key, "# River\nÁrvíztűrő")
	ctx := context.Background()
	for _, eng := range []*index.Engine{e, other} {
		if err = eng.Client.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := eng.Client.StopAndCancel(stop); err != nil {
				t.Error(err)
			}
		})
	}
	await := func(title string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var got string
			err := e.Pool.QueryRow(ctx, `SELECT title FROM objects WHERE scope=$1 AND key=$2`, e.Scope, key).Scan(&got)
			if err == nil && got == title {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("River did not converge to", title)
	}
	await("River")
	for range 8 {
		if err = e.Enqueue(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	put(t, s.Store, key, "# Changed after enqueue")
	if err = e.Enqueue(ctx, key); err != nil {
		t.Fatal(err)
	}
	await("Changed after enqueue")
	// Stop both replicas, update S3, and start a fresh client; RunOnStart recovers.
	for _, eng := range []*index.Engine{e, other} {
		stop, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err = eng.Client.StopAndCancel(stop); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	put(t, s.Store, key, "# After restart")
	fresh, err := index.New(e.Pool, e.Registry, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer fresh.Client.StopAndCancel(ctx)
	await("After restart")
	var discarded int
	if err = e.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state='discarded'`).Scan(&discarded); err != nil || discarded != 0 {
		t.Fatal("discarded jobs", discarded, err)
	}
	var body string
	e.Pool.QueryRow(ctx, `SELECT body FROM objects WHERE scope=$1 AND key=$2`, e.Scope, key).Scan(&body)
	if !strings.Contains(body, "After restart") {
		t.Fatal(body)
	}
}

func TestLargeMarkdownKeepsAuthoritativeContent(t *testing.T) {
	e, s, _ := fixture(t)
	key := e.Prefix + testutil.Owner + "/notes/large.md"
	body := "# Large note\nSearchableprefix\n" + strings.Repeat("ordinary ", 12000) + "Tailcontent"
	put(t, s.Store, key, body)
	if err := e.Index(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	var indexed string
	var matches bool
	if err := e.Pool.QueryRow(context.Background(), `SELECT body,search @@ plainto_tsquery('simple','searchableprefix') FROM objects WHERE scope=$1 AND key=$2`, e.Scope, key).Scan(&indexed, &matches); err != nil || indexed != body || !matches {
		t.Fatal("large document indexing", len(indexed), matches, err)
	}
	raw, _, err := s.Store.Get(context.Background(), key, "", e.MaxNoteBytes)
	if err != nil || string(raw) != body {
		t.Fatal("large document changed", err)
	}
}

func TestEncryptedNotebookRebuild(t *testing.T) {
	e, s, cfg := fixture(t)
	ctx := context.Background()
	registry, n := testutil.Attach(t, e.Pool, s, cfg, testutil.Owner, "encrypted", true, "rebuild password")
	root, err := index.New(e.Pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	book, err := root.Bind(ctx, testutil.Owner, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := storage.Cipher(n, store.Secrets{Password: "rebuild password"})
	if err != nil {
		t.Fatal(err)
	}
	direct := &storage.Mapped{Raw: s.Raw, Owner: testutil.Owner, Prefix: "encrypted/", Cipher: cipher}
	key := testutil.Owner + "/notes/rebuild.md"
	attachment := testutil.Owner + "/attachments/rebuild.md/a.txt"
	put(t, direct, key, "---\ntags: [magyar]\n---\n# Árvíztűrő encrypted\nTükörfúrógép")
	put(t, direct, attachment, "attachment")
	runScan(t, book)
	drain(t, book)
	count(t, book, 2)
	count(t, e, 0)
	downloads := s.Gets.Load()
	runScan(t, book)
	drain(t, book)
	if downloads != s.Gets.Load() {
		t.Fatal("unchanged encrypted content downloaded")
	}
	if _, err = e.Pool.Exec(ctx, `TRUNCATE objects,scan_seen,index_scans,river_job`); err != nil {
		t.Fatal(err)
	}
	runScan(t, book)
	drain(t, book)
	count(t, book, 2)
	put(t, direct, key, "# Updated out of band")
	runScan(t, book)
	drain(t, book)
	var title string
	if err = e.Pool.QueryRow(ctx, `SELECT title FROM objects WHERE scope=$1 AND key=$2`, book.Scope, key).Scan(&title); err != nil || title != "Updated out of band" {
		t.Fatal(title, err)
	}
	m, err := direct.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = direct.Delete(ctx, key, m.ETag); err != nil {
		t.Fatal(err)
	}
	runScan(t, book)
	drain(t, book)
	count(t, book, 1)
	// Removing the mapping invalidates jobs/handles and cascades only the index.
	if err = registry.Repo.RemoveNotebook(ctx, testutil.Owner, n.ID, n.Revision); err != nil {
		t.Fatal(err)
	}
	count(t, book, 0)
	if _, _, err = book.Storage.Get(ctx, attachment, "", 4096); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err = direct.Get(ctx, attachment, "", 4096); err != nil {
		t.Fatal("ciphertext was removed", err)
	}
}

func TestIndexAndScanReuseOneConnection(t *testing.T) {
	e, s, _ := fixture(t)
	cfg := e.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	e.Pool = pool
	e.Registry.Repo.Pool = pool
	key := testutil.Owner + "/notes/single.md"
	put(t, s.Store, key, "# Single connection")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = e.Index(ctx, key); err != nil {
		t.Fatal("indexing required nested pool acquisition", err)
	}
	if _, err = e.ScanPage(ctx); err != nil {
		t.Fatal("scan required nested pool acquisition", err)
	}
}
