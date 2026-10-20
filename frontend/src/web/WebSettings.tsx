'use client';

import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';
import type { CredentialStatus, WorkspaceSettings, WorkspaceSettingsPatch } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { useServerCapabilities } from './context';
import { describeError } from './errors';

/** Example models per provider, shown as hints only (any model the provider offers works). */
const MODEL_EXAMPLES: Record<string, string> = {
  openai: 'e.g. gpt-4o-mini', claude: 'e.g. claude-sonnet-5-5', groq: 'e.g. llama-3.3-70b-versatile',
  openrouter: 'e.g. openai/gpt-4o-mini', ollama: 'e.g. llama3.1', 'custom-openai': 'model name on your endpoint',
};

const PROVIDER_NAMES: Record<string, string> = {
  openai: 'OpenAI', claude: 'Anthropic (Claude)', groq: 'Groq', openrouter: 'OpenRouter', ollama: 'Ollama (self-hosted)', 'custom-openai': 'OpenAI-compatible endpoint',
};

const LANGUAGES: Array<[string, string]> = [
  ['auto', 'Detect automatically'], ['en', 'English'], ['fr', 'French'], ['de', 'German'], ['es', 'Spanish'], ['pt', 'Portuguese'],
  ['it', 'Italian'], ['nl', 'Dutch'], ['pl', 'Polish'], ['tr', 'Turkish'], ['ar', 'Arabic'], ['hi', 'Hindi'], ['zh', 'Chinese'],
  ['ja', 'Japanese'], ['ko', 'Korean'], ['ru', 'Russian'], ['uk', 'Ukrainian'], ['sv', 'Swedish'],
];

const PROVIDER_SUGGESTIONS = ['openai', 'claude', 'groq', 'openrouter', 'ollama', 'custom-openai', 'deepgram', 'elevenLabs'];

/** Builds a patch containing only the fields that changed. */
export function settingsPatch(before: WorkspaceSettings, after: WorkspaceSettings): WorkspaceSettingsPatch {
  const patch: WorkspaceSettingsPatch = {};
  const transcription = Object.fromEntries(
    Object.entries(after.transcription).filter(([key, value]) => before.transcription[key as keyof WorkspaceSettings['transcription']] !== value),
  );
  const summary = Object.fromEntries(
    Object.entries(after.summary).filter(([key, value]) => before.summary[key as keyof WorkspaceSettings['summary']] !== value),
  );
  if (Object.keys(transcription).length) patch.transcription = transcription;
  if (Object.keys(summary).length) patch.summary = summary;
  if (before.retainMedia !== after.retainMedia) patch.retainMedia = after.retainMedia;
  if (before.analyticsEnabled !== after.analyticsEnabled) patch.analyticsEnabled = after.analyticsEnabled;
  if (before.processingCompleteNotifications !== after.processingCompleteNotifications) {
    patch.processingCompleteNotifications = after.processingCompleteNotifications;
  }
  return patch;
}

function TextField({ id, label, value, onChange, list }: { id: string; label: string; value: string; onChange: (value: string) => void; list?: string }) {
  return (
    <div className="space-y-1">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} list={list} autoComplete="off" />
    </div>
  );
}

function ToggleField({ id, label, hint, checked, onChange }: { id: string; label: string; hint?: string; checked: boolean; onChange: (value: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 py-1">
      <span>
        <Label htmlFor={id}>{label}</Label>
        {hint && <span className="block text-xs text-gray-500">{hint}</span>}
      </span>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

function SelectField({ id, label, value, options, onChange, hint }: {
  id: string; label: string; value: string; options: Array<[string, string]>; onChange: (value: string) => void; hint?: string;
}) {
  // Keep an unknown stored value selectable instead of silently changing it.
  const all = options.some(([option]) => option === value) ? options : [...options, [value, value] as [string, string]];
  return (
    <div className="space-y-1">
      <Label htmlFor={id}>{label}</Label>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className="h-10 w-full rounded-md border border-gray-300 bg-white px-3 text-sm">
        {all.map(([option, text]) => <option key={option} value={option}>{text}</option>)}
      </select>
      {hint && <p className="text-xs text-gray-500">{hint}</p>}
    </div>
  );
}

function Card({ title, description, children, label }: { title: string; description?: string; children: React.ReactNode; label?: string }) {
  return (
    <section aria-label={label ?? title} className="space-y-4 rounded-xl border border-gray-200 p-4 sm:p-5">
      <div>
        <h2 className="text-base font-semibold text-gray-900">{title}</h2>
        {description && <p className="text-sm text-gray-500">{description}</p>}
      </div>
      {children}
    </section>
  );
}

function WorkspaceDefaults() {
  const { workspaceSettings } = useVoxaClient();
  const server = useServerCapabilities();
  const [saved, setSaved] = useState<WorkspaceSettings | null>(null);
  const [draft, setDraft] = useState<WorkspaceSettings | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    workspaceSettings?.get()
      .then((settings) => { setSaved(settings); setDraft(settings); })
      .catch((error) => toast.error('Could not load settings', { description: describeError(error) }));
  }, [workspaceSettings]);

  if (!workspaceSettings) return null;
  if (!saved || !draft) return <p className="text-sm text-gray-500">Loading settings…</p>;

  const patch = settingsPatch(saved, draft);
  const dirty = Object.keys(patch).length > 0;
  const setTranscription = (change: Partial<WorkspaceSettings['transcription']>) => setDraft({ ...draft, transcription: { ...draft.transcription, ...change } });
  const setSummary = (change: Partial<WorkspaceSettings['summary']>) => setDraft({ ...draft, summary: { ...draft.summary, ...change } });

  const save = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      const updated = await workspaceSettings.update(patch);
      setSaved(updated);
      setDraft(updated);
      toast.success('Settings saved');
    } catch (error) {
      toast.error('Could not save settings', { description: describeError(error) });
    } finally {
      setSaving(false);
    }
  };

  const transcription = server?.transcription;
  const enabled = server?.summary.providers ?? [];
  const providerOptions: Array<[string, string]> = [['none', 'Off'], ...enabled.map((name): [string, string] => [name, PROVIDER_NAMES[name] ?? name])];
  const summaryOff = draft.summary.provider === 'none' || draft.summary.provider === '';

  return (
    <form onSubmit={save} className="space-y-6" aria-label="Workspace settings">
      <Card title="Transcription" description="Recordings are transcribed on this Voxa server after upload.">
        <p className="rounded-lg bg-gray-50 px-3 py-2 text-sm text-gray-700">
          {transcription?.available
            ? <>Engine: <span className="font-medium">{transcription.providers.join(', ')}</span> (chosen by the server operator)</>
            : 'Transcription is not available on this server.'}
        </p>
        <SelectField id="transcription-language" label="Spoken language" value={draft.transcription.language || 'auto'} options={LANGUAGES}
          onChange={(language) => setTranscription({ language })} />
        <ToggleField id="transcription-translate" label="Translate to English" hint="Produce an English transcript from other languages."
          checked={draft.transcription.translateToEnglish} onChange={(translateToEnglish) => setTranscription({ translateToEnglish })} />
      </Card>
      <Card title="Summaries" description="Summaries are written by the AI provider you choose, using your own API key.">
        {enabled.length === 0 ? (
          <p className="rounded-lg bg-gray-50 px-3 py-2 text-sm text-gray-700">No summary providers are enabled on this server. The operator enables them with VOXA_SUMMARY_PROVIDERS.</p>
        ) : (
          <>
            <SelectField id="summary-provider" label="Provider" value={draft.summary.provider || 'none'} options={providerOptions}
              onChange={(provider) => setSummary({ provider })}
              hint={summaryOff ? undefined : `Add an API key for ${PROVIDER_NAMES[draft.summary.provider] ?? draft.summary.provider} under API keys below, if it needs one.`} />
            {!summaryOff && (
              <div className="space-y-1">
                <Label htmlFor="summary-model">Model</Label>
                <Input id="summary-model" value={draft.summary.model} onChange={(e) => setSummary({ model: e.target.value })} placeholder={MODEL_EXAMPLES[draft.summary.provider] ?? 'model name'} autoComplete="off" />
              </div>
            )}
          </>
        )}
        <SelectField id="summary-language" label="Summary language" value={draft.summary.outputLanguage || 'auto'} options={[['auto', 'Same as the meeting'], ...LANGUAGES.slice(1)]}
          onChange={(outputLanguage) => setSummary({ outputLanguage })} />
        <ToggleField id="summary-auto" label="Summarize automatically" hint="Generate a summary as soon as a transcript is ready."
          checked={draft.summary.autoGenerate} onChange={(autoGenerate) => setSummary({ autoGenerate })} />
      </Card>
      <Card title="Workspace">
        <ToggleField id="retain-media" label="Keep recordings after processing" checked={draft.retainMedia} onChange={(retainMedia) => setDraft({ ...draft, retainMedia })} />
        <ToggleField id="analytics" label="Share anonymous usage analytics" checked={draft.analyticsEnabled} onChange={(analyticsEnabled) => setDraft({ ...draft, analyticsEnabled })} />
        <ToggleField id="processing-complete" label="Notify when processing completes" checked={draft.processingCompleteNotifications} onChange={(processingCompleteNotifications) => setDraft({ ...draft, processingCompleteNotifications })} />
      </Card>
      <div className={`sticky bottom-0 -mx-4 flex gap-2 border-t border-gray-200 bg-white/95 px-4 py-3 backdrop-blur sm:mx-0 sm:rounded-xl sm:border ${dirty ? '' : 'hidden'}`}>
        <span className="mr-auto self-center text-sm text-gray-600">You have unsaved changes.</span>
        <Button type="button" variant="ghost" disabled={saving} onClick={() => setDraft(saved)}>Discard</Button>
        <Button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save settings'}</Button>
      </div>
    </form>
  );
}

function Credentials() {
  const { credentials } = useVoxaClient();
  const [statuses, setStatuses] = useState<CredentialStatus[] | null>(null);
  const [provider, setProvider] = useState('');
  const [secret, setSecret] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    if (!credentials) return;
    try {
      setStatuses(await credentials.list());
    } catch (error) {
      toast.error('Could not load credentials', { description: describeError(error) });
    }
  }, [credentials]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!credentials) return null;

  const save = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!provider.trim() || !secret.trim()) return;
    setBusy(true);
    try {
      await credentials.set(provider.trim(), secret);
      toast.success(`Saved the ${provider.trim()} key`);
      setProvider('');
      await refresh();
    } catch (error) {
      toast.error('Could not save the key', { description: describeError(error) });
    } finally {
      // The secret never outlives the request: it is not kept in state or storage.
      setSecret('');
      setBusy(false);
    }
  };

  const remove = async (name: string) => {
    try {
      await credentials.remove(name);
      toast.success(`Removed the ${name} key`);
      await refresh();
    } catch (error) {
      toast.error('Could not remove the key', { description: describeError(error) });
    }
  };

  return (
    <section className="space-y-4 rounded-xl border border-gray-200 p-4 sm:p-5" aria-label="Provider API keys">
      <div>
        <h2 className="text-base font-semibold text-gray-900">Provider API keys</h2>
        <p className="text-sm text-gray-500">Keys are stored encrypted on the Voxa server and can never be read back. Enter a key again to replace it.</p>
      </div>
      {statuses === null ? (
        <p className="text-sm text-gray-500">Loading…</p>
      ) : statuses.length === 0 ? (
        <p className="text-sm text-gray-500">No keys configured.</p>
      ) : (
        <ul className="divide-y divide-gray-100 rounded-md border border-gray-200">
          {statuses.map((status) => (
            <li key={status.provider} className="flex items-center justify-between px-3 py-2 text-sm">
              <span>
                <span className="font-medium text-gray-800">{status.provider}</span>{' '}
                <span className="text-gray-500">{status.configured ? 'configured ••••' : 'not configured'}</span>
              </span>
              <span className="flex gap-1">
                <Button variant="ghost" size="sm" onClick={() => setProvider(status.provider)}>Replace</Button>
                <Button variant="ghost" size="sm" onClick={() => void remove(status.provider)}>Remove</Button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <datalist id="provider-suggestions">
        {PROVIDER_SUGGESTIONS.map((name) => <option key={name} value={name} />)}
      </datalist>
      <form onSubmit={save} className="grid items-end gap-2 sm:grid-cols-[1fr_2fr_auto]" aria-label="Add provider key">
        <div className="space-y-1">
          <Label htmlFor="credential-provider">Provider</Label>
          <Input id="credential-provider" value={provider} onChange={(e) => setProvider(e.target.value)} list="provider-suggestions" autoComplete="off" />
        </div>
        <div className="space-y-1">
          <Label htmlFor="credential-secret">API key</Label>
          <Input id="credential-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="new-password" spellCheck={false} />
        </div>
        <Button type="submit" disabled={busy || !provider.trim() || !secret.trim()}>Save key</Button>
      </form>
    </section>
  );
}

function DesktopOnlyNotice() {
  const { configuration } = useVoxaClient();
  const desktopOnly = [
    !configuration.capabilities.desktopStorageLocations && 'recordings folder',
    !configuration.capabilities.recordingDeviceDefaults && 'audio devices',
    !configuration.capabilities.nativeNotificationPreferences && 'system notifications',
    'local models',
  ].filter(Boolean);
  return <p className="text-xs text-gray-500">Desktop-only settings ({desktopOnly.join(', ')}) are managed in the Voxa desktop app.</p>;
}

export function WebSettings() {
  return (
    <div className="mx-auto max-w-2xl space-y-6 px-4 py-6 sm:px-8 sm:py-10">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight text-gray-900">Settings</h1>
        <DesktopOnlyNotice />
      </header>
      <WorkspaceDefaults />
      <Credentials />
    </div>
  );
}
