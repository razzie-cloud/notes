-- +goose Up
-- Settings are durable, like Files; only the content index is disposable.
CREATE TABLE storage_connections (
 id uuid PRIMARY KEY,
 owner uuid NOT NULL,
 name text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('AWS','Backblaze','Other')),
 endpoint text NOT NULL DEFAULT '',
 region text NOT NULL DEFAULT 'us-east-1',
 path_style boolean NOT NULL DEFAULT true,
 read_only boolean NOT NULL DEFAULT false,
 access_key text NOT NULL,
 secret_key text NOT NULL,
 session_token text NOT NULL DEFAULT '',
 revision bigint NOT NULL DEFAULT 1,
 UNIQUE(id,owner), UNIQUE(owner,name)
);
CREATE TABLE storage_secrets (
 id uuid PRIMARY KEY,
 owner uuid NOT NULL,
 password text NOT NULL,
 password2 text NOT NULL DEFAULT '',
 recovery jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(id,owner)
);
CREATE TABLE notebooks (
 id text PRIMARY KEY,
 owner uuid NOT NULL,
 connection_id uuid NOT NULL,
 name text NOT NULL,
 bucket text NOT NULL,
 prefix text NOT NULL DEFAULT '',
 read_only boolean NOT NULL DEFAULT false,
 encrypted boolean NOT NULL DEFAULT false,
 filename_encryption text NOT NULL DEFAULT 'standard',
 directory_encryption boolean NOT NULL DEFAULT true,
 filename_encoding text NOT NULL DEFAULT 'base32',
 suffix text NOT NULL DEFAULT '.bin',
 secret_id uuid,
 revision bigint NOT NULL DEFAULT 1,
 UNIQUE(id,owner), UNIQUE(owner,name),
 FOREIGN KEY(connection_id,owner) REFERENCES storage_connections(id,owner),
 FOREIGN KEY(secret_id,owner) REFERENCES storage_secrets(id,owner),
 CHECK(encrypted = (secret_id IS NOT NULL))
);
-- The previous operator-bucket implementation stored only derived state. Drop
-- that index, not any S3 data. Reattach its old prefix in the UI to rebuild it.
DELETE FROM objects;
DELETE FROM scan_seen;
DELETE FROM index_scans;
ALTER TABLE objects ADD CONSTRAINT objects_notebook_owner_fk FOREIGN KEY(scope,owner) REFERENCES notebooks(id,owner) ON DELETE CASCADE;
ALTER TABLE index_scans ADD CONSTRAINT scans_notebook_fk FOREIGN KEY(scope) REFERENCES notebooks(id) ON DELETE CASCADE;
ALTER TABLE scan_seen ADD CONSTRAINT seen_notebook_fk FOREIGN KEY(scope) REFERENCES notebooks(id) ON DELETE CASCADE;
CREATE INDEX notebooks_scheduling ON notebooks(id);

-- +goose Down
ALTER TABLE scan_seen DROP CONSTRAINT seen_notebook_fk;
ALTER TABLE index_scans DROP CONSTRAINT scans_notebook_fk;
ALTER TABLE objects DROP CONSTRAINT objects_notebook_owner_fk;
DROP TABLE notebooks;
DROP TABLE storage_secrets;
DROP TABLE storage_connections;
