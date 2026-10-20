'use client';

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { toast } from 'sonner';
import { Pencil, Trash2 } from 'lucide-react';
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
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
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
    <div className="mx-auto max-w-3xl space-y-6 p-8">
      <header className="space-y-2">
        {editing ? (
          <form onSubmit={rename} className="flex gap-2" aria-label="Rename meeting">
            <Input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={200} autoFocus aria-label="Meeting title" />
            <Button type="submit" disabled={!title.trim()}>Save</Button>
            <Button type="button" variant="ghost" onClick={() => { setEditing(false); setTitle(meeting.title); }}>Cancel</Button>
          </form>
        ) : (
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-semibold text-gray-900">{meeting.title}</h1>
            <Button variant="ghost" size="icon" onClick={() => setEditing(true)} aria-label="Rename meeting">
              <Pencil className="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" onClick={() => setConfirmDelete(true)} aria-label="Delete meeting">
              <Trash2 className="h-4 w-4 text-red-600" />
            </Button>
          </div>
        )}
        <p className="text-xs text-gray-500">Created {formatDate(meeting.created_at)} · Updated {formatDate(meeting.updated_at)}</p>
      </header>

      <section className="space-y-3">
        <h2 className="text-sm font-semibold text-gray-700">Recordings and transcripts</h2>
        <RecordingPanel meetingId={meeting.id} />
        <MeetingMedia meetingId={meeting.id} />
      </section>
      <section className="space-y-3">
        <h2 className="text-sm font-semibold text-gray-700">Summary</h2>
        <SummaryPanel meetingId={meeting.id} />
      </section>
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
