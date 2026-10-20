'use client';

import { useCallback, useEffect, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';
import type { MeetingSummaryView } from '@/clients/contracts';
import { Button } from '@/components/ui/button';
import { describeError } from './errors';

/** Seconds between status checks while a summary job is active. */
export const SUMMARY_POLL_MS = 3000;

const ACTIVE = new Set(['queued', 'generating']);

const REASONS: Record<string, string> = {
  disabled: 'Summaries are not available on this server.',
  not_configured: 'Choose a summary provider and model in Settings to generate summaries.',
  provider_not_enabled: 'The summary provider selected in Settings is not enabled on this server.',
  model_missing: 'Choose a summary model in Settings.',
  credential_missing: 'Add an API key for the summary provider in Settings.',
  no_transcript: 'A summary can be generated once a transcript is ready.',
};

/** Summary Markdown. Raw HTML in provider output is never rendered. */
function SummaryMarkdown({ markdown }: { markdown: string }) {
  return (
    <div className="prose prose-sm max-w-none text-gray-800 [&_table]:w-full [&_td]:border [&_td]:px-2 [&_th]:border [&_th]:px-2" data-testid="summary-markdown">
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml>{markdown}</ReactMarkdown>
    </div>
  );
}

/**
 * Server-generated meeting summary: state, generate/regenerate/cancel, and
 * the result with its provenance. Polls only while a job is active. A
 * previous summary stays visible while a regeneration runs or after it fails.
 */
export function SummaryPanel({ meetingId }: { meetingId: string }) {
  const { serverSummaries } = useVoxaClient();
  const [view, setView] = useState<MeetingSummaryView | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!serverSummaries) return;
    try {
      setView(await serverSummaries.getMeetingSummary(meetingId, signal));
    } catch (error) {
      if ((error as DOMException)?.name !== 'AbortError') setView((current) => current);
    }
  }, [meetingId, serverSummaries]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  // Poll while active; also every few polls while waiting for a transcript.
  useEffect(() => {
    if (!view || !(ACTIVE.has(view.state) || view.reason === 'no_transcript')) return;
    const timer = setInterval(() => void load(), view.reason === 'no_transcript' ? SUMMARY_POLL_MS * 3 : SUMMARY_POLL_MS);
    return () => clearInterval(timer);
  }, [view, load]);

  if (!serverSummaries || !view) return null;

  const run = async (action: () => Promise<unknown>, failure: string) => {
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
  const generate = () => run(() => serverSummaries.generateSummary(meetingId), 'Could not start the summary');
  const cancel = () => run(() => serverSummaries.cancelSummary(meetingId), 'Could not cancel the summary');
  const blocked = view.reason !== null && view.reason !== 'no_transcript' ? REASONS[view.reason] ?? 'Summaries cannot be generated right now.' : null;
  const canGenerate = view.reason === null;
  const summary = view.summary;

  let status: React.ReactNode = null;
  switch (view.state) {
    case 'queued':
    case 'generating':
      status = (
        <div className="flex items-center gap-2 text-sm text-gray-600" role="status">
          <span>{view.state === 'queued' ? 'Summary queued…' : `Generating summary…${view.progress !== null ? ` ${Math.round(view.progress * 100)}%` : ''}`}</span>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void cancel()}>Cancel</Button>
        </div>
      );
      break;
    case 'failed':
    case 'cancelled':
      status = (
        <div className="flex flex-wrap items-center gap-2 text-sm" role="status">
          <span className={view.state === 'failed' ? 'text-red-700' : 'text-gray-600'}>
            {view.state === 'failed' ? view.error?.message ?? 'Summary generation failed.' : 'Summary generation was cancelled.'}
            {summary ? ' Showing the previous summary.' : ''}
          </span>
          {canGenerate && <Button size="sm" variant="outline" disabled={busy} onClick={() => void generate()}>{view.state === 'failed' ? 'Retry' : 'Generate summary'}</Button>}
        </div>
      );
      break;
    case 'unavailable':
      status = <p className="text-sm text-gray-500">{REASONS[view.reason ?? 'disabled'] ?? REASONS.disabled}</p>;
      break;
    case 'none':
      status = view.reason === 'no_transcript' || blocked
        ? <p className="text-sm text-gray-500">{REASONS[view.reason ?? ''] ?? blocked}</p>
        : <Button size="sm" variant="outline" disabled={busy} onClick={() => void generate()}>Generate summary</Button>;
      break;
  }

  return (
    <div className="space-y-3">
      {status}
      {summary && (
        <article className="space-y-2 rounded-md border border-gray-200 p-3">
          {view.stale && (
            <p className="rounded bg-amber-50 px-2 py-1 text-xs text-amber-800" role="note">
              This summary was generated from an earlier transcript and may not match the current one.
            </p>
          )}
          {summary.title && <h3 className="text-base font-semibold text-gray-900">{summary.title}</h3>}
          <SummaryMarkdown markdown={summary.markdown} />
          <footer className="flex flex-wrap items-center gap-2 text-xs text-gray-500">
            <span>
              Generated by {summary.provider ?? 'the server'}{summary.model ? ` (${summary.model})` : ''}
              {summary.templateId ? ` · ${summary.templateId.replace(/_/g, ' ')}` : ''}. AI summaries can be inaccurate; check important details against the transcript.
            </span>
            {view.state === 'ready' && canGenerate && (
              <button type="button" className="underline" disabled={busy} onClick={() => void generate()}>Regenerate</button>
            )}
          </footer>
        </article>
      )}
      {blocked && view.state !== 'unavailable' && view.state !== 'none' && <p className="text-xs text-gray-500">{blocked}</p>}
    </div>
  );
}
