import type { MediaAssetId, MeetingId, ProcessingJobId, RecordingSessionId, SummaryId, TranscriptId, UserId, WorkspaceId } from './ids.js';

/** ISO-8601 timestamp string. Validation belongs at the transport edge. */
export type IsoDateTime = string;
export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };
export interface User { id: UserId; email: string; createdAt: IsoDateTime; }
export interface Workspace { id: WorkspaceId; name: string; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
export type MeetingStatus = 'created' | 'recording' | 'processing' | 'ready' | 'failed';
export interface Meeting { id: MeetingId; workspaceId: WorkspaceId; title: string; status: MeetingStatus; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
export type RecordingSessionStatus = 'recording' | 'paused' | 'completed' | 'failed' | 'cancelled';
export interface RecordingSession { id: RecordingSessionId; workspaceId: WorkspaceId; meetingId: MeetingId; status: RecordingSessionStatus; startedAt: IsoDateTime; endedAt: IsoDateTime | null; createdAt: IsoDateTime; }
export type MediaAssetKind = 'audio';
export interface MediaAsset { id: MediaAssetId; workspaceId: WorkspaceId; meetingId: MeetingId; recordingSessionId: RecordingSessionId | null; kind: MediaAssetKind; mimeType: string; durationSeconds: number | null; sizeBytes: number; createdAt: IsoDateTime; }
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
export interface Transcript { id: TranscriptId; workspaceId: WorkspaceId; meetingId: MeetingId; processingJobId: ProcessingJobId | null; language: string | null; segments: TranscriptSegment[]; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
export interface Summary { id: SummaryId; workspaceId: WorkspaceId; meetingId: MeetingId; processingJobId: ProcessingJobId | null; content: JsonValue; createdAt: IsoDateTime; updatedAt: IsoDateTime; }
export type ProcessingJobKind = 'transcription' | 'summary' | 'import' | 'transcode' | 'diarization';
export type ProcessingJobStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
export interface ProcessingJob { id: ProcessingJobId; workspaceId: WorkspaceId; meetingId: MeetingId; kind: ProcessingJobKind; status: ProcessingJobStatus; progress: number | null; errorCode: string | null; createdAt: IsoDateTime; startedAt: IsoDateTime | null; completedAt: IsoDateTime | null; }
