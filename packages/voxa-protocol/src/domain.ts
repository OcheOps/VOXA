import type { MediaAssetId, MeetingId, ProcessingJobId, RecordingSessionId, SummaryId, TranscriptId, UserId, WorkspaceId } from './ids.js';

/** ISO-8601 timestamp string. Validation belongs at the transport edge. */
export type IsoDateTime = string;
export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };
export interface User { id: UserId; email: string; createdAt: IsoDateTime; }
export interface Workspace { id: WorkspaceId; name: string; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
/** Workspace membership role. Both roles can use all workspace features; owner is reserved for future administration. */
export type WorkspaceRole = 'owner' | 'member';
export type MeetingStatus = 'created' | 'recording' | 'processing' | 'ready' | 'failed';
export interface Meeting { id: MeetingId; workspaceId: WorkspaceId; title: string; status: MeetingStatus; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
export type RecordingSessionStatus = 'recording' | 'paused' | 'completed' | 'failed' | 'cancelled';
/** How a recording was captured when it started. `unknown`: legacy recordings or clients that do not report it. */
export type CaptureMode = 'microphone' | 'shared_audio' | 'microphone_and_shared_audio' | 'unknown';
export type CaptureClient = 'web' | 'desktop' | 'extension' | 'android' | 'unknown';
/** Kind of source shared for shared audio (never a URL or title). */
export type SharedSurface = 'browser' | 'window' | 'monitor' | 'unknown';
/**
 * Operational capture provenance for diagnostics. It never contains a user
 * agent, URL, tab title or meeting-provider data.
 */
export interface CaptureProvenance { mode: CaptureMode; client: CaptureClient; sharedSurface: SharedSurface | null; }
/** `capture` is additive: servers before capture provenance omit it. */
export interface RecordingSession { id: RecordingSessionId; workspaceId: WorkspaceId; meetingId: MeetingId; status: RecordingSessionStatus; startedAt: IsoDateTime; endedAt: IsoDateTime | null; createdAt: IsoDateTime; capture?: CaptureProvenance; }
export type MediaAssetKind = 'audio';
/** uploading: chunks may still arrive; ready: assembled and playable; failed: abandoned. */
export type MediaAssetStatus = 'uploading' | 'ready' | 'failed';
export interface MediaAsset { id: MediaAssetId; workspaceId: WorkspaceId; meetingId: MeetingId; recordingSessionId: RecordingSessionId | null; kind: MediaAssetKind; mimeType: string; status: MediaAssetStatus; durationSeconds: number | null; sizeBytes: number; createdAt: IsoDateTime; updatedAt: IsoDateTime; /** Provenance of the recording session (additive; null without a session). */ capture?: CaptureProvenance | null; }
export type TranscriptTimingProvenance = 'asr' | 'aligned' | 'manual';
export interface TranscriptSegment {
  id: string;
  text: string;
  startTimeSeconds: number;
  endTimeSeconds: number;
  durationSeconds: number;
  sequence: number;
  isFinal: boolean;
  confidence: number | null;
  source: string | null;
  speakerId?: string | null;
  timingProvenance?: TranscriptTimingProvenance | null;
}
/**
 * A transcript of one recording. Segment times are relative to the start of
 * that recording. `asrEngine`/`asrModel` record which recognizer produced it.
 */
export interface Transcript { id: TranscriptId; workspaceId: WorkspaceId; meetingId: MeetingId; mediaAssetId: MediaAssetId | null; processingJobId: ProcessingJobId | null; language: string | null; asrEngine: string | null; asrModel: string | null; segments: TranscriptSegment[]; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
/**
 * A meeting summary. Server-generated summaries carry provenance: the
 * transcript, provider, model and template that produced them. `content` is
 * structured JSON; server summaries use `SummaryMarkdownContent`.
 */
export interface Summary {
  id: SummaryId; workspaceId: WorkspaceId; meetingId: MeetingId; processingJobId: ProcessingJobId | null; content: JsonValue;
  transcriptId?: TranscriptId | null; mediaAssetId?: MediaAssetId | null; provider?: string | null; model?: string | null;
  templateId?: string | null; outputLanguage?: string | null;
  createdAt: IsoDateTime; updatedAt: IsoDateTime;
}
/** Content of a server-generated summary (same Markdown as the desktop result). */
export interface SummaryMarkdownContent {
  format: 'markdown'; markdown: string; title?: string; englishMarkdown?: string;
  chunkCount: number; reasoningStripped: boolean; normalizationFallback: boolean;
}
/** `remux` rewrites a recording's container (stream copy) for reliable duration and seeking. */
export type ProcessingJobKind = 'remux' | 'transcription' | 'summary' | 'import' | 'transcode' | 'diarization';
export type ProcessingJobStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
/** `errorCode`/`errorMessage` are user-safe; internal diagnostics never leave the server. */
export interface ProcessingJob { id: ProcessingJobId; workspaceId: WorkspaceId; meetingId: MeetingId; mediaAssetId: MediaAssetId | null; kind: ProcessingJobKind; status: ProcessingJobStatus; progress: number | null; errorCode: string | null; errorMessage: string | null; createdAt: IsoDateTime; startedAt: IsoDateTime | null; completedAt: IsoDateTime | null; }
