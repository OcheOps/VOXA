'use client';

import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';
import type { CredentialStatus, WorkspaceSettings, WorkspaceSettingsPatch } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { describeError } from './errors';

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

function ToggleField({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 py-1">
      <Label htmlFor={id}>{label}</Label>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

function WorkspaceDefaults() {
  const { workspaceSettings } = useVoxaClient();
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

  return (
    <form onSubmit={save} className="space-y-6" aria-label="Workspace settings">
      <datalist id="provider-suggestions">
        {PROVIDER_SUGGESTIONS.map((provider) => <option key={provider} value={provider} />)}
      </datalist>
      <fieldset className="space-y-3">
        <legend className="text-sm font-semibold text-gray-800">Transcription defaults</legend>
        <TextField id="transcription-provider" label="Provider" value={draft.transcription.provider} onChange={(provider) => setTranscription({ provider })} list="provider-suggestions" />
        <TextField id="transcription-model" label="Model" value={draft.transcription.model} onChange={(model) => setTranscription({ model })} />
        <TextField id="transcription-language" label="Language" value={draft.transcription.language} onChange={(language) => setTranscription({ language })} />
        <ToggleField id="transcription-translate" label="Translate to English" checked={draft.transcription.translateToEnglish} onChange={(translateToEnglish) => setTranscription({ translateToEnglish })} />
      </fieldset>
      <fieldset className="space-y-3">
        <legend className="text-sm font-semibold text-gray-800">Summary defaults</legend>
        <TextField id="summary-provider" label="Provider" value={draft.summary.provider} onChange={(provider) => setSummary({ provider })} list="provider-suggestions" />
        <TextField id="summary-model" label="Model" value={draft.summary.model} onChange={(model) => setSummary({ model })} />
        <TextField id="summary-language" label="Output language" value={draft.summary.outputLanguage} onChange={(outputLanguage) => setSummary({ outputLanguage })} />
        <ToggleField id="summary-auto" label="Generate summaries automatically" checked={draft.summary.autoGenerate} onChange={(autoGenerate) => setSummary({ autoGenerate })} />
      </fieldset>
      <fieldset className="space-y-1">
        <legend className="text-sm font-semibold text-gray-800">Workspace</legend>
        <ToggleField id="retain-media" label="Keep recordings after processing" checked={draft.retainMedia} onChange={(retainMedia) => setDraft({ ...draft, retainMedia })} />
        <ToggleField id="analytics" label="Share anonymous usage analytics" checked={draft.analyticsEnabled} onChange={(analyticsEnabled) => setDraft({ ...draft, analyticsEnabled })} />
        <ToggleField id="processing-complete" label="Notify when processing completes" checked={draft.processingCompleteNotifications} onChange={(processingCompleteNotifications) => setDraft({ ...draft, processingCompleteNotifications })} />
      </fieldset>
      <div className="flex gap-2">
        <Button type="submit" disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save settings'}</Button>
        <Button type="button" variant="ghost" disabled={!dirty || saving} onClick={() => setDraft(saved)}>Discard changes</Button>
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
    <section className="space-y-4" aria-label="Provider API keys">
      <div>
        <h2 className="text-sm font-semibold text-gray-800">Provider API keys</h2>
        <p className="text-xs text-gray-500">Keys are stored encrypted on the Voxa server and can never be read back. Enter a key again to replace it.</p>
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
      <form onSubmit={save} className="grid grid-cols-[1fr_2fr_auto] items-end gap-2" aria-label="Add provider key">
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
    <div className="mx-auto max-w-2xl space-y-10 p-8">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold text-gray-900">Settings</h1>
        <DesktopOnlyNotice />
      </header>
      <WorkspaceDefaults />
      <Credentials />
    </div>
  );
}
