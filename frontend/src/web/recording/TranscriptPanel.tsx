'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';
import type { RecordingTranscript } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { describeError } from '../errors';
import { formatDuration } from './hooks';

/** Seconds between status checks while a recording is being processed. */
export const PROCESSING_POLL_MS = 3000;

const ACTIVE = new Set(['preparing', 'transcribing']);

export interface SeekTarget {
  seek(seconds: number): void;
}

/**
 * Processing status and transcript for one stored recording. Polls only while
 * work is in progress; clicking a segment seeks playback to its
 * recording-relative start time.
 */
export function TranscriptPanel({ meetingId, mediaAssetId, player, onProcessed }: {
  meetingId: string;
  mediaAssetId: string;
  player: SeekTarget | null;
  onProcessed?: () => void;
}) {
  const { serverTranscripts } = useVoxaClient();
  const [data, setData] = useState<RecordingTranscript | null>(null);
  const [busy, setBusy] = useState(false);
  const previousState = useRef<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!serverTranscripts) return;
    try {
      const next = await serverTranscripts.getRecordingTranscript(mediaAssetId, signal);
      setData(next);
      if (previousState.current && ACTIVE.has(previousState.current) && !ACTIVE.has(next.state)) onProcessed?.();
      previousState.current = next.state;
    } catch (error) {
      if ((error as DOMException)?.name !== 'AbortError') setData((current) => current);
    }
  }, [mediaAssetId, onProcessed, serverTranscripts]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  useEffect(() => {
    if (!data || !ACTIVE.has(data.state)) return;
    const timer = setInterval(() => void load(), PROCESSING_POLL_MS);
    return () => clearInterval(timer);
  }, [data, load]);

  if (!serverTranscripts) return null;
  if (!data) return <p className="text-xs text-gray-500">Checking transcript…</p>;

  const act = async (action: () => Promise<unknown>, failure: string) => {
    setBusy(true);
    try {
      await action();
      await load();
    } catch (error) {
      toast.error(failure, { description: describeError(error) });
    } finally {
      setBusy(false);
    }
  };
  const transcribe = () => act(() => serverTranscripts.requestTranscription(mediaAssetId), 'Could not start transcription');
  const cancel = () => act(() => serverTranscripts.cancelProcessing(meetingId, mediaAssetId), 'Could not cancel');

  switch (data.state) {
    case 'preparing':
      return <p className="text-sm text-gray-600" role="status">Preparing recording…</p>;
    case 'transcribing':
      return (
        <div className="flex items-center gap-2 text-sm text-gray-600" role="status">
          <span>Transcribing…</span>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void cancel()}>Cancel</Button>
        </div>
      );
    case 'failed':
      return (
        <div className="flex items-center gap-2 text-sm text-red-700" role="alert">
          <span>{data.error?.message ?? 'Transcription failed.'}</span>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void transcribe()}>Retry</Button>
        </div>
      );
    case 'cancelled':
      return (
        <div className="flex items-center gap-2 text-sm text-gray-600">
          <span>Transcription was cancelled.</span>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void transcribe()}>Transcribe</Button>
        </div>
      );
    case 'unavailable':
      return <p className="text-sm text-gray-500">Transcription is not available on this server.</p>;
    case 'none':
      return (
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void transcribe()}>Transcribe</Button>
      );
    case 'ready':
      break;
  }

  const segments = data.transcript?.segments ?? [];
  if (segments.length === 0) return <p className="text-sm text-gray-500">No speech was detected in this recording.</p>;
  return (
    <div className="space-y-1">
      <ol className="max-h-96 space-y-1 overflow-y-auto text-sm" aria-label="Transcript">
        {segments.map((segment) => (
          <li key={segment.id}>
            <button
              type="button"
              className="group flex w-full gap-3 rounded px-1 py-0.5 text-left hover:bg-gray-50"
              onClick={() => player?.seek(segment.startSeconds)}
              aria-label={`Play from ${formatDuration(segment.startSeconds)}`}
            >
              <span className="shrink-0 font-mono text-xs text-blue-700 group-hover:underline">{formatDuration(segment.startSeconds)}</span>
              <span className="text-gray-800">{segment.text}</span>
            </button>
          </li>
        ))}
      </ol>
      <p className="text-xs text-gray-400">
        Transcribed by {data.transcript?.asrEngine ?? 'the server'}{data.transcript?.asrModel ? ` (${data.transcript.asrModel})` : ''}. Machine transcription may contain errors.
        {' '}
        <button type="button" className="underline" disabled={busy} onClick={() => void transcribe()}>Transcribe again</button>
      </p>
    </div>
  );
}
