import type { SVGProps } from 'react';

// NavIcon renders a small stroked icon for the console navigation. Icons are
// decorative (aria-hidden) so accessible names come from the link label only.
const PATHS: Record<string, string> = {
  dashboard: 'M4 13h7V4H4v9Zm0 7h7v-4H4v4Zm9 0h7v-9h-7v9Zm0-16v4h7V4h-7Z',
  sessions: 'M4 5h16v11H8l-4 4V5Zm4 4h8M8 12h5',
  memories: 'M12 3a4 4 0 0 1 4 4v1a3 3 0 0 1 0 6v1a4 4 0 0 1-8 0v-1a3 3 0 0 1 0-6V7a4 4 0 0 1 4-4Zm0 0v18',
  candidates: 'M9 11l3 3 8-8M4 12v6a2 2 0 0 0 2 2h12',
  debugger: 'M8 4h8M6 8h12v9a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V8Zm-3 2h3m12 0h3M9 4 7 2m8 2 2-2',
  failures: 'M12 9v4m0 4h.01M10.3 4l-7.4 13A1.5 1.5 0 0 0 4.2 19h15.6a1.5 1.5 0 0 0 1.3-2L13.7 4a1.5 1.5 0 0 0-2.6 0Z',
  evaluation: 'M4 20V10m5 10V4m5 16v-7m5 7V8',
  members: 'M16 19v-2a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v2M9.5 9a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Zm11 10v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Zm7.4-3a7.4 7.4 0 0 0-.1-1.2l2-1.5-2-3.4-2.3.9a7.5 7.5 0 0 0-2-1.2L14.7 3h-4l-.3 2.6a7.5 7.5 0 0 0-2 1.2l-2.3-.9-2 3.4 2 1.5a7.4 7.4 0 0 0 0 2.4l-2 1.5 2 3.4 2.3-.9a7.5 7.5 0 0 0 2 1.2l.3 2.6h4l.3-2.6a7.5 7.5 0 0 0 2-1.2l2.3.9 2-3.4-2-1.5c.1-.4.1-.8.1-1.2Z',
  logout: 'M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 17l-5-5 5-5M5 12h11',
  chevron: 'M6 9l6 6 6-6',
  pulse: 'M3 12h4l2.5-7 4 14 2.5-7H21',
};

export interface NavIconProps extends SVGProps<SVGSVGElement> {
  name: string;
}

export function NavIcon({ name, ...rest }: NavIconProps) {
  const path = PATHS[name] ?? PATHS.dashboard;
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" {...rest}>
      <path d={path} />
    </svg>
  );
}
