import type { GetMeetingResponse, Meeting, MeetingListResponse, CreateMeetingResponse } from '@voxa/protocol';
import type { MeetingMetadata, PaginatedTranscriptsResponse } from '@/types';
import type {
  CreateMeetingInput,
  CreateMeetingResult,
  MeetingCapabilities,
  MeetingClient,
  MeetingDetails,
  MeetingListItem,
  TranscriptSearchResult,
} from '../contracts';
import { UnsupportedOperationError } from '../errors';
import type { HttpTransport } from './transport';

/** Server page size (the protocol maximum) and a hard bound on pages followed. */
const PAGE_SIZE = 100;
const MAX_PAGES = 100;

function toMetadata(meeting: Meeting): MeetingMetadata {
  return { id: meeting.id, title: meeting.title, created_at: meeting.createdAt, updated_at: meeting.updatedAt };
}

function meetingPath(meetingId: string): string {
  return `/api/v1/meetings/${encodeURIComponent(meetingId)}`;
}

/**
 * MeetingClient over protocol v1. Workspace scope is resolved by the server
 * from the caller's identity; no workspace ID is sent or exposed. Transcripts
 * and search do not exist server-side yet and are reported as unavailable.
 */
export class HttpMeetingClient implements MeetingClient {
  readonly capabilities: MeetingCapabilities = {
    transcripts: false,
    transcriptSearch: false,
    createWithTranscripts: false,
  };

  constructor(private readonly transport: HttpTransport) {}

  /** Follows the opaque cursor until the server reports no more pages. */
  async list(): Promise<MeetingListItem[]> {
    const items: MeetingListItem[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < MAX_PAGES; page++) {
      const response = await this.transport.get<MeetingListResponse>('/api/v1/meetings', { query: { limit: PAGE_SIZE, cursor } });
      items.push(...response.items.map(({ id, title, createdAt, updatedAt }) => ({ id, title, createdAt, updatedAt })));
      if (!response.page.hasMore || !response.page.nextCursor) return items;
      cursor = response.page.nextCursor;
    }
    return items;
  }

  async searchTranscripts(): Promise<TranscriptSearchResult[]> {
    return [];
  }

  async get(meetingId: string): Promise<MeetingDetails> {
    return { ...(await this.getMetadata(meetingId)), transcripts: [] };
  }

  async getMetadata(meetingId: string): Promise<MeetingMetadata> {
    const response = await this.transport.get<GetMeetingResponse>(meetingPath(meetingId));
    return toMetadata(response.meeting);
  }

  async getTranscripts(): Promise<PaginatedTranscriptsResponse> {
    return { transcripts: [], total_count: 0, has_more: false };
  }

  async create(input: CreateMeetingInput): Promise<CreateMeetingResult> {
    if (input.transcripts.length > 0 || input.folderPath !== null) {
      throw new UnsupportedOperationError('Creating a meeting from transcripts or a local folder');
    }
    const response = await this.transport.post<CreateMeetingResponse>('/api/v1/meetings', { title: input.title });
    return { meeting_id: response.meeting.id };
  }

  async rename(meetingId: string, title: string): Promise<void> {
    await this.transport.patch(meetingPath(meetingId), { title });
  }

  async delete(meetingId: string): Promise<void> {
    await this.transport.delete(meetingPath(meetingId));
  }
}
