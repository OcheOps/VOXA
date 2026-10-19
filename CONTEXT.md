# Voxa Domain

Voxa records, stores, transcribes, and summarizes meeting media while keeping capture separate from server-owned records and processing.

## Language

**User**:
An authenticated human identity. A User may eventually participate in more than one Workspace.
_Avoid_: Account, profile

**Workspace**:
The ownership and security boundary for server-managed Meetings, configuration, credentials, and processing.
_Avoid_: Tenant, organization

**Meeting**:
The logical record that groups capture sessions, media, transcripts, and summaries. It is not a filesystem folder or a single recording file.
_Avoid_: Recording, meeting folder

**Recording Session**:
One bounded capture session associated with a Meeting.
_Avoid_: Meeting, audio file

**Media Asset**:
A persisted media object identified independently of its filesystem or object-storage location.
_Avoid_: File path, S3 key

**Transcript**:
The ordered transcription result associated with a Meeting and, when applicable, a processing run.

**Transcript Segment**:
One ordered span of transcript text with recording-relative timing and optional confidence, speaker, and timing provenance.
_Avoid_: Chunk, caption

**Summary**:
A generated meeting result whose content may use the existing structured summary JSON or a future normalized representation.

**Processing Job**:
The lifecycle record for asynchronous transcription, summary, import, transcode, or diarization work. It does not imply a particular queue or worker architecture.
_Avoid_: Process ID, task
