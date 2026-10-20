'use client';

import { Suspense, useState } from 'react';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { toast } from 'sonner';
import { LayoutList, LogOut, Menu, Plus, Settings } from 'lucide-react';
import { useVoxaClient } from '@/clients';
import { useSidebar } from '@/components/Sidebar/SidebarProvider';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet';
import { useAuth, useCurrentIdentity, useWebRuntime } from './context';
import { describeError } from './errors';

export function CreateMeetingForm() {
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
      <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="New meeting…" maxLength={200} aria-label="New meeting title" className="bg-white" />
      <Button type="submit" size="icon" disabled={busy || !title.trim()} aria-label="Create meeting" className="shrink-0">
        <Plus className="h-4 w-4" />
      </Button>
    </form>
  );
}

/** Voxa wordmark with a recording-dot mark. */
export function Brand() {
  return (
    <Link href="/" className="flex items-center gap-2 px-2 text-lg font-semibold tracking-tight text-gray-900">
      <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-gray-900" aria-hidden>
        <span className="h-2.5 w-2.5 rounded-full bg-red-500" />
      </span>
      Voxa
    </Link>
  );
}

function PrimaryNav() {
  const pathname = usePathname();
  const item = (href: string, label: string, icon: React.ReactNode, active: boolean) => (
    <Link href={href} className={`flex items-center gap-2 rounded-md px-2 py-1.5 text-sm ${active ? 'bg-white font-medium text-gray-900 shadow-sm ring-1 ring-gray-200' : 'text-gray-700 hover:bg-gray-100'}`}>
      {icon}{label}
    </Link>
  );
  return (
    <nav aria-label="Main" className="space-y-1">
      {item('/', 'Meetings', <LayoutList className="h-4 w-4" />, pathname === '/')}
      {item('/settings', 'Settings', <Settings className="h-4 w-4" />, pathname === '/settings')}
    </nav>
  );
}

function MeetingNav() {
  const { meetings } = useSidebar();
  const pathname = usePathname();
  const selected = useSearchParams().get('id');
  return (
    <nav aria-label="Meetings" className="min-h-0 flex-1 space-y-0.5 overflow-y-auto">
      <p className="px-2 pb-1 text-xs font-medium uppercase tracking-wide text-gray-400">Recent</p>
      {meetings.length === 0 && <p className="px-2 py-2 text-sm text-gray-500">No meetings yet.</p>}
      {meetings.map((meeting) => {
        const active = pathname === '/meeting-details' && selected === meeting.id;
        return (
          <Link
            key={meeting.id}
            href={`/meeting-details?id=${encodeURIComponent(meeting.id)}`}
            className={`block truncate rounded-md px-2 py-1.5 text-sm ${active ? 'bg-white font-medium text-gray-900 shadow-sm ring-1 ring-gray-200' : 'text-gray-600 hover:bg-gray-100'}`}
          >
            {meeting.title}
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

function SidebarContent({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <div className="flex h-full flex-col gap-4 p-3" onClick={(event) => { if ((event.target as HTMLElement).closest('a')) onNavigate?.(); }}>
      <Brand />
      <CreateMeetingForm />
      <PrimaryNav />
      <Suspense fallback={null}>
        <MeetingNav />
      </Suspense>
      <Identity />
    </div>
  );
}

/**
 * Browser layout: a sidebar on wide screens, a top bar with a slide-over
 * menu on phones. No desktop controls are rendered.
 */
export function WebShell({ children }: { children: React.ReactNode }) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div className="flex h-screen bg-white">
      <aside className="hidden w-72 shrink-0 border-r border-gray-200 bg-gray-50 md:block">
        <SidebarContent />
      </aside>
      <Sheet open={menuOpen} onOpenChange={setMenuOpen}>
        <SheetContent side="left" className="w-80 max-w-[85vw] bg-gray-50 p-0">
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <SidebarContent onNavigate={() => setMenuOpen(false)} />
        </SheetContent>
      </Sheet>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-gray-200 px-3 md:hidden">
          <Button variant="ghost" size="icon" onClick={() => setMenuOpen(true)} aria-label="Open menu">
            <Menu className="h-5 w-5" />
          </Button>
          <Brand />
        </header>
        <main className="min-h-0 flex-1 overflow-y-auto">{children}</main>
      </div>
    </div>
  );
}
