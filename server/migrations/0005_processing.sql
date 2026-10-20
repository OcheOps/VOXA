-- Asynchronous media processing: remux + transcription jobs and transcripts.
--
-- media_assets gains:
--   original_storage_key  the uploaded bytes, kept when processing replaces
--                         the playback object (provenance; never deleted by
--                         remux, protected from cleanup).
--   processed_at          set once processing produced (or gave up on) a
--                         playback object; NULL means "not processed yet".
--
-- processing_jobs is the PostgreSQL-backed queue. Workers claim with
-- SELECT ... FOR UPDATE SKIP LOCKED, hold a lease (lease_expires_at) renewed
-- by heartbeats, and use the attempt number as a fencing token. A running job
-- whose lease expired is reclaimable until max_attempts is reached.
-- error_code / error_message are user-safe; internal_error is operator-only.

ALTER TABLE media_assets
    ADD COLUMN original_storage_key text COLLATE "C" UNIQUE,
    ADD COLUMN processed_at timestamptz;

CREATE TABLE processing_jobs (
    id               text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    workspace_id     text COLLATE "C" NOT NULL,
    meeting_id       text COLLATE "C" NOT NULL,
    media_asset_id   text COLLATE "C" REFERENCES media_assets (id) ON DELETE CASCADE,
    type             text NOT NULL CHECK (type IN ('remux', 'transcription')),
    status           text NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    attempts         integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts     integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
    run_after        timestamptz NOT NULL,
    lease_expires_at timestamptz,
    progress         double precision CHECK (progress IS NULL OR progress BETWEEN 0 AND 1),
    error_code       text CHECK (error_code IS NULL OR char_length(error_code) <= 64),
    error_message    text CHECK (error_message IS NULL OR char_length(error_message) <= 500),
    internal_error   text CHECK (internal_error IS NULL OR char_length(internal_error) <= 4000),
    created_at       timestamptz NOT NULL,
    started_at       timestamptz,
    completed_at     timestamptz,
    updated_at       timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, meeting_id) REFERENCES meetings (workspace_id, id) ON DELETE CASCADE,
    CHECK (status <> 'running' OR lease_expires_at IS NOT NULL)
);

-- At most one active job of each type per media asset.
CREATE UNIQUE INDEX processing_jobs_one_active_idx ON processing_jobs (media_asset_id, type) WHERE status IN ('queued', 'running');
-- Claim scans.
CREATE INDEX processing_jobs_runnable_idx ON processing_jobs (run_after, created_at) WHERE status = 'queued';
CREATE INDEX processing_jobs_leases_idx ON processing_jobs (lease_expires_at) WHERE status = 'running';
CREATE INDEX processing_jobs_meeting_idx ON processing_jobs (workspace_id, meeting_id, created_at, id);

CREATE TABLE transcripts (
    id                text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    workspace_id      text COLLATE "C" NOT NULL,
    meeting_id        text COLLATE "C" NOT NULL,
    media_asset_id    text COLLATE "C" NOT NULL UNIQUE REFERENCES media_assets (id) ON DELETE CASCADE,
    processing_job_id text COLLATE "C" REFERENCES processing_jobs (id) ON DELETE SET NULL,
    language          text CHECK (language IS NULL OR char_length(language) <= 16),
    asr_engine        text NOT NULL CHECK (char_length(asr_engine) BETWEEN 1 AND 64),
    asr_model         text NOT NULL CHECK (char_length(asr_model) BETWEEN 1 AND 128),
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, meeting_id) REFERENCES meetings (workspace_id, id) ON DELETE CASCADE
);

-- Times are seconds relative to the start of the recording.
CREATE TABLE transcript_segments (
    transcript_id text COLLATE "C" NOT NULL REFERENCES transcripts (id) ON DELETE CASCADE,
    sequence      integer NOT NULL CHECK (sequence >= 0),
    start_seconds double precision NOT NULL CHECK (start_seconds >= 0),
    end_seconds   double precision NOT NULL,
    text          text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 10000),
    confidence    double precision CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
    PRIMARY KEY (transcript_id, sequence),
    CHECK (end_seconds >= start_seconds)
);
