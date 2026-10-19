import Link from "next/link";
import { AuthForm, AuthMode, OAuthRegistration } from "@/features/auth";
import { ThemeToggle } from "@/features/theme-toggle";
import { Brand, FooterLegalNotice } from "@/shared/ui";
export function AuthPage({
  mode,
  oauthRegistration = false,
}: {
  mode: AuthMode;
  oauthRegistration?: boolean;
}) {
  return (
    <div className="auth-page">
      <aside className="auth-aside">
        <Brand />
        <div className="auth-aside-copy">
          <h2>
            Круглосуточный мониторинг
            <br />
            <span>ваших сайтов и сервисов.</span>
          </h2>
          <p>
            Доступность, время ответа и история инцидентов — в одном кабинете.
          </p>
          <div className="auth-decoration" aria-hidden="true">
            {Array.from({ length: 13 }, (_, i) => (
              <span key={i} style={{ animationDelay: `${i * 140}ms` }} />
            ))}
          </div>
          <span className="small">
            Точное состояние. Понятный следующий шаг.
          </span>
        </div>
        <p className="small muted">Heartbeat Checker</p>
      </aside>
      <main className="auth-main" id="main">
        <div className="auth-top">
          <Link className="back-home small" href="/">
            ← На главную
          </Link>
          <ThemeToggle />
        </div>
        <div className="auth-card">
          {oauthRegistration ? <OAuthRegistration /> : <AuthForm mode={mode} />}
        </div>
        <footer className="small muted">
          <nav aria-label="Документы и контакты">
            <Link href="/terms">Условия и оферта</Link>
            <Link href="/privacy">Персональные данные</Link>
            <Link href="/cookies">Cookie</Link>
            <Link href="/contacts">Контакты</Link>
          </nav>
          <FooterLegalNotice />
        </footer>
      </main>
    </div>
  );
}
