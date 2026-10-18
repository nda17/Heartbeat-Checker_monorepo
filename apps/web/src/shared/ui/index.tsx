import Link from "next/link";
import { FooterLegalNotice } from "./legal-notice";
export { FooterLegalNotice };
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
      <span className="brand-wordmark">
        Heartbeat{!compact && <span className="brand-light"> Checker</span>}
      </span>
    </Link>
  );
}
export function PublicFooter({ onHome = false }: { onHome?: boolean }) {
  const landingMode = process.env.WEB_MODE === "landing";
  return (
    <footer className="landing-footer">
      <div className="landing-footer-main">
        <Brand />
        <span>Круглосуточный мониторинг сайтов и сервисов.</span>
      </div>
      <nav aria-label="Документы и контакты">
        <Link href="/terms">Условия и оферта</Link>
        <Link href="/terms#payments">Оплата и возвраты</Link>
        <Link href="/privacy">Персональные данные</Link>
        <Link href="/cookies">Cookie</Link>
        <Link href="/contacts">Контакты</Link>
      </nav>
      <div className="landing-footer-contact">
        <a href="mailto:info@heartbeat-checker.ru">info@heartbeat-checker.ru</a>
        {onHome && (
          <Link href={landingMode ? "#how" : "/login"}>
            {landingMode ? "Как это работает ↗" : "Личный кабинет ↗"}
          </Link>
        )}
      </div>
      <FooterLegalNotice />
    </footer>
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
