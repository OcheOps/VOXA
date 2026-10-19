-- Authentication and workspace membership.
--
-- users: one row per external OIDC identity. (issuer, subject) is the stable
-- identity key; email and display_name are mutable profile hints and are
-- never used for identity or authorization. id is an opaque Voxa ID.
--
-- workspace_memberships: which users may act in which workspace, with a
-- deliberately small role set. Deleting a user or workspace removes only its
-- membership rows; it never deletes workspace data through a user.
--
-- deployment_state: single-row marker written by first-user bootstrap. Its
-- boolean primary key with CHECK (singleton) makes a second bootstrap
-- impossible even if application-level locking failed.

ALTER TABLE workspaces
    ADD COLUMN name text NOT NULL DEFAULT 'Default workspace' CHECK (char_length(name) BETWEEN 1 AND 100);

CREATE TABLE users (
    id           text COLLATE "C" PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
    issuer       text COLLATE "C" NOT NULL CHECK (char_length(issuer) BETWEEN 1 AND 2048),
    subject      text COLLATE "C" NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
    email        text CHECK (email IS NULL OR char_length(email) BETWEEN 1 AND 320),
    display_name text CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 200),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    CONSTRAINT users_external_identity_key UNIQUE (issuer, subject)
);

CREATE TABLE workspace_memberships (
    workspace_id text COLLATE "C" NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      text COLLATE "C" NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('owner', 'member')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

-- Membership lookup by user on every authenticated request.
CREATE INDEX workspace_memberships_user_idx ON workspace_memberships (user_id, workspace_id);

-- Historical record of the bootstrap; deliberately no foreign keys so the
-- record never blocks or cascades user/workspace deletion.
CREATE TABLE deployment_state (
    singleton              boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    initialized_at         timestamptz NOT NULL DEFAULT now(),
    bootstrap_user_id      text NOT NULL,
    bootstrap_workspace_id text NOT NULL
);
