import type { BrowserCaptureCapabilities, CaptureDescription, CaptureMode } from '@/clients/browser/recording-client';
import type { RecordingCaptureProvenance } from '@/clients/contracts';

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
    return 'Your browser may not provide shared tab audio. Voxa will verify after you choose a source.';
  }
  return null;
}

const PROVENANCE_LABELS: Record<string, string> = {
  microphone: 'Microphone',
  microphone_and_shared_audio: 'Meeting tab + microphone',
  shared_audio: 'Meeting tab',
};

/**
 * Short label for how a stored recording was captured; null when unknown
 * (recordings from before provenance existed). Window/screen shares are
 * named as such.
 */
export function provenanceLabel(capture: RecordingCaptureProvenance | null | undefined): string | null {
  if (!capture || capture.mode === 'unknown') return null;
  let label = PROVENANCE_LABELS[capture.mode] ?? null;
  if (label && capture.sharedSurface && capture.sharedSurface !== 'browser' && capture.sharedSurface !== 'unknown') {
    label = label.replace('Meeting tab', capture.sharedSurface === 'window' ? 'Shared window' : 'Shared screen');
  }
  return label;
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
 * talks to the meeting provider. They describe only the tab-sharing step
 * that Voxa has verified (tab audio capture in Chromium); live calls in each
 * app have not been validated yet (docs/browser-capture-compatibility.md).
 */
export interface MeetingSiteHint {
  id: string;
  name: string;
  hosts: string;
  tip: string;
}

const tabTip = (app: string) => `Join the call in a browser tab, then choose the ${app} tab in the share dialog and turn on “Share tab audio”.`;

export const MEETING_SITE_HINTS: MeetingSiteHint[] = [
  { id: 'meet', name: 'Google Meet', hosts: 'meet.google.com', tip: tabTip('Google Meet') },
  { id: 'teams', name: 'Microsoft Teams (web)', hosts: 'teams.microsoft.com, teams.live.com', tip: tabTip('Teams') },
  { id: 'zoom', name: 'Zoom (web client)', hosts: 'app.zoom.us', tip: tabTip('Zoom') },
  { id: 'webex', name: 'Cisco Webex (web)', hosts: 'web.webex.com', tip: tabTip('Webex') },
  { id: 'whatsapp', name: 'WhatsApp Web', hosts: 'web.whatsapp.com', tip: tabTip('WhatsApp Web') },
];

/** Shown with the tips: what they are based on. */
export const MEETING_SITE_HINTS_NOTE = 'Only audio playing inside the shared tab is recorded. Calls in desktop or phone apps are not in a tab and cannot be captured here. Voxa has not yet been validated with live calls in each app.';

/** Tips are only useful for browsers that can actually share tab audio. */
export function availableModes(capabilities: BrowserCaptureCapabilities | undefined): CaptureMode[] {
  return capabilities?.modes ?? ['microphone'];
}
