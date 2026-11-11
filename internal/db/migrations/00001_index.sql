-- +goose Up
CREATE TABLE objects (
 scope text NOT NULL,
 owner uuid NOT NULL,
 key text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('notes','attachments')),
 relative_key text NOT NULL,
 note_key text NOT NULL,
 etag text NOT NULL,
 version_id text NOT NULL DEFAULT '',
 size bigint NOT NULL,
 modified timestamptz NOT NULL,
 content_type text NOT NULL DEFAULT '',
 stable_id text NOT NULL DEFAULT '',
 title text NOT NULL DEFAULT '',
 body text NOT NULL DEFAULT '',
 tags text[] NOT NULL DEFAULT '{}',
 created timestamptz,
 indexed_at timestamptz NOT NULL DEFAULT now(),
 index_error text NOT NULL DEFAULT '',
 search tsvector GENERATED ALWAYS AS (setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', left(body, 65536)), 'B')) STORED,
 PRIMARY KEY (scope,key)
);
CREATE INDEX objects_owner_listing ON objects (scope,owner,kind,modified DESC,key);
CREATE INDEX objects_search ON objects USING gin(search);
CREATE INDEX objects_tags ON objects USING gin(tags);
CREATE INDEX objects_attachments ON objects (scope,owner,note_key) WHERE kind='attachments';
CREATE TABLE index_scans (
 scope text PRIMARY KEY,
 id uuid NOT NULL,
 status text NOT NULL CHECK (status IN ('listing','reconciling','complete')),
 cursor text NOT NULL DEFAULT '',
 sweep_cursor text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 listed bigint NOT NULL DEFAULT 0,
 checked bigint NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT ''
);
CREATE TABLE scan_seen (
 scope text NOT NULL,
 scan_id uuid NOT NULL,
 key text NOT NULL,
 PRIMARY KEY (scope,scan_id,key)
);

-- +goose Down
DROP TABLE scan_seen;
DROP TABLE index_scans;
DROP TABLE objects;
