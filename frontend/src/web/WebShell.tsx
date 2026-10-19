'use client';

import { Suspense, useState } from 'react';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { toast } from 'sonner';
import { LogOut, Plus, Settings, StickyNote } from 'lucide-react';
import { useVoxaClient } from '@/clients';
import { useSidebar } from '@/components/Sidebar/SidebarProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { useAuth, useCurrentIdentity, useWebRuntime } from './context';
import { describeError } from './errors';

function CreateMeetingForm() {
  const { meetings } = useVoxaClient();
  const { refetchMeetings } = useSidebar();
  const router = useRouter();
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const clean = title.trim();
    if (!clean) return;
    setBusy(true);
    try {
      const { meeting_id } = await meetings.create({ title: clean, transcripts: [], folderPath: null });
      setTitle('');
      await refetchMeetings();
      router.push(`/meeting-details?id=${encodeURIComponent(meeting_id)}`);
    } catch (error) {
      toast.error('Could not create the meeting', { description: describeError(error) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex gap-2" aria-label="Create meeting">
      <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="New meeting title" maxLength={200} aria-label="New meeting title" />
      <Button type="submit" size="icon" disabled={busy || !title.trim()} aria-label="Create meeting">
        <Plus className="h-4 w-4" />
      </Button>
    </form>
  );
}

function MeetingNav() {
  const { meetings } = useSidebar();
  const pathname = usePathname();
  const selected = useSearchParams().get('id');
  return (
    <nav aria-label="Meetings" className="flex-1 space-y-1 overflow-y-auto">
      {meetings.length === 0 && <p className="px-2 py-4 text-sm text-gray-500">No meetings yet.</p>}
      {meetings.map((meeting) => {
        const active = pathname === '/meeting-details' && selected === meeting.id;
        return (
          <Link
            key={meeting.id}
            href={`/meeting-details?id=${encodeURIComponent(meeting.id)}`}
            className={`flex items-center gap-2 truncate rounded-md px-2 py-1.5 text-sm ${active ? 'bg-gray-100 font-medium text-gray-900' : 'text-gray-700 hover:bg-gray-50'}`}
          >
            <StickyNote className="h-4 w-4 shrink-0 text-gray-400" />
            <span className="truncate">{meeting.title}</span>
          </Link>
        );
      })}
    </nav>
  );
}

function Identity() {
  const identity = useCurrentIdentity();
  const { controller } = useAuth();
  const { config } = useWebRuntime();
  const name = identity.user.displayName ?? identity.user.email ?? 'Signed in';
  return (
    <div className="space-y-2 border-t border-gray-200 pt-3 text-sm">
      <div>
        <p className="truncate font-medium text-gray-900">{name}</p>
        <p className="truncate text-xs text-gray-500">
          {identity.workspace.name} · {identity.role}
        </p>
      </div>
      {config.auth.mode === 'oidc' ? (
        <Button variant="ghost" size="sm" className="w-full justify-start" onClick={() => controller.signOut()}>
          <LogOut className="mr-2 h-4 w-4" /> Sign out
        </Button>
      ) : (
        <p className="text-xs text-amber-700">Development authentication</p>
      )}
    </div>
  );
}

/** Browser layout: meeting navigation, account and settings. No desktop controls are rendered. */
export function WebShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-screen bg-white">
      <aside className="flex w-72 shrink-0 flex-col gap-3 border-r border-gray-200 bg-gray-50 p-3">
        <Link href="/" className="px-2 text-lg font-semibold text-gray-900">Voxa</Link>
        <CreateMeetingForm />
        <Suspense fallback={null}>
          <MeetingNav />
        </Suspense>
        <Link href="/settings" className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-gray-700 hover:bg-gray-100">
          <Settings className="h-4 w-4" /> Settings
        </Link>
        <Identity />
      </aside>
      <main className="flex-1 overflow-y-auto">{children}</main>
    </div>
  );
}
