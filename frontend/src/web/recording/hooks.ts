'use client';

import { useEffect, useState, useSyncExternalStore } from 'react';
import { toast } from 'sonner';
import { useWebRuntime } from '../context';
import type { SessionState } from './session';
import type { UploadJobState } from './upload-coordinator';

const idleSession: SessionState = { phase: 'idle', meetingId: null, mediaAssetId: null, error: null };
const noop = () => () => {};

export function useRecordingSession(): SessionState {
  const { recordingSession } = useWebRuntime();
  return useSyncExternalStore(
    recordingSession?.subscribe ?? noop,
    recordingSession?.getState ?? (() => idleSession),
    recordingSession?.getState ?? (() => idleSession),
  );
}

export function useUploadJobs(): UploadJobState[] {
  const { uploads } = useWebRuntime();
  const [jobs, setJobs] = useState(() => uploads.getJobs());
  useEffect(() => uploads.subscribe(() => setJobs(uploads.getJobs())), [uploads]);
  return jobs;
}

/** Elapsed recorded (non-paused) seconds, refreshed every second while recording. */
export function useElapsedSeconds(active: boolean): number {
  const { client } = useWebRuntime();
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    if (!active) return;
    const update = () => void client.recording.getState().then((state) => setSeconds(state.activeDuration ?? 0));
    update();
    const timer = setInterval(update, 1000);
    return () => clearInterval(timer);
  }, [active, client.recording]);
  return active ? seconds : 0;
}

/** Resumes uploads left by previous pages once the user is signed in. */
export function useRecordingRecovery(): void {
  const { uploads } = useWebRuntime();
  useEffect(() => {
    let cancelled = false;
    uploads.recoverPending().then((resumed) => {
      if (!cancelled && resumed.length > 0) {
        toast.info(`Resuming upload of ${resumed.length} unsaved recording${resumed.length === 1 ? '' : 's'}.`);
      }
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [uploads]);
}

/** Warns before closing the page while audio exists only on this device. */
export function useUnloadGuard(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [active]);
}

export function formatDuration(totalSeconds: number | null): string {
  if (totalSeconds === null || !Number.isFinite(totalSeconds)) return '–';
  const seconds = Math.floor(totalSeconds);
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  const pad = (n: number) => n.toString().padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}
