import Link from "next/link";
import { AuthForm, AuthMode } from "@/features/auth";
import { ThemeToggle } from "@/features/theme-toggle";
import { Brand } from "@/shared/ui";
export function AuthPage({ mode }: { mode: AuthMode }) {
  return (
    <div className="auth-page">
      <aside className="auth-aside">
        <Brand />
        <div className="auth-aside-copy">
          <p className="eyebrow">СПОКОЙСТВИЕ НАЧИНАЕТСЯ С ВИДИМОСТИ</p>
          <h2>
            Знайте, как
            <br />
            чувствуют себя
            <br />
            <span>ваши сервисы.</span>
          </h2>
          <p>
            Доступность, время ответа и история инцидентов — в одном рабочем
            пространстве.
          </p>
          <div className="auth-decoration" aria-hidden="true">
            {Array.from({ length: 13 }, (_, i) => (
              <span key={i} />
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
          <AuthForm mode={mode} />
        </div>
        <footer className="small muted">
          Проверяйте важное. Остальное подождёт.
        </footer>
      </main>
    </div>
  );
}
