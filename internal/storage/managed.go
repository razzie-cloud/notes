package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/db"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/rclone/rclone/backend/crypt"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
)

var ErrCrypto = errors.New("cannot decrypt storage; check crypt settings")
var quietRclone sync.Once
var initializeCryptError error

func Cipher(n store.Notebook, secret store.Secrets) (*crypt.Cipher, error) {
	if !n.Encrypted {
		return nil, nil
	}
	quietRclone.Do(func() {
		fs.SetLogger(slog.NewTextHandler(io.Discard, nil))
		// rclone's obscure helper initializes a package-global cipher lazily.
		// Initialize it once before concurrent workers can call Obscure/Reveal.
		_, initializeCryptError = obscure.Obscure("notes initialization")
	})
	if initializeCryptError != nil {
		return nil, ErrCrypto
	}
	password, e := obscure.Obscure(secret.Password)
	if e != nil || secret.Password == "" {
		return nil, ErrCrypto
	}
	salt := ""
	if secret.Password2 != "" {
		salt, e = obscure.Obscure(secret.Password2)
		if e != nil {
			return nil, ErrCrypto
		}
	}
	cipher, e := crypt.NewCipher(configmap.Simple{"password": password, "password2": salt, "filename_encryption": n.FilenameEncryption, "filename_encoding": n.FilenameEncoding, "directory_name_encryption": strconv.FormatBool(n.DirectoryEncryption), "suffix": n.Suffix, "pass_bad_blocks": "false"})
	if e != nil {
		return nil, ErrCrypto
	}
	return cipher, nil
}

// Mapped applies the notebook's physical prefix and rclone's exported crypt
// implementation. ETags remain the underlying S3 validators for conditional I/O.
// Logical keys include the owner; physical notebook roots do not require UUIDs.
type Mapped struct {
	Raw           Store
	Owner, Prefix string
	Cipher        *crypt.Cipher
}

func (m *Mapped) physical(key string) (string, error) {
	root := m.Owner + "/"
	if !strings.HasPrefix(key, root) {
		return "", ErrPath
	}
	relative := strings.TrimPrefix(key, root)
	if !Path(relative) {
		return "", ErrPath
	}
	if m.Cipher != nil {
		relative = m.Cipher.EncryptFileName(relative)
	}
	physical := m.Prefix + relative
	if len(physical) > 1024 {
		return "", ErrPath
	}
	return physical, nil
}
func (m *Mapped) logical(meta Meta) (Meta, error) {
	if !strings.HasPrefix(meta.Key, m.Prefix) {
		return Meta{}, ErrPath
	}
	relative := strings.TrimPrefix(meta.Key, m.Prefix)
	if m.Cipher != nil {
		var e error
		relative, e = m.Cipher.DecryptFileName(relative)
		if e != nil {
			return Meta{}, ErrCrypto
		}
		meta.Size, e = m.Cipher.DecryptedSize(meta.Size)
		if e != nil {
			return Meta{}, ErrCrypto
		}
	}
	if !Path(relative) {
		return Meta{}, ErrPath
	}
	meta.Key = m.Owner + "/" + relative
	return meta, nil
}
func (m *Mapped) List(ctx context.Context, _ string, cursor string) (Page, error) {
	p, e := m.Raw.List(ctx, m.Prefix, cursor)
	if e != nil {
		return Page{}, e
	}
	out := Page{Next: p.Next, Objects: make([]Meta, 0, len(p.Objects))}
	for _, o := range p.Objects {
		if strings.HasSuffix(o.Key, "/") {
			continue
		}
		logical, e := m.logical(o)
		if e != nil {
			return Page{}, e
		}
		out.Objects = append(out.Objects, logical)
	}
	return out, nil
}
func (m *Mapped) Head(ctx context.Context, key string) (Meta, error) {
	physical, e := m.physical(key)
	if e != nil {
		return Meta{}, e
	}
	meta, e := m.Raw.Head(ctx, physical)
	if e != nil {
		return Meta{}, e
	}
	return m.logical(meta)
}
func (m *Mapped) Get(ctx context.Context, key, etag string, limit int64) ([]byte, Meta, error) {
	physical, e := m.physical(key)
	if e != nil {
		return nil, Meta{}, e
	}
	rawLimit := limit
	if m.Cipher != nil {
		rawLimit = m.Cipher.EncryptedSize(limit)
	}
	b, meta, e := m.Raw.Get(ctx, physical, etag, rawLimit)
	if e != nil {
		return nil, Meta{}, e
	}
	meta, e = m.logical(meta)
	if e != nil {
		return nil, Meta{}, e
	}
	if m.Cipher != nil {
		reader, e := m.Cipher.DecryptData(io.NopCloser(bytes.NewReader(b)))
		if e != nil {
			return nil, Meta{}, ErrCrypto
		}
		defer reader.Close()
		b, e = io.ReadAll(io.LimitReader(reader, limit+1))
		if e != nil {
			return nil, Meta{}, ErrCrypto
		}
		if int64(len(b)) > limit {
			return nil, Meta{}, ErrTooLarge
		}
	}
	return b, meta, nil
}
func (m *Mapped) Put(ctx context.Context, key string, b []byte, etag, ct string) (Meta, error) {
	physical, e := m.physical(key)
	if e != nil {
		return Meta{}, e
	}
	plainSize := int64(len(b))
	if m.Cipher != nil {
		reader, e := m.Cipher.EncryptData(bytes.NewReader(b))
		if e != nil {
			return Meta{}, ErrCrypto
		}
		b, e = io.ReadAll(reader)
		if e != nil {
			return Meta{}, ErrCrypto
		}
		ct = "application/octet-stream"
	}
	meta, e := m.Raw.Put(ctx, physical, b, etag, ct)
	if e != nil {
		return Meta{}, e
	}
	meta.Key = key
	meta.Size = plainSize
	return meta, nil
}
func (m *Mapped) Delete(ctx context.Context, key, etag string) error {
	physical, e := m.physical(key)
	if e != nil {
		return e
	}
	return m.Raw.Delete(ctx, physical, etag)
}

type Registry struct {
	Repo   *store.Repository
	Config config.Config
}
type Managed struct {
	Registry  *Registry
	Owner, ID string
}

func (r *Registry) Open(ctx context.Context, owner, id string) (Store, store.Notebook, error) {
	n, e := r.Repo.Notebook(ctx, owner, id)
	if e != nil {
		return nil, n, e
	}
	return &Managed{Registry: r, Owner: owner, ID: id}, n, nil
}
func (r *Registry) Direct(c store.Connection, n store.Notebook, secret store.Secrets) (*Mapped, func(), error) {
	raw, e := New(S3Config{Endpoint: c.Endpoint, Region: c.Region, Bucket: n.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey, SessionToken: c.SessionToken, PrivateEndpoints: r.Config.PrivateEndpoints, AllowHTTP: r.Config.AllowHTTP, PathStyle: c.PathStyle, DisableConditionalWrites: !c.ConditionalWrites})
	if e != nil {
		return nil, nil, e
	}
	cipher, e := Cipher(n, secret)
	if e != nil {
		return nil, nil, e
	}
	prefix := n.Prefix
	if prefix != "" {
		prefix += "/"
	}
	close := func() {
		if closer, ok := raw.client.Options().HTTPClient.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
	return &Mapped{Raw: raw, Owner: n.Owner, Prefix: prefix, Cipher: cipher}, close, nil
}
func (m *Managed) call(ctx context.Context, write bool, key string, fn func(Store, store.Connection, pgx.Tx) error) error {
	tx := store.Transaction(ctx)
	owned := tx == nil
	if tx == nil {
		var e error
		tx, e = m.Registry.Repo.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx.Rollback(cleanup)
		}()
	}
	if key != "" {
		if err := db.LockObject(ctx, tx, m.ID, key); err != nil {
			return err
		}
	}
	n, c, secrets, e := m.Registry.Repo.Locked(ctx, tx, m.Owner, m.ID)
	if e != nil {
		return e
	}
	if write && !n.Writable(c) {
		return store.ErrReadOnly
	}
	mapped, close, e := m.Registry.Direct(c, n, secrets)
	if e != nil {
		return e
	}
	defer close()
	if e = fn(mapped, c, tx); e != nil {
		return e
	}
	if owned {
		if e = tx.Commit(ctx); e != nil && write {
			// The S3 mutation succeeded. A failed commit may invalidate its
			// returned write token; a fresh read obtains the current revision.
			slog.Warn("Storage saved; reload to refresh write revision", "event", "notes.write_revision_commit_failed", "error_kind", "dependency")
			return nil
		}
	}
	return e
}
func (m *Managed) List(ctx context.Context, prefix, cursor string) (out Page, e error) {
	e = m.call(ctx, false, "", func(s Store, _ store.Connection, _ pgx.Tx) error {
		var e error
		out, e = s.List(ctx, prefix, cursor)
		return e
	})
	return
}
func (m *Managed) Head(ctx context.Context, key string) (out Meta, e error) {
	e = m.call(ctx, false, key, func(s Store, c store.Connection, tx pgx.Tx) error {
		var err error
		out, err = s.Head(ctx, key)
		if err == nil && !c.ConditionalWrites {
			out.WriteETag, err = m.observe(ctx, tx, key, out, true)
		}
		return err
	})
	return
}
func (m *Managed) Get(ctx context.Context, key, etag string, limit int64) (b []byte, out Meta, e error) {
	e = m.call(ctx, false, key, func(s Store, c store.Connection, tx pgx.Tx) error {
		var err error
		b, out, err = s.Get(ctx, key, etag, limit)
		if err == nil && !c.ConditionalWrites && etag == "" {
			current, err := s.Head(ctx, key)
			if err != nil {
				return err
			}
			if current.ETag != out.ETag || current.Size != out.Size || out.Version != "" && out.Version != current.Version {
				return ErrConflict
			}
			out.WriteETag, err = m.observe(ctx, tx, key, current, true)
			return err
		}
		return err
	})
	return
}
func (m *Managed) Put(ctx context.Context, key string, b []byte, etag, ct string) (out Meta, e error) {
	e = m.call(ctx, true, key, func(s Store, c store.Connection, tx pgx.Tx) error {
		var err error
		if c.ConditionalWrites {
			out, err = s.Put(ctx, key, b, etag, ct)
			return err
		}
		underlying, revision, err := m.claim(ctx, tx, s, key, etag)
		if err != nil {
			return err
		}
		out, err = s.Put(ctx, key, b, underlying, ct)
		if err != nil {
			return err
		}
		out.WriteETag = databaseValidator(revision)
		m.record(ctx, tx, key, revision, out, true)
		return nil
	})
	return
}
func (m *Managed) Delete(ctx context.Context, key, etag string) error {
	return m.call(ctx, true, key, func(s Store, c store.Connection, tx pgx.Tx) error {
		if c.ConditionalWrites {
			return s.Delete(ctx, key, etag)
		}
		if etag == "*" || etag == "" {
			return ErrConflict
		}
		underlying, revision, err := m.claim(ctx, tx, s, key, etag)
		if err != nil {
			return err
		}
		if err = s.Delete(ctx, key, underlying); err != nil {
			return err
		}
		m.record(ctx, tx, key, revision, Meta{}, false)
		return nil
	})
}
