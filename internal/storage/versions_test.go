package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestDatabaseOptimisticWritesWithoutS3Conditions(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		name := "plain"
		if encrypted {
			name = "encrypted"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := testutil.Database(t)
			fixture, cfg := testutil.ObjectStore(t)
			registry, book := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, name, encrypted, "crypt password")
			s, _, err := registry.Open(ctx, testutil.Owner, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			key := testutil.Owner + "/notes/note.md"
			fixture.RejectConditionalWrites.Store(true)
			_, err = s.Put(ctx, key, []byte("# Initial"), "*", "text/markdown")
			var failure *storage.Failure
			if !errors.As(err, &failure) || failure.Feature != "conditional_write" {
				t.Fatal("strict mode must never fall back implicitly", err)
			}
			connection, err := registry.Repo.Credentials(ctx, testutil.Owner, book.ConnectionID)
			if err != nil || !connection.ConditionalWrites {
				t.Fatal(connection, err)
			}
			connection.ConditionalWrites = false
			if err = registry.Repo.SaveConnection(ctx, &connection); err != nil {
				t.Fatal(err)
			}
			// The existing handle sees the policy change without restart/reopen.
			first, err := s.Put(ctx, key, []byte("# Initial"), "*", "text/markdown")
			if err != nil || first.Validator() == first.ETag || !strings.HasPrefix(first.Validator(), `"notes-db-`) {
				t.Fatal(first, err)
			}
			if _, err = s.Put(ctx, key, []byte("duplicate"), "*", "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("existing object overwritten by create", err)
			}
			if _, err = s.Put(ctx, key, []byte("raw validator"), first.ETag, "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("S3 ETag bypassed DB revision check", err)
			}
			second, err := s.Put(ctx, key, []byte("# Initial"), first.Validator(), "text/markdown")
			if err != nil || second.Validator() == first.Validator() {
				t.Fatal("identical-content save did not advance DB revision", second, err)
			}
			if _, err = s.Put(ctx, key, []byte("stale"), first.Validator(), "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("stale DB revision accepted", err)
			}
			if err = s.Delete(ctx, key, first.Validator()); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("stale delete accepted", err)
			}
			_, read, err := s.Get(ctx, key, "", 4096)
			if err != nil || read.Validator() != second.Validator() {
				t.Fatal("read did not preserve DB revision", read, err)
			}
			// Two independent handles sharing the database represent replicas.
			otherRegistry := &storage.Registry{Repo: &store.Repository{Pool: pool}, Config: cfg}
			other, _, err := otherRegistry.Open(ctx, testutil.Owner, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, handle := range []storage.Store{s, other} {
				go func(handle storage.Store) {
					<-start
					_, err := handle.Put(ctx, key, []byte("# Initial"), second.Validator(), "text/markdown")
					results <- err
				}(handle)
			}
			close(start)
			wins, conflicts := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					wins++
				} else if errors.Is(err, storage.ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if wins != 1 || conflicts != 1 {
				t.Fatal("concurrent same-content saves did not conflict", wins, conflicts)
			}
			_, current, err := s.Get(ctx, key, "", 4096)
			if err != nil {
				t.Fatal(err)
			}
			fixture.FailPut.Store(true)
			if _, err = s.Put(ctx, key, []byte("failed"), current.Validator(), "text/markdown"); err == nil {
				t.Fatal("provider failure swallowed")
			}
			fixture.FailPut.Store(false)
			_, afterFailure, err := s.Get(ctx, key, "", 4096)
			if err != nil || afterFailure.Validator() != current.Validator() {
				t.Fatal("failed write consumed DB revision", err)
			}
			// An external writer bypasses Notes' DB, as an independent rclone
			// client would. The fresh S3 check still detects completed edits.
			direct, closeDirect, err := registry.Direct(connection, book, store.Secrets{Password: "crypt password"})
			if err != nil {
				t.Fatal(err)
			}
			defer closeDirect()
			if _, err = direct.Put(ctx, key, []byte("# External"), current.ETag, "text/markdown"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Put(ctx, key, []byte("stale external"), current.Validator(), "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("completed external edit overwritten", err)
			}
			body, external, err := s.Get(ctx, key, "", 4096)
			if err != nil || string(body) != "# External" || external.Validator() == current.Validator() {
				t.Fatal(string(body), external, err)
			}
			if err = s.Delete(ctx, key, external.Validator()); err != nil {
				t.Fatal(err)
			}
			recreated, err := s.Put(ctx, key, []byte("# External"), "*", "text/markdown")
			if err != nil || recreated.Validator() == external.Validator() {
				t.Fatal(recreated, err)
			}
			if err = s.Delete(ctx, key, external.Validator()); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("delete/recreate reused a stale revision", err)
			}
			// Clearing only the search index cannot reset concurrency protection.
			if _, err = pool.Exec(ctx, `DELETE FROM objects`); err != nil {
				t.Fatal(err)
			}
			_, indexed, err := s.Get(ctx, key, "", 4096)
			if err != nil || indexed.Validator() != recreated.Validator() {
				t.Fatal("search rebuild reset write revision", err)
			}
			// A policy change invalidates old editor tokens, even if storage
			// content returns to exactly the same bytes while strict mode is on.
			connection.ConditionalWrites = true
			if err = registry.Repo.SaveConnection(ctx, &connection); err != nil {
				t.Fatal(err)
			}
			connection.ConditionalWrites = false
			if err = registry.Repo.SaveConnection(ctx, &connection); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Put(ctx, key, []byte("policy stale"), recreated.Validator(), "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("validator survived mode changes", err)
			}
		})
	}
}
