import { invoke } from '@tauri-apps/api/core';
import type { SummaryClient } from '../contracts';
import type {
  CancelSummaryResponse,
  ProcessTranscriptResponse,
  SummaryProcessResponse,
} from '@/types';

export class TauriSummaryClient implements SummaryClient {
  get(meetingId: string): Promise<SummaryProcessResponse> {
    return invoke('api_get_summary', { meetingId });
  }

  process(input: Record<string, unknown>): Promise<ProcessTranscriptResponse> {
    return invoke('api_process_transcript', input);
  }

  cancel(meetingId: string, processId?: string): Promise<CancelSummaryResponse> {
    return invoke('api_cancel_summary', { meetingId, processId });
  }

  async save(meetingId: string, summary: unknown): Promise<void> {
    await invoke('api_save_meeting_summary', { meetingId, summary });
  }
}
