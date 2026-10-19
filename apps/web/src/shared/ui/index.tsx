import Link from "next/link";
import { FooterLegalNotice } from "./legal-notice";
export { FooterLegalNotice };
export { PasswordInput } from "./password-input";
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
export function RuStoreSoon({ label = "RuStore" }: { label?: string }) {
  return (
    <span className="store-soon">
      <button
        type="button"
        aria-disabled="true"
        aria-label={`${label} — Скоро`}
        title="Скоро"
      >
        {/* Official RuStore media kit: https://www.rustore.ru/media-kit */}
        <svg
          width="24"
          height="24"
          viewBox="0 0 96 96"
          fill="none"
          aria-hidden="true"
        >
          <path
            d="M6.74826 89.2517C13.4965 96 24.3577 96 46.08 96H49.92C71.6423 96 82.5034 96 89.2518 89.2517C96 82.5034 96 71.6422 96 49.9197V46.0797C96 24.3573 96 13.4961 89.2518 6.74786C82.5034 0 71.6423 0 49.9201 0H46.08C24.3577 0 13.4965 0 6.74826 6.74787C0 13.4961 0 24.3573 0 46.0797V49.9198C0 71.6422 0 82.5035 6.74826 89.2517Z"
            fill="#0077FF"
          />
          <path
            fillRule="evenodd"
            clipRule="evenodd"
            d="M74.1174 63.8091L67.6309 62.1887C66.8499 61.9647 66.2983 61.2619 66.2706 60.445L65.4611 36.5712C65.227 33.4112 62.8813 30.8981 60.4301 30.158C60.2925 30.1164 60.1456 30.1724 60.0641 30.2908C59.9813 30.4112 60.0132 30.5777 60.1288 30.6671C60.7341 31.1357 62.4 32.6648 62.4 35.3022L62.3952 66.5166C62.3952 69.5307 59.5471 71.7413 56.6093 71.0074L50.0226 69.362C49.2923 69.1064 48.785 68.4263 48.7584 67.6417L47.949 43.7662C47.7149 40.6063 45.3692 38.0932 42.918 37.3531C42.7804 37.3115 42.6335 37.3675 42.552 37.4859C42.4692 37.6062 42.5011 37.7728 42.6167 37.8622C43.222 38.3307 44.8879 39.8599 44.8879 42.4973L44.883 68.5161L44.8867 68.5168V73.7147C44.8867 76.7288 42.0386 78.9393 39.1007 78.2055L20.4963 73.558C17.9098 72.9119 16.0963 70.5982 16.0963 67.9445L16.0963 36.6814C16.0963 33.6673 18.9444 31.4567 21.8822 32.1906L33.6048 35.119V29.4833C33.6048 26.4692 36.4529 24.2587 39.3908 24.9925L51.1129 27.9208V22.285C51.1129 19.2709 53.961 17.0603 56.8988 17.7942L75.5032 22.4417C78.0897 23.0878 79.9033 25.4015 79.9033 28.0552V59.3183C79.9033 62.3324 77.0552 64.5429 74.1174 63.8091Z"
            fill="white"
          />
        </svg>
        {label}
      </button>
      <span className="store-soon-tooltip" aria-hidden="true">
        Скоро
      </span>
    </span>
  );
}
export function PublicFooter({ onHome = false }: { onHome?: boolean }) {
  const landingMode = process.env.WEB_MODE === "landing";
  return (
    <footer className="landing-footer">
      <div className="landing-footer-inner">
        <div className="landing-footer-main">
          <Brand />
          <p>Круглосуточный мониторинг сайтов и сервисов.</p>
          <a href="mailto:info@heartbeat-checker.ru">
            info@heartbeat-checker.ru
          </a>
          {onHome && (
            <Link href={landingMode ? "#how" : "/login"}>
              {landingMode ? "Как это работает ↗" : "Личный кабинет ↗"}
            </Link>
          )}
        </div>
        <nav
          className="landing-footer-install"
          aria-label="Установка приложения"
        >
          <h2>Установка приложения</h2>
          <a href="/downloads/android">Скачать .APK с сайта</a>
          <RuStoreSoon label="Скачать из RuStore" />
        </nav>
        <nav className="landing-footer-social" aria-label="Соцсети">
          <h2>Соцсети</h2>
          <a
            href="https://max.ru/id272385800008_biz"
            target="_blank"
            rel="noopener noreferrer"
          >
            Наш канал в MAX
          </a>
          <a
            href="https://vk.ru/heartbeat_ch"
            target="_blank"
            rel="noopener noreferrer"
          >
            Наш канал в VK
          </a>
        </nav>
        <nav className="landing-footer-support" aria-label="Техподдержка">
          <h2>Техподдержка</h2>
          <a
            href="https://max.ru/id272385800008_1_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            Техподдержка в MAX
          </a>
          <a
            href="https://t.me/heartbeat_support_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            Техподдержка в Telegram
          </a>
          <Link href="/contacts">Контакты</Link>
        </nav>
        <nav className="landing-footer-documents" aria-label="Документы">
          <h2>Документы</h2>
          <Link href="/terms">Условия и оферта</Link>
          <Link href="/terms#payments">Оплата и возвраты</Link>
          <Link href="/privacy">Персональные данные</Link>
          <Link href="/cookies">Cookie</Link>
        </nav>
        <FooterLegalNotice />
      </div>
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
