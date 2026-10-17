import Link from "next/link";
export function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <Link href="/" className="brand" aria-label="Heartbeat Checker — главная">
      <span className="brand-mark" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none">
          <path
            d="M3 12h4l2.5-6 5 12 2.5-6h4"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
      <span>
        Heartbeat{!compact && <span className="brand-light"> Checker</span>}
      </span>
    </Link>
  );
}
export function Arrow() {
  return <span aria-hidden="true">↗</span>;
}
export function Notice({
  children,
  success = false,
}: {
  children: React.ReactNode;
  success?: boolean;
}) {
  return (
    <div
      className={`notice ${success ? "success" : ""}`}
      role={success ? "status" : "alert"}
    >
      {children}
    </div>
  );
}
export function Spinner() {
  return <span className="spinner" aria-hidden="true" />;
}
export function Loading({ text = "Загрузка…" }: { text?: string }) {
  return (
    <div className="loading" role="status">
      <Spinner />
      {text}
    </div>
  );
}
export function dateTime(value: string | null | undefined) {
  return value
    ? new Intl.DateTimeFormat("ru", {
        day: "2-digit",
        month: "short",
        hour: "2-digit",
        minute: "2-digit",
      }).format(new Date(value))
    : "—";
}
