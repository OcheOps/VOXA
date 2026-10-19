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
      meetings: '/api/v1/meetings',
      meeting: '/api/v1/meetings/:meetingId',
      configuration: '/api/v1/configuration',
      credentials: '/api/v1/credentials',
      credential: '/api/v1/credentials/:provider',
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

// Compile-time wire-safety assertions. `tsc` must reject these assignments.
const safeError: ApiErrorResponse = { error: { code: 'validation', message: 'Invalid request' } };
void safeError;
// @ts-expect-error Credential responses only permit the fixed non-secret mask.
const unsafeMask: import('../src/index.js').CredentialStatus = { provider: 'openai', configured: true, maskedValue: 'sk-secret' };
void unsafeMask;
// @ts-expect-error Summary content must be JSON data, not executable values.
const unsafeSummary: Pick<Summary, 'content'> = { content: () => 'not JSON' };
void unsafeSummary;
