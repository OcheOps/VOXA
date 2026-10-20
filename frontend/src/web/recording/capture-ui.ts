import type { BrowserCaptureCapabilities, CaptureDescription, CaptureMode } from '@/clients/browser/recording-client';

export const MODE_LABELS: Record<CaptureMode, string> = {
  microphone: 'Microphone only',
  'microphone+shared': 'Meeting tab + microphone',
  shared: 'Meeting tab only',
};

export const MODE_DESCRIPTIONS: Record<CaptureMode, string> = {
  microphone: 'Records your microphone. In a call, remote participants are captured only if your speakers are audible to the microphone.',
  'microphone+shared': 'Records your microphone and the audio of a tab you share (remote participants).',
  shared: 'Records only the audio of a tab you share, for example when you are listening without speaking.',
};

/** Shown before starting a shared mode: Voxa cannot pick the tab for the user. */
export const SHARE_INSTRUCTIONS =
  'When you start, your browser asks what to share. Choose the tab where your meeting is running and turn on “Share tab audio”. Voxa cannot choose the tab for you, and only that tab’s audio is recorded (video is discarded).';

export function sharedAudioNote(capabilities: BrowserCaptureCapabilities): string | null {
  if (capabilities.sharedAudio === 'unknown') {
    return 'This browser may share video without audio. Voxa checks the shared source and tells you if it has no audio.';
  }
  return null;
}

const SURFACES = { browser: 'meeting tab audio', window: 'shared window audio', monitor: 'shared screen audio', unknown: 'shared audio' } as const;

/** Exactly what is being recorded, for the recording indicator. */
export function captureSummary(capture: CaptureDescription | null): string {
  if (!capture) return 'Recording';
  const parts: string[] = [];
  if (capture.microphone) parts.push('your microphone');
  if (capture.shared) parts.push(SURFACES[capture.shared.surface]);
  if (parts.length === 0) return 'Recording';
  return `Recording ${parts.join(' + ')}`;
}

/**
 * Guidance for common browser meeting apps. Voxa cannot see which site the
 * shared tab shows; these are static tips, not detection, and nothing here
 * talks to the meeting provider.
 */
export interface MeetingSiteHint {
  id: string;
  name: string;
  hosts: string;
  tip: string;
}

export const MEETING_SITE_HINTS: MeetingSiteHint[] = [
  { id: 'meet', name: 'Google Meet', hosts: 'meet.google.com',
    tip: 'Join the call in a browser tab, then choose that Google Meet tab in the share dialog with “Share tab audio” on.' },
  { id: 'teams', name: 'Microsoft Teams (web)', hosts: 'teams.microsoft.com, teams.live.com',
    tip: 'Use Teams in the browser (not the desktop app), then choose the Teams tab and share its audio.' },
  { id: 'zoom', name: 'Zoom (web client)', hosts: 'app.zoom.us, zoom.us/wc',
    tip: 'Choose “Join from your browser” so the call runs in a tab, then share that tab with its audio. Calls in the Zoom desktop app cannot be captured here.' },
  { id: 'webex', name: 'Cisco Webex (web)', hosts: 'web.webex.com, *.webex.com',
    tip: 'Join with the Webex web app, then choose the Webex tab and share its audio.' },
  { id: 'whatsapp', name: 'WhatsApp Web', hosts: 'web.whatsapp.com',
    tip: 'Only audio played inside the WhatsApp Web tab can be captured. Calls that open in the WhatsApp desktop or phone app are not in a tab; use “Microphone only” or the Voxa desktop app.' },
];

/** Tips are only useful for browsers that can actually share tab audio. */
export function availableModes(capabilities: BrowserCaptureCapabilities | undefined): CaptureMode[] {
  return capabilities?.modes ?? ['microphone'];
}
