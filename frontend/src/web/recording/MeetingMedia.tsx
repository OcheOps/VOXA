'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';
import type { ServerMediaAsset } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { describeError } from '../errors';
import { formatDuration, useUploadJobs } from './hooks';
import { TranscriptPanel, type SeekTarget } from './TranscriptPanel';

/**
 * Streams a ready recording through a short-lived signed URL with HTTP range
 * requests (no full download or decoding into memory). If the URL expires
 * mid-session, a fresh one is fetched and playback resumes at the same time.
 */
function Player({ asset, onElement }: { asset: ServerMediaAsset; onElement?: (element: HTMLAudioElement | null) => void }) {
  const { serverMedia } = useVoxaClient();
  const audio = useRef<HTMLAudioElement | null>(null);
  const attach = useCallback((element: HTMLAudioElement | null) => {
    audio.current = element;
    onElement?.(element);
  }, [onElement]);
  const [url, setUrl] = useState<string | null>(null);
  const refreshed = useRef(false);

  useEffect(() => {
    serverMedia?.getPlaybackUrl(asset.id).then((playback) => setUrl(playback.url)).catch((error) => {
      toast.error('Could not load the recording', { description: describeError(error) });
    });
  }, [asset.id, serverMedia]);

  const onError = useCallback(async () => {
    if (!serverMedia || refreshed.current) return;
    refreshed.current = true;
    const position = audio.current?.currentTime ?? 0;
    try {
      const playback = await serverMedia.getPlaybackUrl(asset.id);
      setUrl(playback.url);
      requestAnimationFrame(() => { if (audio.current) audio.current.currentTime = position; });
    } catch (error) {
      toast.error('Playback failed', { description: describeError(error) });
    }
  }, [asset.id, serverMedia]);

  if (!url) return <p className="text-xs text-gray-500">Loading player…</p>;
  return (
    <audio ref={attach} controls preload="metadata" src={url} onError={() => void onError()} onPlaying={() => { refreshed.current = false; }} className="w-full">
      Your browser cannot play this recording.
    </audio>
  );
}

/** One stored recording: player plus processing status and transcript. */
function Recording({ meetingId, asset, onChanged, onDelete }: { meetingId: string; asset: ServerMediaAsset; onChanged: () => void; onDelete: () => void }) {
  const [element, setElement] = useState<HTMLAudioElement | null>(null);
  const seekTarget: SeekTarget | null = element ? {
    seek(seconds) {
      element.currentTime = seconds;
      void element.play().catch(() => {});
    },
  } : null;
  return (
    <li className="space-y-2 rounded-md border border-gray-200 p-3">
      <div className="flex items-center gap-2 text-xs text-gray-500">
        <span>{new Date(asset.createdAt).toLocaleString()}</span>
        <span>· {formatDuration(asset.durationSeconds)}</span>
        {asset.status === 'ready' && <span>· {(asset.sizeBytes / 1024 / 1024).toFixed(1)} MB</span>}
        <Button size="sm" variant="ghost" className="ml-auto h-6 px-2 text-xs" onClick={onDelete} aria-label="Delete recording">Delete</Button>
      </div>
      {asset.status === 'ready' && (
        <>
          <Player asset={asset} onElement={setElement} />
          <TranscriptPanel meetingId={meetingId} mediaAssetId={asset.id} player={seekTarget} onProcessed={onChanged} />
        </>
      )}
      {asset.status === 'failed' && <p className="text-sm text-red-700">This upload was not completed and cannot be played.</p>}
    </li>
  );
}

export function MeetingMedia({ meetingId }: { meetingId: string }) {
  const { serverMedia } = useVoxaClient();
  const jobs = useUploadJobs();
  const [assets, setAssets] = useState<ServerMediaAsset[] | null>(null);
  const readyJobs = jobs.filter((job) => job.meetingId === meetingId && job.phase === 'ready').length;
  const localJobs = new Set(jobs.filter((job) => job.meetingId === meetingId).map((job) => job.mediaAssetId));

  const load = useCallback(async () => {
    if (!serverMedia) return;
    try {
      setAssets(await serverMedia.listMeetingMedia(meetingId));
    } catch (error) {
      toast.error('Could not load recordings', { description: describeError(error) });
    }
  }, [meetingId, serverMedia]);

  useEffect(() => { void load(); }, [load, readyJobs]);

  if (!serverMedia) return null;
  if (assets === null) return <p className="text-sm text-gray-500">Loading recordings…</p>;
  if (assets.length === 0) return <p className="text-sm text-gray-500">No recordings yet.</p>;

  const remove = async (asset: ServerMediaAsset) => {
    try {
      await serverMedia.deleteMedia(asset.id);
      await load();
    } catch (error) {
      toast.error('Could not delete the recording', { description: describeError(error) });
    }
  };

  return (
    <ul className="space-y-3">
      {assets.map((asset) => asset.status === 'uploading' ? (
        <li key={asset.id} className="rounded-md border border-gray-200 p-3 text-sm text-gray-600">
          {localJobs.has(asset.id) ? 'Upload in progress (see above).' : 'Incomplete upload. It can only be resumed from the device that recorded it; it will be discarded automatically if it is not completed.'}
          <Button size="sm" variant="ghost" className="ml-2 h-6 px-2 text-xs" onClick={() => void remove(asset)}>Delete</Button>
        </li>
      ) : (
        <Recording key={asset.id} meetingId={meetingId} asset={asset} onChanged={() => void load()} onDelete={() => void remove(asset)} />
      ))}
    </ul>
  );
}
