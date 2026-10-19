declare const idBrand: unique symbol;

export type BrandedId<Name extends string> = string & { readonly [idBrand]: Name };
export type UserId = BrandedId<'UserId'>;
export type WorkspaceId = BrandedId<'WorkspaceId'>;
export type MeetingId = BrandedId<'MeetingId'>;
export type RecordingSessionId = BrandedId<'RecordingSessionId'>;
export type MediaAssetId = BrandedId<'MediaAssetId'>;
export type TranscriptId = BrandedId<'TranscriptId'>;
export type SummaryId = BrandedId<'SummaryId'>;
export type ProcessingJobId = BrandedId<'ProcessingJobId'>;

function brandedId<Name extends string>(name: Name, value: string): BrandedId<Name> {
  if (value.trim().length === 0) throw new Error(`${name} cannot be empty`);
  return value as BrandedId<Name>;
}

export const userId = (value: string): UserId => brandedId('UserId', value);
export const workspaceId = (value: string): WorkspaceId => brandedId('WorkspaceId', value);
export const meetingId = (value: string): MeetingId => brandedId('MeetingId', value);
export const recordingSessionId = (value: string): RecordingSessionId => brandedId('RecordingSessionId', value);
export const mediaAssetId = (value: string): MediaAssetId => brandedId('MediaAssetId', value);
export const transcriptId = (value: string): TranscriptId => brandedId('TranscriptId', value);
export const summaryId = (value: string): SummaryId => brandedId('SummaryId', value);
export const processingJobId = (value: string): ProcessingJobId => brandedId('ProcessingJobId', value);
