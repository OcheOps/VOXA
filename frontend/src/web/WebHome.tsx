'use client';

import Link from 'next/link';
import { useSidebar } from '@/components/Sidebar/SidebarProvider';
import { CapabilityNotice } from './CapabilityNotice';
import { useCurrentIdentity } from './context';

export function WebHome() {
  const { meetings } = useSidebar();
  const identity = useCurrentIdentity();
  return (
    <div className="mx-auto max-w-3xl space-y-6 p-8">
      <header>
        <h1 className="text-2xl font-semibold text-gray-900">Meetings</h1>
        <p className="text-sm text-gray-500">{identity.workspace.name}</p>
      </header>
      <CapabilityNotice />
      {meetings.length === 0 ? (
        <p className="text-sm text-gray-600">No meetings yet. Create one from the sidebar.</p>
      ) : (
        <ul className="divide-y divide-gray-100 rounded-md border border-gray-200">
          {meetings.map((meeting) => (
            <li key={meeting.id}>
              <Link href={`/meeting-details?id=${encodeURIComponent(meeting.id)}`} className="block px-4 py-3 text-sm text-gray-800 hover:bg-gray-50">
                {meeting.title}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
