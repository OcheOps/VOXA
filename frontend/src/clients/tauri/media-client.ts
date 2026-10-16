import { invoke } from '@tauri-apps/api/core';
import type {
  MediaCapabilities, MediaClient, MediaImportSelection, MediaPlaybackSource, MediaSource,
} from '../contracts';
import { createTauriMediaSource, getTauriMediaReference } from './media-source';

interface NativeAudioFileInfo {
  path: string;
  filename: string;
  duration_seconds: number;
  size_bytes: number;
  format: string;
}

interface NativeMeetingMetadata { folder_path?: string | null; }

function importSelection(file: NativeAudioFileInfo): MediaImportSelection {
  return {
    source: createTauriMediaSource(file.path),
    fileName: file.filename,
    durationSeconds: file.duration_seconds,
    sizeBytes: file.size_bytes,
    format: file.format,
  };
}

export class TauriMediaClient implements MediaClient {
  readonly capabilities: MediaCapabilities = {
    nativeFilePicker: true,
    filesystemReveal: true,
    localByteReads: true,
    urlPlayback: false,
    rangedStreaming: false,
    import: true,
    exportDownload: false,
  };

  async getMeetingSource(meetingId: string): Promise<MediaSource | null> {
    const metadata = await invoke<NativeMeetingMetadata>('api_get_meeting_metadata', { meetingId });
    return metadata.folder_path ? createTauriMediaSource(metadata.folder_path) : null;
  }

  async getPlaybackSource(source: MediaSource): Promise<MediaPlaybackSource> {
    const bytes = await invoke<number[]>('read_audio_file', {
      filePath: getTauriMediaReference(source),
    });
    return { kind: 'bytes', bytes };
  }

  async selectImportSource(): Promise<MediaImportSelection | null> {
    const selected = await invoke<NativeAudioFileInfo | null>('select_and_validate_audio_command');
    return selected ? importSelection(selected) : null;
  }

  async validateDroppedImport(nativeReference: string): Promise<MediaImportSelection> {
    const selected = await invoke<NativeAudioFileInfo>('validate_audio_file_command', {
      path: nativeReference,
    });
    return importSelection(selected);
  }

  openMeetingFolder(meetingId: string): Promise<void> {
    return invoke('open_meeting_folder', { meetingId });
  }

  openRecordingsFolder(): Promise<void> {
    return invoke('open_recordings_folder');
  }
}
