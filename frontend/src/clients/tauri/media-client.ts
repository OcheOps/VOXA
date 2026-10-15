import { invoke } from '@tauri-apps/api/core';
import type { MediaClient } from '../contracts';

export class TauriMediaClient implements MediaClient {
  readAudioFile(filePath: string): Promise<number[]> {
    return invoke('read_audio_file', { filePath });
  }
}
