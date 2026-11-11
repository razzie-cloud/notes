package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
	"github.com/rclone/rclone/backend/crypt"
	rclones3 "github.com/rclone/rclone/backend/s3"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/object"
)

func TestManagedStorageIsolationEncryptionAndRecovery(t *testing.T) {
	pool := testutil.Database(t)
	f, cfg := testutil.ObjectStore(t)
	ctx := context.Background()
	registry, plain := testutil.Attach(t, pool, f, cfg, testutil.Owner, "plain", false, "")
	_, encrypted := testutil.Attach(t, pool, f, cfg, testutil.Owner, "encrypted", true, "correct secret")
	_, foreign := testutil.Attach(t, pool, f, cfg, testutil.Other, "foreign", false, "")
	if _, _, e := registry.Open(ctx, testutil.Other, plain.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("foreign notebook exposed", e)
	}
	key := testutil.Owner + "/notes/a.md"
	p, _, e := registry.Open(ctx, testutil.Owner, plain.ID)
	if e != nil {
		t.Fatal(e)
	}
	s, _, e := registry.Open(ctx, testutil.Owner, encrypted.ID)
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.Put(ctx, key, []byte("# Encrypted árvíztűrő"), "*", "text/markdown")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Put(ctx, key, []byte("# Plain"), "*", "text/markdown"); e != nil {
		t.Fatal(e)
	}
	raw, e := f.Raw.List(ctx, "encrypted/", "")
	if e != nil || len(raw.Objects) != 1 {
		t.Fatal(raw, e)
	}
	if strings.Contains(raw.Objects[0].Key, "a.md") {
		t.Fatal("encrypted filename leaked", raw.Objects[0].Key)
	}
	cipher, _, e := f.Raw.Get(ctx, raw.Objects[0].Key, "", 4096)
	if e != nil || strings.Contains(string(cipher), "árvíztűrő") || !strings.HasPrefix(string(cipher), "RCLONE") {
		t.Fatal("not rclone ciphertext", e)
	}
	b, meta, e := s.Get(ctx, key, first.ETag, 4096)
	if e != nil || string(b) != "# Encrypted árvíztűrő" || meta.Size != int64(len(b)) {
		t.Fatal(string(b), meta, e)
	}
	if _, e = s.Put(ctx, key, []byte("changed"), first.ETag, "text/markdown"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Put(ctx, key, []byte("stale"), first.ETag, "text/markdown"); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, key, first.ETag); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	if _, e = s.Put(ctx, testutil.Other+"/notes/a.md", nil, "*", "text/markdown"); !errors.Is(e, storage.ErrPath) {
		t.Fatal("foreign logical key accepted", e)
	}
	// Changes to a connection policy apply to existing handles immediately.
	connection, e := registry.Repo.Connection(ctx, testutil.Owner, plain.ConnectionID)
	if e != nil {
		t.Fatal(e)
	}
	connection.ReadOnly = true
	connection.RetainSession = true
	if e = registry.Repo.SaveConnection(ctx, &connection); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Put(ctx, key, []byte("forbidden"), "*", "text/markdown"); !errors.Is(e, store.ErrReadOnly) {
		t.Fatal(e)
	}
	if e = p.Delete(ctx, key, first.ETag); !errors.Is(e, store.ErrReadOnly) {
		t.Fatal(e)
	}
	if b, _, e = p.Get(ctx, key, "", 4096); e != nil || string(b) != "# Plain" {
		t.Fatal(string(b), e)
	}
	if _, e = registry.Repo.Recovery(ctx, testutil.Other, encrypted.SecretID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal(e)
	}
	if e = registry.Repo.RemoveNotebook(ctx, testutil.Owner, encrypted.ID, encrypted.Revision); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Get(ctx, key, "", 4096); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("deleted mapping still usable", e)
	}
	if recovery, e := registry.Repo.Recovery(ctx, testutil.Owner, encrypted.SecretID); e != nil || !strings.Contains(string(recovery), "correct secret") {
		t.Fatal("recovery lost", e)
	}
	if page, e := f.Raw.List(ctx, "encrypted/", ""); e != nil || len(page.Objects) != 1 {
		t.Fatal("S3 content deleted", page, e)
	}
	if _, _, e = registry.Open(ctx, testutil.Owner, foreign.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestRcloneFilesystemCompatibility(t *testing.T) {
	pool := testutil.Database(t)
	f, cfg := testutil.ObjectStore(t)
	ctx := context.Background()
	for _, mode := range []string{"standard", "off"} {
		t.Run(mode, func(t *testing.T) {
			registry, n := testutil.Attach(t, pool, f, cfg, testutil.Owner, "compat-"+mode, true, "compatible password")
			// These options are immutable after attach; prepare the fixture's alternative
			// format directly, then use the independent rclone filesystem API to verify.
			if _, e := pool.Exec(ctx, `UPDATE notebooks SET filename_encryption=$2,filename_encoding='base64' WHERE id=$1`, n.ID, mode); e != nil {
				t.Fatal(e)
			}
			n.FilenameEncryption = mode
			n.FilenameEncoding = "base64"
			s, _, e := registry.Open(ctx, testutil.Owner, n.ID)
			if e != nil {
				t.Fatal(e)
			}
			key := testutil.Owner + "/notes/from-notes.md"
			if _, e = s.Put(ctx, key, []byte("# Notes to rclone"), "*", "text/markdown"); e != nil {
				t.Fatal(e)
			}
			underlying, e := rclones3.NewFs(ctx, "compat-"+mode, "test/"+n.Prefix+"/", defaults(t, "s3", configmap.Simple{"provider": "Other", "endpoint": f.Server.URL, "access_key_id": "test", "secret_access_key": "test", "region": "us-east-1", "force_path_style": "true", "no_check_bucket": "true"}))
			if e != nil {
				t.Fatal(e)
			}
			alias := "notes-test-" + n.ID + ":" + underlying.Root()
			cache.Put(alias, underlying)
			cache.Pin(underlying)
			defer cache.Unpin(underlying)
			defer cache.ClearConfig("notes-test-" + n.ID)
			password, e := obscure.Obscure("compatible password")
			if e != nil {
				t.Fatal(e)
			}
			cryptFS, e := crypt.NewFs(ctx, "notes-test-"+n.ID+"-crypt", "", configmap.Simple{"remote": alias, "password": password, "filename_encryption": mode, "filename_encoding": "base64", "directory_name_encryption": "true", "suffix": ".bin", "no_data_encryption": "false", "pass_bad_blocks": "false", "strict_names": "true"})
			if e != nil {
				t.Fatal(e)
			}
			remote, e := cryptFS.NewObject(ctx, "notes/from-notes.md")
			if e != nil {
				t.Fatal(e)
			}
			in, e := remote.Open(ctx)
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(in)
			in.Close()
			if e != nil || string(b) != "# Notes to rclone" {
				t.Fatal(string(b), e)
			}
			text := "# Rclone to Notes"
			info := object.NewStaticObjectInfo("notes/from-rclone.md", time.Now(), int64(len(text)), true, nil, cryptFS)
			if _, e = cryptFS.Put(ctx, strings.NewReader(text), info); e != nil {
				t.Fatal(e)
			}
			b, _, e = s.Get(ctx, testutil.Owner+"/notes/from-rclone.md", "", 4096)
			if e != nil || string(b) != text {
				t.Fatal(string(b), e)
			}
			page, e := s.List(ctx, "", "")
			if e != nil || len(page.Objects) != 2 {
				t.Fatal(page, e)
			}
		})
	}
}

func defaults(t *testing.T, backend string, overrides configmap.Simple) configmap.Simple {
	t.Helper()
	info, err := fs.Find(backend)
	if err != nil {
		t.Fatal(err)
	}
	values := configmap.Simple{}
	for _, option := range info.Options {
		values[option.Name] = fmt.Sprint(option.Default)
	}
	for key, value := range overrides {
		values[key] = value
	}
	return values
}
