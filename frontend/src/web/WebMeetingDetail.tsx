'use client';

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { toast } from 'sonner';
import { MoreHorizontal, Pencil, Trash2 } from 'lucide-react';
import { useVoxaClient } from '@/clients';
import { VoxaApiError } from '@/clients/http/transport';
import { useSidebar } from '@/components/Sidebar/SidebarProvider';
import { ConfirmationModal } from '@/components/ConfirmationModel/confirmation-modal';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import type { MeetingMetadata } from '@/types';
import { CapabilityNotice } from './CapabilityNotice';
import { MeetingMedia } from './recording/MeetingMedia';
import { RecordingPanel } from './recording/RecordingPanel';
import { SummaryPanel } from './SummaryPanel';
import { describeError } from './errors';

function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit" });
}

/** Small actions menu (rename, delete) for the meeting header. */
function ActionsMenu({ onRename, onDelete }: { onRename: () => void; onDelete: () => void }) {
  const [open, setOpen] = useState(false);
  const choose = (action: () => void) => () => { setOpen(false); action(); };
  return (
    <div className="relative" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false); }}>
      <Button variant="ghost" size="icon" aria-label="Meeting actions" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <MoreHorizontal className="h-5 w-5" />
      </Button>
      {open && (
        <div role="menu" className="absolute right-0 z-20 mt-1 w-44 overflow-hidden rounded-md border border-gray-200 bg-white py-1 text-sm shadow-lg">
          <button type="button" role="menuitem" aria-label="Rename meeting" onClick={choose(onRename)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-gray-800 hover:bg-gray-50">
            <Pencil className="h-4 w-4" />Rename
          </button>
          <button type="button" role="menuitem" aria-label="Delete meeting" onClick={choose(onDelete)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-red-600 hover:bg-red-50">
            <Trash2 className="h-4 w-4" />Delete meeting
          </button>
        </div>
      )}
    </div>
  );
}

function MeetingDetail() {
  const meetingId = useSearchParams().get('id');
  const client = useVoxaClient();
  const { refetchMeetings } = useSidebar();
  const router = useRouter();
  const [meeting, setMeeting] = useState<MeetingMetadata | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'missing' | 'error'>('loading');
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [tab, setTab] = useState<'summary' | 'transcript'>('summary');

  const load = useCallback(async () => {
    if (!meetingId) {
      setStatus('missing');
      return;
    }
    setStatus('loading');
    try {
      const metadata = await client.meetings.getMetadata(meetingId);
      setMeeting(metadata);
      setTitle(metadata.title);
      setStatus('ready');
    } catch (error) {
      setStatus(error instanceof VoxaApiError && error.status === 404 ? 'missing' : 'error');
    }
  }, [client.meetings, meetingId]);

  useEffect(() => {
    void load();
  }, [load]);

  const rename = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!meeting || !title.trim()) return;
    try {
      await client.meetings.rename(meeting.id, title.trim());
      setEditing(false);
      await Promise.all([load(), refetchMeetings()]);
      toast.success('Meeting renamed');
    } catch (error) {
      toast.error('Could not rename the meeting', { description: describeError(error) });
    }
  };

  const remove = async () => {
    if (!meeting) return;
    setConfirmDelete(false);
    try {
      await client.meetings.delete(meeting.id);
      await refetchMeetings();
      toast.success('Meeting deleted');
      router.push('/');
    } catch (error) {
      toast.error('Could not delete the meeting', { description: describeError(error) });
    }
  };

  if (status === 'loading') return <p className="p-8 text-sm text-gray-500">Loading meeting…</p>;
  if (status === 'missing') return <p className="p-8 text-sm text-gray-600">This meeting does not exist or you do not have access to it.</p>;
  if (status === 'error' || !meeting) {
    return (
      <div className="space-y-3 p-8 text-sm text-gray-600">
        <p>The meeting could not be loaded.</p>
        <Button variant="outline" onClick={() => void load()}>Try again</Button>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6 px-4 py-6 sm:px-8 sm:py-10">
      <header className="flex items-start gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          {editing ? (
            <form onSubmit={rename} className="flex gap-2" aria-label="Rename meeting">
              <Input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={200} autoFocus aria-label="Meeting title" />
              <Button type="submit" disabled={!title.trim()}>Save</Button>
              <Button type="button" variant="ghost" onClick={() => { setEditing(false); setTitle(meeting.title); }}>Cancel</Button>
            </form>
          ) : (
            <h1 className="break-words text-2xl font-semibold tracking-tight text-gray-900">{meeting.title}</h1>
          )}
          <p className="text-sm text-gray-500">{formatDate(meeting.created_at)}</p>
        </div>
        {!editing && (
          <ActionsMenu onRename={() => setEditing(true)} onDelete={() => setConfirmDelete(true)} />
        )}
      </header>

      <section aria-label="Record" className="rounded-xl border border-gray-200 bg-gray-50/60 p-4 sm:p-5">
        <RecordingPanel meetingId={meeting.id} />
      </section>

      <div>
        <div role="tablist" aria-label="Meeting content" className="flex gap-1 border-b border-gray-200">
          {([['summary', 'Summary'], ['transcript', 'Transcript & recordings']] as const).map(([value, label]) => (
            <button
              key={value}
              type="button"
              role="tab"
              id={`tab-${value}`}
              aria-selected={tab === value}
              aria-controls={`panel-${value}`}
              onClick={() => setTab(value)}
              className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${tab === value ? 'border-gray-900 text-gray-900' : 'border-transparent text-gray-500 hover:text-gray-800'}`}
            >
              {label}
            </button>
          ))}
        </div>
        {/* Both panels stay mounted so polling and playback state survive switching tabs. */}
        <div role="tabpanel" id="panel-summary" aria-labelledby="tab-summary" hidden={tab !== 'summary'} className="pt-4">
          <SummaryPanel meetingId={meeting.id} />
        </div>
        <div role="tabpanel" id="panel-transcript" aria-labelledby="tab-transcript" hidden={tab !== 'transcript'} className="pt-4">
          <MeetingMedia meetingId={meeting.id} />
        </div>
      </div>
      <CapabilityNotice />

      <ConfirmationModal
        isOpen={confirmDelete}
        text={`Delete "${meeting.title}"? This cannot be undone.`}
        onConfirm={() => void remove()}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  );
}

export function WebMeetingDetail() {
  return (
    <Suspense fallback={<p className="p-8 text-sm text-gray-500">Loading meeting…</p>}>
      <MeetingDetail />
    </Suspense>
  );
}
