'use client';

import { useEffect, useState } from 'react';
import { Mic, Pause, Play, Square } from 'lucide-react';
import { useVoxaClient } from '@/clients';
import type { RecordingDevice } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { useServerCapabilities, useWebRuntime } from '../context';
import { formatDuration, useElapsedSeconds, useRecordingSession, useUnloadGuard, useUploadJobs } from './hooks';
import type { UploadJobState } from './upload-coordinator';

export function uploadStatusText(job: UploadJobState): string {
  const local = job.pendingChunks > 0 ? `${job.pendingChunks} part${job.pendingChunks === 1 ? '' : 's'} saved on this device` : null;
  switch (job.phase) {
    case 'recording':
      return job.waitingForNetwork ? `Offline: recording continues, ${local ?? 'saving on this device'}` : local ? `Uploading (${local})` : 'Uploading as you record';
    case 'waiting':
      return `Waiting for the network: ${local ?? 'finishing upload'}. Keep this tab open.`;
    case 'uploading':
      return `Uploading: ${local ?? 'almost done'}`;
    case 'finalizing':
      return 'Finalizing recording…';
    case 'ready':
      return 'Recording saved';
    case 'error':
      return job.error ?? 'Upload failed';
  }
}

/** Minimal, reliability-first recorder for one meeting. */
export function RecordingPanel({ meetingId }: { meetingId: string }) {
  const { recordingSession, uploads, config } = useWebRuntime();
  const { recording } = useVoxaClient();
  const server = useServerCapabilities();
  const session = useRecordingSession();
  const jobs = useUploadJobs().filter((job) => job.meetingId === meetingId && job.phase !== 'ready');
  const [devices, setDevices] = useState<RecordingDevice[]>([]);
  const [device, setDevice] = useState<string>('');
  const active = session.meetingId === meetingId && session.phase !== 'idle';
  const otherMeetingRecording = session.phase !== 'idle' && session.meetingId !== meetingId;
  const elapsed = useElapsedSeconds(active && (session.phase === 'recording' || session.phase === 'paused'));
  useUnloadGuard(active || jobs.some((job) => job.phase !== 'error'));

  useEffect(() => {
    if (recording.capabilities.deviceEnumeration !== 'unavailable') recording.listDevices().then(setDevices).catch(() => setDevices([]));
  }, [recording, session.phase]);

  if (!recordingSession || !recording.capabilities.microphoneCapture) {
    return <p className="text-sm text-gray-500">This browser cannot record audio (it needs microphone access and MediaRecorder support).</p>;
  }
  if (server && !server.mediaStorage.available) {
    return <p className="text-sm text-gray-500">Recording is unavailable: this Voxa server does not store media.</p>;
  }

  return (
    <div className="space-y-3">
      {active ? (
        <div className="space-y-2 rounded-md border border-red-200 bg-red-50 p-3" role="status" aria-live="polite">
          <div className="flex items-center gap-3">
            <span className={`h-3 w-3 rounded-full bg-red-600 ${session.phase === 'recording' ? 'animate-pulse' : ''}`} aria-hidden />
            <span className="font-medium text-red-800">
              {session.phase === 'paused' ? 'Paused' : session.phase === 'starting' ? 'Starting…' : session.phase === 'stopping' ? 'Stopping…' : 'Recording'} {formatDuration(elapsed)}
            </span>
            <span className="ml-auto flex gap-2">
              {recording.capabilities.pauseResume && session.phase === 'recording' && (
                <Button size="sm" variant="outline" onClick={() => void recordingSession.pause()}><Pause className="mr-1 h-4 w-4" />Pause</Button>
              )}
              {session.phase === 'paused' && (
                <Button size="sm" variant="outline" onClick={() => void recordingSession.resume()}><Play className="mr-1 h-4 w-4" />Resume</Button>
              )}
              {(session.phase === 'recording' || session.phase === 'paused') && (
                <Button size="sm" variant="destructive" onClick={() => void recordingSession.stop()}><Square className="mr-1 h-4 w-4" />Stop</Button>
              )}
            </span>
          </div>
          <p className="text-xs text-red-700">{config.recordingNotice}</p>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          {devices.length > 1 && (
            <select className="rounded-md border border-gray-300 px-2 py-1.5 text-sm" value={device} onChange={(e) => setDevice(e.target.value)} aria-label="Microphone">
              <option value="">Default microphone</option>
              {devices.map((d) => <option key={d.name} value={d.name}>{d.name}</option>)}
            </select>
          )}
          <Button onClick={() => void recordingSession.start(meetingId, device || null)} disabled={otherMeetingRecording}>
            <Mic className="mr-2 h-4 w-4" />Start recording
          </Button>
          {otherMeetingRecording && <span className="text-xs text-gray-500">Another meeting is being recorded.</span>}
        </div>
      )}
      {session.error && session.meetingId === meetingId && <p className="text-sm text-red-700" role="alert">{session.error}</p>}
      {jobs.map((job) => (
        <div key={job.mediaAssetId} className="flex items-center gap-2 text-sm text-gray-700">
          <span className={job.phase === 'error' ? 'text-red-700' : ''}>{job.recovered ? 'Recovered recording: ' : ''}{uploadStatusText(job)}</span>
          {job.phase === 'error' && (
            <>
              <Button size="sm" variant="outline" onClick={() => uploads.retry(job.mediaAssetId)}>Retry</Button>
              <Button size="sm" variant="ghost" onClick={() => void uploads.discard(job.mediaAssetId)}>Discard</Button>
            </>
          )}
        </div>
      ))}
    </div>
  );
}
