// Package store owns durable owner-scoped connection and notebook settings.
// Credentials are held in PostgreSQL, as in Files, with no master key volume.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("storage settings not found")
	ErrConflict = errors.New("storage settings changed, name exists, or prefixes overlap")
	ErrReadOnly = errors.New("storage is read-only")
)

type Connection struct {
	ID, Owner, Name, Provider, Endpoint, Region string
	PathStyle, ReadOnly, ConditionalWrites      bool
	Revision                                    int64
	RetainSession                               bool   `json:"-"`
	AccessKey, SecretKey, SessionToken          string `json:"-"`
}
type Notebook struct {
	ID, Owner, ConnectionID, Name, Bucket, Prefix          string
	ReadOnly, Encrypted, DirectoryEncryption               bool
	FilenameEncryption, FilenameEncoding, Suffix, SecretID string
	Revision                                               int64
	ConnectionRevision                                     int64
}

func (n Notebook) Writable(c Connection) bool { return !n.ReadOnly && !c.ReadOnly }

type Secrets struct{ Password, Password2 string }
type Recovery struct {
	Provider            string `json:"provider"`
	PathStyle           bool   `json:"path_style"`
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Bucket              string `json:"bucket"`
	Prefix              string `json:"prefix"`
	Endpoint            string `json:"endpoint"`
	Region              string `json:"region"`
	FilenameEncryption  string `json:"filename_encryption"`
	FilenameEncoding    string `json:"filename_encoding"`
	DirectoryEncryption bool   `json:"directory_name_encryption"`
	Suffix              string `json:"suffix"`
	Password            string `json:"password"`
	Password2           string `json:"password2"`
}
type Repository struct{ Pool *pgxpool.Pool }

func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(err, &p) && (p.Code == "23505" || p.Code == "23503" || p.Code == "23514") {
		return ErrConflict
	}
	return err
}

const connCols = `id::text,owner::text,name,provider,endpoint,region,path_style,read_only,revision,conditional_writes`
const bookCols = `id,owner::text,connection_id::text,name,bucket,prefix,read_only,encrypted,directory_encryption,filename_encryption,filename_encoding,suffix,COALESCE(secret_id::text,''),revision`

type scanner interface{ Scan(...any) error }

func scanConnection(s scanner) (c Connection, e error) {
	e = s.Scan(&c.ID, &c.Owner, &c.Name, &c.Provider, &c.Endpoint, &c.Region, &c.PathStyle, &c.ReadOnly, &c.Revision, &c.ConditionalWrites)
	return c, translate(e)
}
func scanNotebook(s scanner) (n Notebook, e error) {
	e = s.Scan(&n.ID, &n.Owner, &n.ConnectionID, &n.Name, &n.Bucket, &n.Prefix, &n.ReadOnly, &n.Encrypted, &n.DirectoryEncryption, &n.FilenameEncryption, &n.FilenameEncoding, &n.Suffix, &n.SecretID, &n.Revision)
	return n, translate(e)
}
func validID(id string) bool {
	v, e := uuid.Parse(id)
	return e == nil && v != uuid.Nil && v.String() == id
}
func (r *Repository) Connections(ctx context.Context, owner string) ([]Connection, error) {
	rows, e := r.Pool.Query(ctx, `SELECT `+connCols+` FROM storage_connections WHERE owner=$1 ORDER BY name`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, e := scanConnection(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (r *Repository) Connection(ctx context.Context, owner, id string) (Connection, error) {
	if !validID(id) {
		return Connection{}, ErrNotFound
	}
	return scanConnection(r.Pool.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE owner=$1 AND id=$2`, owner, id))
}
func (r *Repository) Notebooks(ctx context.Context, owner string) ([]Notebook, error) {
	rows, e := r.Pool.Query(ctx, `SELECT `+bookCols+` FROM notebooks WHERE owner=$1 ORDER BY name`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Notebook{}
	for rows.Next() {
		n, e := scanNotebook(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (r *Repository) Notebook(ctx context.Context, owner, id string) (Notebook, error) {
	if !validID(id) {
		return Notebook{}, ErrNotFound
	}
	return scanNotebook(r.Pool.QueryRow(ctx, `SELECT `+bookCols+` FROM notebooks WHERE owner=$1 AND id=$2`, owner, id))
}

// Target is for trusted River jobs; HTTP callers must use Notebook with an owner.
func (r *Repository) Target(ctx context.Context, id string) (Notebook, error) {
	if !validID(id) {
		return Notebook{}, ErrNotFound
	}
	return scanNotebook(r.Pool.QueryRow(ctx, `SELECT `+bookCols+` FROM notebooks WHERE id=$1`, id))
}
func (r *Repository) Targets(ctx context.Context, cursor string) ([]Notebook, error) {
	rows, e := r.Pool.Query(ctx, `SELECT `+bookCols+` FROM notebooks WHERE id>$1 ORDER BY id LIMIT 200`, cursor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Notebook{}
	for rows.Next() {
		n, e := scanNotebook(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Locked reads lock both policy rows until the S3 call finishes. Management
// updates/deletions wait, so a stale handle cannot bypass a read-only change.
func (r *Repository) Locked(ctx context.Context, tx pgx.Tx, owner, id string) (Notebook, Connection, Secrets, error) {
	n, e := scanNotebook(tx.QueryRow(ctx, `SELECT `+bookCols+` FROM notebooks WHERE owner=$1 AND id=$2 FOR SHARE`, owner, id))
	if e != nil {
		return n, Connection{}, Secrets{}, e
	}
	c, e := scanConnection(tx.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE owner=$1 AND id=$2 FOR SHARE`, owner, n.ConnectionID))
	if e != nil {
		return n, c, Secrets{}, e
	}
	e = tx.QueryRow(ctx, `SELECT access_key,secret_key,session_token FROM storage_connections WHERE owner=$1 AND id=$2`, owner, c.ID).Scan(&c.AccessKey, &c.SecretKey, &c.SessionToken)
	if e != nil {
		return n, c, Secrets{}, e
	}
	var secret Secrets
	if n.Encrypted {
		e = tx.QueryRow(ctx, `SELECT password,password2 FROM storage_secrets WHERE owner=$1 AND id=$2`, owner, n.SecretID).Scan(&secret.Password, &secret.Password2)
	}
	return n, c, secret, translate(e)
}
func (r *Repository) SaveConnection(ctx context.Context, c *Connection) error {
	if !validID(c.Owner) || c.ID != "" && !validID(c.ID) {
		return ErrNotFound
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if c.ID == "" {
		c.ID = uuid.NewString()
		c.Revision = 1
		_, e = tx.Exec(ctx, `INSERT INTO storage_connections(id,owner,name,provider,endpoint,region,path_style,read_only,access_key,secret_key,session_token,conditional_writes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, c.ID, c.Owner, c.Name, c.Provider, c.Endpoint, c.Region, c.PathStyle, c.ReadOnly, c.AccessKey, c.SecretKey, c.SessionToken, c.ConditionalWrites)
	} else {
		old, e := scanConnection(tx.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE owner=$1 AND id=$2 FOR UPDATE`, c.Owner, c.ID))
		if e != nil {
			return e
		}
		if old.Revision != c.Revision {
			return ErrConflict
		}
		var attached bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notebooks WHERE owner=$1 AND connection_id=$2)`, c.Owner, c.ID).Scan(&attached); e != nil {
			return e
		}
		if attached && (old.Provider != c.Provider || old.Endpoint != c.Endpoint || old.Region != c.Region || old.PathStyle != c.PathStyle) {
			return ErrConflict
		}
		if old.ConditionalWrites != c.ConditionalWrites {
			// Switching validator schemes requires editors to reload. Never reuse
			// an earlier compatibility-mode revision after a strict-mode write.
			if _, e = tx.Exec(ctx, `DELETE FROM object_write_versions WHERE owner=$1 AND scope IN (SELECT id FROM notebooks WHERE owner=$1 AND connection_id=$2)`, c.Owner, c.ID); e != nil {
				return e
			}
		}
		// Blank secret inputs retain existing values; clearing a session token is explicit.
		if c.AccessKey == "" || c.SecretKey == "" {
			var a, s string
			if e = tx.QueryRow(ctx, `SELECT access_key,secret_key FROM storage_connections WHERE owner=$1 AND id=$2`, c.Owner, c.ID).Scan(&a, &s); e != nil {
				return e
			}
			if c.AccessKey == "" {
				c.AccessKey = a
			}
			if c.SecretKey == "" {
				c.SecretKey = s
			}
		}
		_, e = tx.Exec(ctx, `UPDATE storage_connections SET name=$3,provider=$4,endpoint=$5,region=$6,path_style=$7,read_only=$8,access_key=$9,secret_key=$10,session_token=CASE WHEN $12 THEN session_token ELSE $11 END,conditional_writes=$13,revision=revision+1 WHERE owner=$1 AND id=$2`, c.Owner, c.ID, c.Name, c.Provider, c.Endpoint, c.Region, c.PathStyle, c.ReadOnly, c.AccessKey, c.SecretKey, c.SessionToken, c.RetainSession, c.ConditionalWrites)
		c.Revision++
	}
	if e != nil {
		return translate(e)
	}
	return translate(tx.Commit(ctx))
}
func (r *Repository) SaveNotebook(ctx context.Context, n *Notebook, secret Secrets) error {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if n.ID != "" {
		result, e := tx.Exec(ctx, `UPDATE notebooks SET name=$3,read_only=$4,revision=revision+1 WHERE owner=$1 AND id=$2 AND revision=$5`, n.Owner, n.ID, n.Name, n.ReadOnly, n.Revision)
		if e != nil {
			return translate(e)
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		n.Revision++
		return tx.Commit(ctx)
	}
	if !validID(n.ConnectionID) || !validID(n.Owner) {
		return ErrNotFound
	}
	c, e := scanConnection(tx.QueryRow(ctx, `SELECT `+connCols+` FROM storage_connections WHERE owner=$1 AND id=$2 FOR SHARE`, n.Owner, n.ConnectionID))
	if e != nil {
		return e
	}
	if n.ConnectionRevision != 0 && n.ConnectionRevision != c.Revision {
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,2))`, n.ConnectionID+":"+n.Bucket); e != nil {
		return e
	}
	var overlap bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notebooks WHERE owner=$1 AND connection_id=$2 AND bucket=$3 AND (prefix=$4 OR prefix='' OR $4='' OR starts_with(prefix,$4||'/') OR starts_with($4,prefix||'/')))`, n.Owner, n.ConnectionID, n.Bucket, n.Prefix).Scan(&overlap)
	if e != nil {
		return e
	}
	if overlap {
		return ErrConflict
	}
	n.ID = uuid.NewString()
	n.Revision = 1
	if n.Encrypted {
		n.SecretID = uuid.NewString()
		recovery := Recovery{Provider: c.Provider, PathStyle: c.PathStyle, ID: n.SecretID, Name: n.Name, Bucket: n.Bucket, Prefix: n.Prefix, Endpoint: c.Endpoint, Region: c.Region, FilenameEncryption: n.FilenameEncryption, FilenameEncoding: n.FilenameEncoding, DirectoryEncryption: n.DirectoryEncryption, Suffix: n.Suffix, Password: secret.Password, Password2: secret.Password2}
		data, e := json.Marshal(recovery)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO storage_secrets(id,owner,password,password2,recovery) VALUES($1,$2,$3,$4,$5)`, n.SecretID, n.Owner, secret.Password, secret.Password2, data); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO notebooks(id,owner,connection_id,name,bucket,prefix,read_only,encrypted,directory_encryption,filename_encryption,filename_encoding,suffix,secret_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid)`, n.ID, n.Owner, n.ConnectionID, n.Name, n.Bucket, n.Prefix, n.ReadOnly, n.Encrypted, n.DirectoryEncryption, n.FilenameEncryption, n.FilenameEncoding, n.Suffix, n.SecretID)
	if e != nil {
		return translate(e)
	}
	return translate(tx.Commit(ctx))
}
func (r *Repository) RemoveNotebook(ctx context.Context, owner, id string, revision int64) error {
	if !validID(id) {
		return ErrNotFound
	}
	res, e := r.Pool.Exec(ctx, `DELETE FROM notebooks WHERE owner=$1 AND id=$2 AND revision=$3`, owner, id, revision)
	if e != nil {
		return translate(e)
	}
	if res.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (r *Repository) RemoveConnection(ctx context.Context, owner, id string, revision int64) error {
	if !validID(id) {
		return ErrNotFound
	}
	res, e := r.Pool.Exec(ctx, `DELETE FROM storage_connections WHERE owner=$1 AND id=$2 AND revision=$3`, owner, id, revision)
	if e != nil {
		return translate(e)
	}
	if res.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

type RecoveryInfo struct{ ID, Name string }

func (r *Repository) Recoveries(ctx context.Context, owner string) ([]RecoveryInfo, error) {
	rows, e := r.Pool.Query(ctx, `SELECT id::text,recovery->>'name' FROM storage_secrets WHERE owner=$1 ORDER BY created_at`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []RecoveryInfo{}
	for rows.Next() {
		var v RecoveryInfo
		if e = rows.Scan(&v.ID, &v.Name); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) Recovery(ctx context.Context, owner, id string) ([]byte, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	var b []byte
	e := r.Pool.QueryRow(ctx, `SELECT recovery FROM storage_secrets WHERE owner=$1 AND id=$2`, owner, id).Scan(&b)
	return b, translate(e)
}
func ValidName(s string) bool {
	return strings.TrimSpace(s) == s && len(s) > 0 && len(s) <= 120 && !strings.ContainsAny(s, "\x00\r\n")
}

func (r *Repository) Credentials(ctx context.Context, owner, id string) (Connection, error) {
	c, e := r.Connection(ctx, owner, id)
	if e != nil {
		return c, e
	}
	e = r.Pool.QueryRow(ctx, `SELECT access_key,secret_key,session_token FROM storage_connections WHERE owner=$1 AND id=$2`, owner, id).Scan(&c.AccessKey, &c.SecretKey, &c.SessionToken)
	return c, translate(e)
}

type transactionKey struct{}

// WithTransaction lets storage checks share the caller's index/write transaction
// rather than acquiring a second pool connection while holding an object lock.
func WithTransaction(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, transactionKey{}, tx)
}
func Transaction(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value(transactionKey{}).(pgx.Tx)
	return tx
}
