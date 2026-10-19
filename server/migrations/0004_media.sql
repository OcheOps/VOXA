-- Recording sessions and media assets for server-side media storage.
--
-- recording_sessions: one logical capture of a meeting (not a device or
-- browser recorder). Pausing is a client concern and is not stored.
--
-- media_assets: metadata for stored media. Bytes live in the media store;
-- storage_key is an internal, opaque locator (never a client-visible path
-- and never the asset identity). Status: uploading -> ready | failed.
--
-- Both reference meetings by (workspace_id, meeting_id) so an asset can
-- never point at another workspace's meeting. Deleting a meeting removes
-- these rows; orphaned media bytes are removed by the server's cleanup.

CREATE TABLE recording_sessions (
    id           text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    workspace_id text COLLATE "C" NOT NULL,
    meeting_id   text COLLATE "C" NOT NULL,
    status       text NOT NULL CHECK (status IN ('recording', 'completed', 'failed')),
    started_at   timestamptz NOT NULL,
    ended_at     timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, meeting_id) REFERENCES meetings (workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE media_assets (
    id                   text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    workspace_id         text COLLATE "C" NOT NULL,
    meeting_id           text COLLATE "C" NOT NULL,
    recording_session_id text COLLATE "C" REFERENCES recording_sessions (id) ON DELETE SET NULL,
    kind                 text NOT NULL CHECK (kind IN ('audio')),
    mime_type            text NOT NULL CHECK (char_length(mime_type) BETWEEN 1 AND 100),
    status               text NOT NULL CHECK (status IN ('uploading', 'ready', 'failed')),
    size_bytes           bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    duration_seconds     double precision CHECK (duration_seconds IS NULL OR duration_seconds >= 0),
    chunk_count          integer CHECK (chunk_count IS NULL OR chunk_count >= 1),
    storage_key          text COLLATE "C" NOT NULL UNIQUE,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, meeting_id) REFERENCES meetings (workspace_id, id) ON DELETE CASCADE,
    CHECK (status <> 'ready' OR chunk_count IS NOT NULL)
);

CREATE INDEX media_assets_meeting_idx ON media_assets (workspace_id, meeting_id, created_at, id);
CREATE INDEX media_assets_stale_uploads_idx ON media_assets (updated_at) WHERE status = 'uploading';
