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
export interface RecordingSession { id: RecordingSessionId; workspaceId: WorkspaceId; meetingId: MeetingId; status: RecordingSessionStatus; startedAt: IsoDateTime; endedAt: IsoDateTime | null; createdAt: IsoDateTime; }
export type MediaAssetKind = 'audio';
/** uploading: chunks may still arrive; ready: assembled and playable; failed: abandoned. */
export type MediaAssetStatus = 'uploading' | 'ready' | 'failed';
export interface MediaAsset { id: MediaAssetId; workspaceId: WorkspaceId; meetingId: MeetingId; recordingSessionId: RecordingSessionId | null; kind: MediaAssetKind; mimeType: string; status: MediaAssetStatus; durationSeconds: number | null; sizeBytes: number; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
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
export interface Summary { id: SummaryId; workspaceId: WorkspaceId; meetingId: MeetingId; processingJobId: ProcessingJobId | null; content: JsonValue; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
/** `remux` rewrites a recording's container (stream copy) for reliable duration and seeking. */
export type ProcessingJobKind = 'remux' | 'transcription' | 'summary' | 'import' | 'transcode' | 'diarization';
export type ProcessingJobStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
/** `errorCode`/`errorMessage` are user-safe; internal diagnostics never leave the server. */
export interface ProcessingJob { id: ProcessingJobId; workspaceId: WorkspaceId; meetingId: MeetingId; mediaAssetId: MediaAssetId | null; kind: ProcessingJobKind; status: ProcessingJobStatus; progress: number | null; errorCode: string | null; errorMessage: string | null; createdAt: IsoDateTime; startedAt: IsoDateTime | null; completedAt: IsoDateTime | null; }
