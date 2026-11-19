-- +goose Up
-- Stop all Notes HTTP processes and workers before either direction; reload
-- compatibility-mode editors afterward. Only write-coordination tokens expire.
DELETE FROM object_write_versions;
ALTER TABLE object_write_versions DROP CONSTRAINT object_write_versions_scope_owner_fkey;
ALTER TABLE object_write_versions ALTER COLUMN scope TYPE uuid USING scope::uuid;
ALTER TABLE object_write_versions ADD CONSTRAINT object_write_versions_scope_owner_fkey
 FOREIGN KEY(scope,owner) REFERENCES storage_connections(id,owner) ON DELETE CASCADE;

-- +goose Down
DELETE FROM object_write_versions;
ALTER TABLE object_write_versions DROP CONSTRAINT object_write_versions_scope_owner_fkey;
ALTER TABLE object_write_versions ALTER COLUMN scope TYPE text USING scope::text;
ALTER TABLE object_write_versions ADD CONSTRAINT object_write_versions_scope_owner_fkey
 FOREIGN KEY(scope,owner) REFERENCES notebooks(id,owner) ON DELETE CASCADE;
