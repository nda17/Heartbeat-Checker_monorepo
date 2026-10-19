import type { ReactNode } from "react";

const shapes = {
  loader: <path d="M21 12a9 9 0 1 1-9-9" />,
  activity: <path d="M3 12h4l2.5-6 5 12 2.5-6h4" />,
  payment: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="M3 10h18M7 15h3" />
    </>
  ),
  settings: (
    <>
      <path d="M9.6 2.8h4.8l.4 2.4 1.6.96 2.24-.8 2.4 4.16-1.84 1.52v1.92l1.84 1.52-2.4 4.16-2.24-.8-1.6.96-.4 2.4H9.6l-.4-2.4-1.6-.96-2.24.8-2.4-4.16 1.84-1.52v-1.92L2.96 9.52l2.4-4.16 2.24.8 1.6-.96.4-2.4Z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  shield: (
    <>
      <path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z" />
      <path d="m8 12 3 3 5-6" />
    </>
  ),
  logout: <path d="M9 4H4v16h5M9 12h12m-5-5 5 5-5 5" />,
  pause: (
    <>
      <rect x="6" y="4" width="4" height="16" rx="1" />
      <rect x="14" y="4" width="4" height="16" rx="1" />
    </>
  ),
  play: <path d="m8 4 12 8-12 8V4Z" />,
  bell: <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />,
  trash: <path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7" />,
  plus: <path d="M12 4v16M4 12h16" />,
  refresh: (
    <>
      <path d="M20 7v5h-5M4 17v-5h5" />
      <path d="M6 6a8 8 0 0 1 13 2l1 4M4 12l1 4a8 8 0 0 0 13 2" />
    </>
  ),
  close: <path d="m6 6 12 12M6 18 18 6" />,
  info: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v6M12 7v.01" />
    </>
  ),
  device: (
    <>
      <rect x="3" y="4" width="18" height="13" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </>
  ),
  check: <path d="m5 12 4 4L19 6" />,
  alert: <path d="m12 3 10 18H2L12 3ZM12 9v5M12 17v.01" />,
  dot: <circle cx="12" cy="12" r="2" fill="currentColor" stroke="none" />,
  moon: <path d="M20 14a8.5 8.5 0 0 1-10-10 8.5 8.5 0 1 0 10 10Z" />,
  sun: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5" />
    </>
  ),
  eye: (
    <>
      <path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z" />
      <circle cx="12" cy="12" r="3" />
    </>
  ),
  "eye-off": (
    <path d="M9 6.4c1-.3 2-.4 3-.4 6.5 0 10 6 10 6a20 20 0 0 1-3.2 3.6M6 8a20 20 0 0 0-4 4s3.5 6 10 6c1.8 0 3.4-.5 4.8-1.2M10 10a3 3 0 0 0 4 4M3 3l18 18" />
  ),
  mail: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="m3 6 9 7 9-7" />
    </>
  ),
  "arrow-up-right": <path d="M6 18 18 6M6 6h12v12" />,
  "arrow-right": <path d="M4 12h16m-6-6 6 6-6 6" />,
  "arrow-left": <path d="M20 12H4m6-6-6 6 6 6" />,
  "arrow-down": <path d="M12 4v16m-6-6 6 6 6-6" />,
  "chevron-down": <path d="m6 9 6 6 6-6" />,
  globe: (
    <>
      <circle cx="12" cy="12" r="9" />
      <ellipse cx="12" cy="12" rx="4" ry="9" />
      <path d="M3 12h18" />
    </>
  ),
  network: <path d="M3 8h18m-4-4 4 4-4 4M21 16H3m4-4-4 4 4 4" />,
  radar: (
    <>
      <circle cx="12" cy="12" r="9" />
      <circle cx="12" cy="12" r="5" />
      <path d="m12 12 6-6" />
    </>
  ),
  user: (
    <>
      <circle cx="12" cy="8" r="4" />
      <path d="M4 21v-2a8 8 0 0 1 16 0v2" />
    </>
  ),
} satisfies Record<string, ReactNode>;

export function Icon({
  name,
  size = 20,
  className = "",
}: {
  name: keyof typeof shapes;
  size?: number;
  className?: string;
}) {
  return (
    <svg
      className={`ui-icon${name.startsWith("arrow-") ? " ui-icon-arrow" : ""} ${className}`}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {shapes[name]}
    </svg>
  );
}
