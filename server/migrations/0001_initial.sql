-- Initial Voxa server schema: workspaces, meetings, and workspace configuration.
-- Identifiers are application-generated, protocol-safe strings. "C" collation
-- keeps ordering byte-wise and identical to the in-memory adapter.

CREATE TABLE workspaces (
    id         text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE meetings (
    workspace_id text COLLATE "C" NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    id           text COLLATE "C" NOT NULL CHECK (char_length(id) BETWEEN 1 AND 128),
    title        text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    status       text NOT NULL CHECK (status IN ('created')),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, id)
);

-- Supports keyset pagination ordered by (created_at, id) within a workspace.
CREATE INDEX meetings_workspace_created_id_idx ON meetings (workspace_id, created_at, id);

-- One row per workspace. A missing row means "server defaults", which are
-- owned by application code rather than duplicated as SQL defaults.
-- Credentials are intentionally not stored here.
CREATE TABLE workspace_configuration (
    workspace_id                       text COLLATE "C" PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    transcription_provider             text NOT NULL,
    transcription_model                text NOT NULL,
    transcription_language             text NOT NULL,
    transcription_translate_to_english boolean NOT NULL,
    summary_provider                   text NOT NULL,
    summary_model                      text NOT NULL,
    summary_output_language            text NOT NULL,
    summary_template_id                text,
    summary_auto_generate              boolean NOT NULL,
    recording_retain_media             boolean NOT NULL,
    privacy_analytics_enabled          boolean NOT NULL,
    notifications_processing_complete  boolean NOT NULL,
    updated_at                         timestamptz NOT NULL DEFAULT now()
);
