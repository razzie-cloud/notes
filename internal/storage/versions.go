package storage

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/razzie-cloud/notes/internal/store"
)

func databaseValidator(revision string) string { return `"notes-db-` + revision + `"` }

func (m *Managed) observe(ctx context.Context, tx pgx.Tx, key string, meta Meta, present bool) (string, error) {
	revision, err := m.Registry.Repo.ObserveVersion(ctx, tx, m.Owner, m.ID, key, meta.ETag, meta.Version, present)
	return databaseValidator(revision), err
}

func (m *Managed) claim(ctx context.Context, tx pgx.Tx, s Store, key, expected string) (underlying, next string, err error) {
	meta, err := s.Head(ctx, key)
	present := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	revision, err := m.Registry.Repo.ObserveVersion(ctx, tx, m.Owner, m.ID, key, meta.ETag, meta.Version, present)
	if err != nil {
		return "", "", err
	}
	if expected == "" || expected == "*" && present || expected != "*" && (!present || expected != databaseValidator(revision)) {
		return "", "", ErrConflict
	}
	next, err = m.Registry.Repo.ClaimVersion(ctx, tx, m.Owner, m.ID, key, revision)
	if errors.Is(err, store.ErrConflict) {
		err = ErrConflict
	}
	underlying = meta.ETag
	if !present {
		underlying = "*"
	}
	return underlying, next, err
}

func (m *Managed) record(ctx context.Context, tx pgx.Tx, key, revision string, meta Meta, present bool) {
	if err := m.Registry.Repo.RecordVersion(ctx, tx, m.Owner, m.ID, key, revision, meta.ETag, meta.Version, present); err != nil {
		// Never report a successful S3 mutation as failed. The transaction's
		// owner will attempt commit; a fresh read repairs coordination metadata.
		slog.Warn("Storage saved; reload to refresh write revision", "event", "notes.write_revision_record_failed", "error_kind", "dependency")
	}
}
