import { invoke } from '@tauri-apps/api/core';
import type {
  CreateMeetingInput,
  CreateMeetingResult,
  MeetingCapabilities,
  MeetingClient,
  MeetingDetails,
  MeetingListItem,
  TranscriptSearchResult,
} from '../contracts';
import type { MeetingMetadata, PaginatedTranscriptsResponse } from '@/types';

export class TauriMeetingClient implements MeetingClient {
  readonly capabilities: MeetingCapabilities = {
    transcripts: true,
    transcriptSearch: true,
    createWithTranscripts: true,
  };

  list(): Promise<MeetingListItem[]> {
    return invoke('api_get_meetings');
  }

  searchTranscripts(query: string): Promise<TranscriptSearchResult[]> {
    return invoke('api_search_transcripts', { query });
  }

  get(meetingId: string): Promise<MeetingDetails> {
    return invoke('api_get_meeting', { meetingId });
  }

  getMetadata(meetingId: string): Promise<MeetingMetadata> {
    return invoke('api_get_meeting_metadata', { meetingId });
  }

  getTranscripts(
    meetingId: string,
    { limit, offset }: { limit: number; offset: number },
  ): Promise<PaginatedTranscriptsResponse> {
    return invoke('api_get_meeting_transcripts', { meetingId, limit, offset });
  }

  create({ title, transcripts, folderPath }: CreateMeetingInput): Promise<CreateMeetingResult> {
    return invoke('api_save_transcript', {
      meetingTitle: title,
      transcripts,
      folderPath,
    });
  }

  async rename(meetingId: string, title: string): Promise<void> {
    await invoke('api_save_meeting_title', { meetingId, title });
  }

  async delete(meetingId: string): Promise<void> {
    await invoke('api_delete_meeting', { meetingId });
  }

}
