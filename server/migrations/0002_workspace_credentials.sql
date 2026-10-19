-- Encrypted provider credentials, one row per (workspace, provider).
-- Only AES-256-GCM output is stored: there is no plaintext column and no
-- display mask. key_version names the key that sealed the row, so rows can
-- be decrypted independently and keys can be rotated without schema changes.
-- The ciphertext's AAD binds it to workspace_id, provider and key_version
-- (see docs/server-credential-security.md).

CREATE TABLE workspace_credentials (
    workspace_id text COLLATE "C" NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    provider     text COLLATE "C" NOT NULL CHECK (provider ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    key_version  text COLLATE "C" NOT NULL CHECK (key_version ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$'),
    nonce        bytea NOT NULL CHECK (octet_length(nonce) = 12),
    -- GCM output: encrypted secret (at least 1 byte) followed by the 16-byte tag.
    ciphertext   bytea NOT NULL CHECK (octet_length(ciphertext) > 16),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, provider)
);

-- Lets rotation tooling find rows still sealed with a retiring key.
CREATE INDEX workspace_credentials_key_version_idx ON workspace_credentials (key_version);
