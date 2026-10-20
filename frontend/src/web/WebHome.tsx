'use client';

import { useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { ChevronRight, MonitorUp, Mic, Sparkles } from 'lucide-react';
import { useVoxaClient } from '@/clients';
import type { MeetingListItem } from '@/clients/contracts';
import { useSidebar } from '@/components/Sidebar/SidebarProvider';
import { CapabilityNotice } from './CapabilityNotice';
import { useCurrentIdentity } from './context';
import { CreateMeetingForm } from './WebShell';

const DAY = 24 * 60 * 60 * 1000;

/** Groups meetings by when they were created: Today, Yesterday, This week, then month. */
export function groupMeetings(items: MeetingListItem[], now = new Date()): Array<{ label: string; items: MeetingListItem[] }> {
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const groups = new Map<string, MeetingListItem[]>();
  const sorted = [...items].sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
  for (const item of sorted) {
    const created = item.createdAt ? new Date(item.createdAt) : null;
    let label = 'Earlier';
    if (created && !Number.isNaN(created.getTime())) {
      const time = created.getTime();
      if (time >= startOfToday) label = 'Today';
      else if (time >= startOfToday - DAY) label = 'Yesterday';
      else if (time >= startOfToday - 6 * DAY) label = 'This week';
      else label = created.toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
    }
    if (!groups.has(label)) groups.set(label, []);
    groups.get(label)!.push(item);
  }
  return [...groups.entries()].map(([label, groupItems]) => ({ label, items: groupItems }));
}

function meetingTime(item: MeetingListItem): string {
  if (!item.createdAt) return '';
  const date = new Date(item.createdAt);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}

function EmptyState() {
  const steps = [
    { icon: <Mic className="h-4 w-4" />, title: 'Create a meeting', text: 'Give it a name above. Each meeting holds its recordings, transcript and summary.' },
    { icon: <MonitorUp className="h-4 w-4" />, title: 'Record the call', text: 'Choose “Meeting tab + microphone” and pick the tab your Meet, Teams or Zoom call runs in. No bot joins.' },
    { icon: <Sparkles className="h-4 w-4" />, title: 'Get the transcript and summary', text: 'Voxa transcribes on your server and summarizes with the provider you choose.' },
  ];
  return (
    <ol className="grid gap-3 sm:grid-cols-3">
      {steps.map((step, index) => (
        <li key={step.title} className="rounded-xl border border-gray-200 p-4">
          <div className="mb-2 flex items-center gap-2 text-sm font-medium text-gray-900">
            <span className="flex h-6 w-6 items-center justify-center rounded-full bg-gray-100 text-gray-700">{step.icon}</span>
            {index + 1}. {step.title}
          </div>
          <p className="text-sm text-gray-600">{step.text}</p>
        </li>
      ))}
    </ol>
  );
}

export function WebHome() {
  const { meetings: client } = useVoxaClient();
  const { meetings: sidebarMeetings } = useSidebar();
  const identity = useCurrentIdentity();
  const [items, setItems] = useState<MeetingListItem[] | null>(null);
  // Reload when the sidebar's list changes (create, rename, delete).
  const signature = sidebarMeetings.map((m) => `${m.id}:${m.title}`).join('|');

  useEffect(() => {
    let active = true;
    client.list().then((list) => { if (active) setItems(list); }).catch(() => { if (active) setItems(sidebarMeetings); });
    return () => { active = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, signature]);

  const groups = useMemo(() => groupMeetings(items ?? []), [items]);

  return (
    <div className="mx-auto max-w-4xl space-y-8 px-4 py-6 sm:px-8 sm:py-10">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-gray-900">Meetings</h1>
          <p className="text-sm text-gray-500">{identity.workspace.name}</p>
        </div>
        {/* On wide screens the sidebar has the same field. */}
        <div className="w-full md:hidden"><CreateMeetingForm /></div>
      </header>
      <CapabilityNotice />
      {items === null ? (
        <p className="text-sm text-gray-500">Loading meetings…</p>
      ) : items.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="space-y-6">
          {groups.map((group) => (
            <section key={group.label} aria-label={group.label} className="space-y-2">
              <h2 className="text-xs font-medium uppercase tracking-wide text-gray-400">{group.label}</h2>
              <ul className="divide-y divide-gray-100 overflow-hidden rounded-xl border border-gray-200">
                {group.items.map((meeting) => (
                  <li key={meeting.id}>
                    <Link href={`/meeting-details?id=${encodeURIComponent(meeting.id)}`} className="flex items-center gap-3 px-4 py-3 hover:bg-gray-50">
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium text-gray-900">{meeting.title}</span>
                        {meetingTime(meeting) && <span className="block text-xs text-gray-500">{meetingTime(meeting)}</span>}
                      </span>
                      <ChevronRight className="h-4 w-4 shrink-0 text-gray-300" aria-hidden />
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}
