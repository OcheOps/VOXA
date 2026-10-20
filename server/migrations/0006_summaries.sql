-- Server-side meeting summaries as a processing job type.
--
-- processing_jobs gains:
--   parameters  immutable snapshot taken at enqueue (summary: transcript ID,
--               provider, model, template, output language, user context).
--   checkpoint  resumable intermediate results written by the owning attempt
--               (summary chunk results), so a retry does not redo them.
-- Both are server-internal and never returned by the API.
--
-- summaries holds the current summary of a meeting. It is replaced only by a
-- job that is still running under its claimed attempt, in the same
-- transaction that checks ownership, so the previous summary stays visible
-- until a newer job succeeds. transcript_id records which transcript
-- produced it; it is deliberately not a foreign key so the provenance
-- survives retranscription (the summary is then reported as stale).

ALTER TABLE processing_jobs DROP CONSTRAINT processing_jobs_type_check;
ALTER TABLE processing_jobs
    ADD CONSTRAINT processing_jobs_type_check CHECK (type IN ('remux', 'transcription', 'summary')),
    ADD COLUMN parameters jsonb CHECK (parameters IS NULL OR (jsonb_typeof(parameters) = 'object' AND octet_length(parameters::text) <= 65536)),
    ADD COLUMN checkpoint jsonb CHECK (checkpoint IS NULL OR (jsonb_typeof(checkpoint) = 'object' AND octet_length(checkpoint::text) <= 8388608));

-- At most one active summary job per meeting.
CREATE UNIQUE INDEX processing_jobs_one_active_summary_idx ON processing_jobs (workspace_id, meeting_id)
    WHERE type = 'summary' AND status IN ('queued', 'running');

CREATE TABLE summaries (
    id                text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    workspace_id      text COLLATE "C" NOT NULL,
    meeting_id        text COLLATE "C" NOT NULL,
    transcript_id     text COLLATE "C" NOT NULL CHECK (char_length(transcript_id) BETWEEN 1 AND 128),
    media_asset_id    text COLLATE "C" REFERENCES media_assets (id) ON DELETE SET NULL,
    processing_job_id text COLLATE "C" REFERENCES processing_jobs (id) ON DELETE SET NULL,
    provider          text NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 64),
    model             text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 256),
    template_id       text NOT NULL CHECK (char_length(template_id) BETWEEN 1 AND 128),
    output_language   text CHECK (output_language IS NULL OR char_length(output_language) <= 16),
    content           jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object' AND octet_length(content::text) <= 1048576),
    usage             jsonb CHECK (usage IS NULL OR jsonb_typeof(usage) = 'object'),
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    UNIQUE (workspace_id, meeting_id),
    FOREIGN KEY (workspace_id, meeting_id) REFERENCES meetings (workspace_id, id) ON DELETE CASCADE
);
