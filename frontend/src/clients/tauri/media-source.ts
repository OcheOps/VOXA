import type { MediaSource } from '../contracts';

const nativeReferences = new WeakMap<object, string>();

export function createTauriMediaSource(nativeReference: string): MediaSource {
  const source = {} as MediaSource;
  nativeReferences.set(source, nativeReference);
  return source;
}

export function getTauriMediaReference(source: MediaSource): string {
  const nativeReference = nativeReferences.get(source);
  if (!nativeReference) throw new Error('Media source does not belong to the Tauri transport');
  return nativeReference;
}
