-- +goose Up
ALTER TABLE storage_connections ADD COLUMN conditional_writes boolean NOT NULL DEFAULT true;
-- Independent of the asynchronous search index. Keep tombstones so stale
-- editors cannot reuse a validator after deletion/recreation.
CREATE TABLE object_write_versions (
 scope text NOT NULL,
 owner uuid NOT NULL,
 key text NOT NULL,
 revision uuid NOT NULL,
 etag text NOT NULL DEFAULT '',
 version_id text NOT NULL DEFAULT '',
 present boolean NOT NULL,
 PRIMARY KEY(scope,key),
 FOREIGN KEY(scope,owner) REFERENCES notebooks(id,owner) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE object_write_versions;
ALTER TABLE storage_connections DROP COLUMN conditional_writes;
