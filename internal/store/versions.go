package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ObserveVersion uses the connection creator, connection UUID and bucket/physical
// key, under the physical-object advisory lock with current
// S3 metadata. External changes invalidate previously issued database revisions.
// Search-index rows are never used to authorize writes.
func (r *Repository) ObserveVersion(ctx context.Context, tx pgx.Tx, owner, scope, key, etag, version string, present bool) (string, error) {
	var revision string
	err := tx.QueryRow(ctx, `INSERT INTO object_write_versions AS v(scope,owner,key,revision,etag,version_id,present)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(scope,key) DO UPDATE SET
 revision=CASE WHEN (v.etag,v.present) IS DISTINCT FROM (EXCLUDED.etag,EXCLUDED.present)
 OR (v.version_id<>'' AND EXCLUDED.version_id<>'' AND v.version_id<>EXCLUDED.version_id)
 THEN EXCLUDED.revision ELSE v.revision END,
 etag=EXCLUDED.etag,version_id=EXCLUDED.version_id,present=EXCLUDED.present
 WHERE v.owner=EXCLUDED.owner RETURNING revision::text`, scope, owner, key, uuid.NewString(), etag, version, present).Scan(&revision)
	return revision, translate(err)
}

// ClaimVersion performs the optimistic compare-and-swap before touching S3.
// The caller keeps the transaction/object lock through the operation. Failure
// rolls back the claim; identical-content saves still get distinct revisions.
func (r *Repository) ClaimVersion(ctx context.Context, tx pgx.Tx, owner, scope, key, expected string) (string, error) {
	next := uuid.NewString()
	var revision string
	err := tx.QueryRow(ctx, `UPDATE object_write_versions SET revision=$5 WHERE scope=$1 AND owner=$2 AND key=$3 AND revision::text=$4 RETURNING revision::text`, scope, owner, key, expected, next).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConflict
	}
	return revision, err
}

func (r *Repository) RecordVersion(ctx context.Context, tx pgx.Tx, owner, scope, key, revision, etag, version string, present bool) error {
	result, err := tx.Exec(ctx, `UPDATE object_write_versions SET etag=$5,version_id=$6,present=$7 WHERE scope=$1 AND owner=$2 AND key=$3 AND revision::text=$4`, scope, owner, key, revision, etag, version, present)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}
