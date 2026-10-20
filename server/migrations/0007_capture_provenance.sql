-- Capture provenance on recording sessions: how a recording was captured
-- (operational diagnostics, not tracking). Existing sessions predate it and
-- become 'unknown'; no media is rewritten.
--   capture_mode    microphone | shared_audio | microphone_and_shared_audio | unknown
--   capture_client  web | desktop | extension | android | unknown
--   shared_surface  browser | window | monitor | unknown, when shared audio was captured
-- No user agent, URL, tab title or meeting-provider data is stored.

ALTER TABLE recording_sessions
    ADD COLUMN capture_mode text NOT NULL DEFAULT 'unknown'
        CHECK (capture_mode IN ('microphone', 'shared_audio', 'microphone_and_shared_audio', 'unknown')),
    ADD COLUMN capture_client text NOT NULL DEFAULT 'unknown'
        CHECK (capture_client IN ('web', 'desktop', 'extension', 'android', 'unknown')),
    ADD COLUMN shared_surface text
        CHECK (shared_surface IS NULL OR shared_surface IN ('browser', 'window', 'monitor', 'unknown'));
