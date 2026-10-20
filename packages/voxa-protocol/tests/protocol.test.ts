import { describe, expect, test } from 'bun:test';
import {
  API_V1,
  apiError,
  createCredentialStatus,
  mediaAssetId,
  meetingId,
  processingJobId,
  recordingSessionId,
  summaryId,
  transcriptId,
  userId,
  workspaceId,
  type CapabilitiesResponse,
  type ConfigurationResponse,
  type CreateMeetingRequest,
  type Meeting,
  type MeetingListResponse,
  type ApiErrorResponse,
  type MeResponse,
  type CreateRecordingResponse,
  type MediaTranscriptResponse,
  WORKSPACE_SELECTION_REQUIRED,
  type Summary,
  type TranscriptSegment,
} from '../src/index.js';

describe('transport-safe identifiers', () => {
  test('serialize as ordinary JSON strings without wrappers', () => {
    const ids = {
      userId: userId('user-1'),
      workspaceId: workspaceId('workspace-1'),
      meetingId: meetingId('meeting-1'),
      recordingSessionId: recordingSessionId('session-1'),
      mediaAssetId: mediaAssetId('asset-1'),
      transcriptId: transcriptId('transcript-1'),
      summaryId: summaryId('summary-1'),
      processingJobId: processingJobId('job-1'),
    };

    expect(JSON.stringify(ids)).toBe(
      '{"userId":"user-1","workspaceId":"workspace-1","meetingId":"meeting-1","recordingSessionId":"session-1","mediaAssetId":"asset-1","transcriptId":"transcript-1","summaryId":"summary-1","processingJobId":"job-1"}',
    );
  });

  test('rejects empty identifier values at the protocol boundary', () => {
    expect(() => meetingId('')).toThrow('MeetingId cannot be empty');
    expect(() => workspaceId('   ')).toThrow('WorkspaceId cannot be empty');
  });
});

describe('meeting protocol', () => {
  test('creates meetings without accepting a filesystem location', () => {
    const request: CreateMeetingRequest = { title: 'Weekly planning' };
    expect(JSON.stringify(request)).toBe('{"title":"Weekly planning"}');
  });

  test('serializes workspace-owned meetings and cursor pagination', () => {
    const meeting: Meeting = {
      id: meetingId('meeting-1'),
      workspaceId: workspaceId('workspace-1'),
      title: 'Weekly planning',
      status: 'ready',
      createdAt: '2026-10-19T10:00:00.000Z',
      updatedAt: '2026-10-19T10:30:00.000Z',
    };
    const response: MeetingListResponse = {
      items: [meeting],
      page: { nextCursor: 'next-opaque-cursor', hasMore: true },
    };

    expect(response.items[0]).toEqual(meeting);
    expect(response.page).toEqual({ nextCursor: 'next-opaque-cursor', hasMore: true });
    expect(JSON.stringify(response)).not.toContain('folder');
    expect(JSON.stringify(response)).not.toContain('path');
  });
});

describe('transcript domain', () => {
  test('preserves recording-relative timing and optional future provenance', () => {
    const segment: TranscriptSegment = {
      id: 'segment-1',
      text: 'Decision approved',
      startTimeSeconds: 12.5,
      endTimeSeconds: 14.25,
      durationSeconds: 1.75,
      sequence: 3,
      isFinal: true,
      confidence: 0.94,
      source: 'whisper',
      speakerId: null,
      timingProvenance: 'asr',
    };

    expect(segment.startTimeSeconds).toBe(12.5);
    expect(segment.endTimeSeconds).toBe(14.25);
    expect(segment.durationSeconds).toBe(1.75);
    expect(segment.speakerId).toBeNull();
  });
});

describe('server configuration credentials', () => {
  test('configuration responses expose credential status but never raw secrets', () => {
    const response: ConfigurationResponse = {
      workspaceId: workspaceId('workspace-1'),
      transcription: { provider: 'whisper', model: 'large-v3', language: 'auto', translateToEnglish: false },
      summary: { provider: 'openai', model: 'gpt-4o-mini', outputLanguage: 'auto', templateId: null, autoGenerate: false },
      recording: { retainMedia: true },
      privacy: { analyticsEnabled: false },
      notifications: { processingComplete: true },
      credentials: [createCredentialStatus('openai', 'sk-super-secret')],
    };

    const json = JSON.stringify(response);
    expect(response.credentials).toEqual([{ provider: 'openai', configured: true, maskedValue: '••••' }]);
    expect(json).not.toContain('sk-super-secret');
    expect(json).not.toContain('apiKey');
    expect(json).not.toContain('secret');
  });

  test('summary content remains generic but JSON serializable', () => {
    const summary: Pick<Summary, 'content'> = {
      content: { sections: [{ title: 'Decisions', items: ['Ship v1'] }] },
    };
    expect(JSON.stringify(summary)).toBe(
      '{"content":{"sections":[{"title":"Decisions","items":["Ship v1"]}]}}',
    );
  });
});

describe('API v1 surface', () => {
  test('uses stable versioned routes', () => {
    expect(API_V1).toEqual({
      health: '/api/v1/health',
      capabilities: '/api/v1/capabilities',
      me: '/api/v1/me',
      meetings: '/api/v1/meetings',
      meeting: '/api/v1/meetings/:meetingId',
      configuration: '/api/v1/configuration',
      credentials: '/api/v1/credentials',
      credential: '/api/v1/credentials/:provider',
      meetingRecordings: '/api/v1/meetings/:meetingId/recordings',
      meetingMedia: '/api/v1/meetings/:meetingId/media',
      mediaAsset: '/api/v1/media/:mediaAssetId',
      mediaUpload: '/api/v1/media/:mediaAssetId/upload',
      mediaChunk: '/api/v1/media/:mediaAssetId/chunks/:index',
      mediaFinalize: '/api/v1/media/:mediaAssetId/finalize',
      mediaPlayback: '/api/v1/media/:mediaAssetId/playback',
      mediaContent: '/api/v1/media/:mediaAssetId/content',
      meetingJobs: '/api/v1/meetings/:meetingId/jobs',
      meetingTranscript: '/api/v1/meetings/:meetingId/transcript',
      mediaTranscript: '/api/v1/media/:mediaAssetId/transcript',
      mediaTranscription: '/api/v1/media/:mediaAssetId/transcription',
      jobCancel: '/api/v1/jobs/:jobId/cancel',
    });
  });

  test('serializes capability availability without secrets or native details', () => {
    const response: CapabilitiesResponse = {
      apiVersion: 'v1',
      mediaStorage: { available: true },
      transcription: { available: true, providers: ['whisper'] },
      summary: { available: true, providers: ['ollama', 'openai'] },
      configuration: { serverManaged: true, credentialManagement: true },
    };

    expect(JSON.parse(JSON.stringify(response))).toEqual(response);
    expect(JSON.stringify(response)).not.toContain('path');
  });

  test('serializes stable errors without native exception details', () => {
    const response = apiError('not_found', 'request-1');

    expect(response).toEqual({
      error: {
        code: 'not_found',
        message: 'Resource not found',
        requestId: 'request-1',
      },
    });
    expect(JSON.stringify(response)).not.toContain('sql');
  });
});

describe('authenticated identity', () => {
  test('describes the caller and active workspace without auth artifacts', () => {
    const response: MeResponse = {
      user: { id: userId('usr_1'), email: null, displayName: 'Ada' },
      workspace: { id: workspaceId('wsp_1'), name: 'Default workspace' },
      role: 'owner',
    };
    const wire = JSON.stringify(response);
    expect(JSON.parse(wire)).toEqual(response);
    expect(API_V1.me).toBe('/api/v1/me');
    for (const forbidden of ['token', 'issuer', 'subject', 'claims', 'password']) {
      expect(wire).not.toContain(forbidden);
    }
  });

  test('names the multi-workspace selection conflict', () => {
    expect(WORKSPACE_SELECTION_REQUIRED).toBe('workspace_selection_required');
    expect(apiError('unauthorized').error.message).toBe('Authentication required');
    expect(apiError('forbidden').error.message).toBe('Access forbidden');
  });
});

describe('processing protocol', () => {
  test('transcript responses carry recording-relative ASR segments and user-safe errors', () => {
    const response: MediaTranscriptResponse = {
      mediaAssetId: mediaAssetId('med_1'),
      state: 'ready',
      error: null,
      transcript: {
        id: transcriptId('trn_1'), workspaceId: workspaceId('wsp_1'), meetingId: meetingId('mtg_1'), mediaAssetId: mediaAssetId('med_1'),
        processingJobId: processingJobId('job_1'), language: 'en', asrEngine: 'whisper.cpp', asrModel: 'ggml-base.en',
        segments: [{ id: 'trn_1:0', text: 'Hello.', startTimeSeconds: 0.5, endTimeSeconds: 1.5, durationSeconds: 1, sequence: 0, isFinal: true, confidence: null, source: null, timingProvenance: 'asr' }],
        createdAt: '2026-10-20T10:00:00Z', updatedAt: '2026-10-20T10:00:00Z',
      },
    };
    expect(JSON.parse(JSON.stringify(response))).toEqual(response);
    const pending: MediaTranscriptResponse = { mediaAssetId: mediaAssetId('med_1'), state: 'transcribing', error: null, transcript: null };
    expect(pending.transcript).toBeNull();
  });
});

describe('media protocol', () => {
  test('recording creation exposes no storage locations', () => {
    const response: CreateRecordingResponse = {
      recordingSession: { id: recordingSessionId('rec_1'), workspaceId: workspaceId('wsp_1'), meetingId: meetingId('mtg_1'), status: 'recording', startedAt: '2026-10-19T10:00:00Z', endedAt: null, createdAt: '2026-10-19T10:00:00Z' },
      mediaAsset: { id: mediaAssetId('med_1'), workspaceId: workspaceId('wsp_1'), meetingId: meetingId('mtg_1'), recordingSessionId: recordingSessionId('rec_1'), kind: 'audio', mimeType: 'audio/webm;codecs=opus', status: 'uploading', durationSeconds: null, sizeBytes: 0, createdAt: '2026-10-19T10:00:00Z', updatedAt: '2026-10-19T10:00:00Z' },
      upload: { mediaAssetId: mediaAssetId('med_1'), status: 'uploading', receivedChunks: [], receivedBytes: 0, maxChunkBytes: 8388608, maxTotalBytes: 1073741824 },
    };
    const wire = JSON.stringify(response);
    expect(JSON.parse(wire)).toEqual(response);
    for (const forbidden of ['storage', 'path', 'folder', 'obj_']) expect(wire).not.toContain(forbidden);
  });
});

// Compile-time wire-safety assertions. `tsc` must reject these assignments.
const safeError: ApiErrorResponse = { error: { code: 'validation', message: 'Invalid request' } };
void safeError;
// @ts-expect-error Credential responses only permit the fixed non-secret mask.
const unsafeMask: import('../src/index.js').CredentialStatus = { provider: 'openai', configured: true, maskedValue: 'sk-secret' };
void unsafeMask;
// @ts-expect-error Summary content must be JSON data, not executable values.
const unsafeSummary: Pick<Summary, 'content'> = { content: () => 'not JSON' };
void unsafeSummary;
// @ts-expect-error Roles are limited to owner and member.
const unsafeRole: MeResponse['role'] = 'admin';
void unsafeRole;
// @ts-expect-error Media assets have no client-visible storage location.
const leakingAsset: import('../src/index.js').MediaAsset = { storageKey: 'obj_x' } as const;
void leakingAsset;
// @ts-expect-error Processing states are a closed set; queue internals are not exposed.
const queueState: MediaTranscriptResponse['state'] = 'remux_job_running';
void queueState;
