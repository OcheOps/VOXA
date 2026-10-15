import { describe, expect, mock, test } from 'bun:test';

const calls: Array<{ command: string; args?: Record<string, unknown> }> = [];
const invoke = mock(async (command: string, args?: Record<string, unknown>) => {
  calls.push({ command, args });
  if (command === 'api_get_meetings') return [{ id: 'meeting-1', title: 'Planning' }];
  if (command === 'api_get_meeting_metadata') {
    return { id: 'meeting-1', title: 'Planning', created_at: '2026-10-14', updated_at: '2026-10-14' };
  }
  if (command === 'api_get_meeting_transcripts') {
    return { transcripts: [], total_count: 0, has_more: false };
  }
  if (command === 'api_save_transcript') return { meeting_id: 'meeting-2' };
  return undefined;
});

mock.module('@tauri-apps/api/core', () => ({ invoke }));
const { TauriMeetingClient } = await import('../../src/clients/tauri/meeting-client');

describe('TauriMeetingClient', () => {
  test('preserves existing meeting query commands and payloads', async () => {
    calls.length = 0;
    const client = new TauriMeetingClient();

    expect(await client.list()).toEqual([{ id: 'meeting-1', title: 'Planning' }]);
    await client.getMetadata('meeting-1');
    await client.getTranscripts('meeting-1', { limit: 100, offset: 0 });

    expect(calls).toEqual([
      { command: 'api_get_meetings', args: undefined },
      { command: 'api_get_meeting_metadata', args: { meetingId: 'meeting-1' } },
      {
        command: 'api_get_meeting_transcripts',
        args: { meetingId: 'meeting-1', limit: 100, offset: 0 },
      },
    ]);
  });

  test('preserves existing meeting mutation commands and payloads', async () => {
    calls.length = 0;
    const client = new TauriMeetingClient();
    const transcripts = [{ id: 'segment-1', text: 'Hello', timestamp: '10:00:00' }];

    expect(await client.create({ title: 'New meeting', transcripts, folderPath: '/recording' }))
      .toEqual({ meeting_id: 'meeting-2' });
    await client.rename('meeting-1', 'Renamed');
    await client.delete('meeting-1');
    await client.openRecordingFolder('meeting-1');

    expect(calls).toEqual([
      {
        command: 'api_save_transcript',
        args: { meetingTitle: 'New meeting', transcripts, folderPath: '/recording' },
      },
      {
        command: 'api_save_meeting_title',
        args: { meetingId: 'meeting-1', title: 'Renamed' },
      },
      { command: 'api_delete_meeting', args: { meetingId: 'meeting-1' } },
      { command: 'open_meeting_folder', args: { meetingId: 'meeting-1' } },
    ]);
  });
});
