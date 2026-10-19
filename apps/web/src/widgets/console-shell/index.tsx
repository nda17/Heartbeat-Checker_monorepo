"use client";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { Profile, SessionContext } from "@/entities/session";
import { ThemeToggle } from "@/features/theme-toggle";
import { api, ApiError, errorMessage, safeAppReturnTo } from "@/shared/api";
import {
  captureMonitorDraft,
  clearMonitorDraft,
  readMonitorDraft,
} from "@/shared/android";
import { Brand, Loading, Notice } from "@/shared/ui";
export function ConsoleShell({ children }: { children: React.ReactNode }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");
  const [draftError, setDraftError] = useState("");
  const [leaving, setLeaving] = useState(false);
  const path = usePathname();
  const router = useRouter();
  const refresh = useCallback(async () => {
    try {
      const current = await api<Profile>("/me");
      readMonitorDraft(current.account.id);
      setProfile(current);
      setError("");
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setProfile(null);
        const returnTo = safeAppReturnTo(
          window.location.pathname + window.location.search,
        );
        router.replace(
          returnTo
            ? `/login?returnTo=${encodeURIComponent(returnTo)}`
            : "/login",
        );
      } else setError(errorMessage(e));
    }
  }, [router]);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    const problem = captureMonitorDraft();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (problem) setDraftError(problem);
    void refresh();
    const visible = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    const focused = () => void refresh();
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("focus", focused);
    return () => {
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("focus", focused);
    };
  }, [refresh]);
  useEffect(() => {
    const capture = () => {
      const problem = captureMonitorDraft(profile?.account.id);
      if (problem) setDraftError(problem);
    };
    window.addEventListener("hashchange", capture);
    return () => window.removeEventListener("hashchange", capture);
  }, [profile?.account.id]);
  async function logout() {
    setLeaving(true);
    try {
      await api("/auth/logout", { method: "POST", authenticated: true });
      clearMonitorDraft();
      setProfile(null);
      router.replace("/login");
    } catch (e) {
      setError(errorMessage(e));
      setLeaving(false);
    }
  }
  if (!profile)
    return (
      <main id="main" className="standalone">
        {error ? (
          <>
            <Notice>{error}</Notice>
            <button onClick={() => void refresh()}>Повторить</button>
          </>
        ) : (
          <Loading text="Открываем кабинет…" />
        )}
      </main>
    );
  return (
    <SessionContext key={profile.account.id} value={{ profile, refresh }}>
      <div className="console">
        <aside className="sidebar">
          <Brand compact />
          <p className="sidebar-label">РАБОЧЕЕ ПРОСТРАНСТВО</p>
          <nav aria-label="Кабинет">
            <Link
              href="/app"
              aria-current={path === "/app" ? "page" : undefined}
            >
              <span aria-hidden="true">◉</span> Обзор мониторов
            </Link>
            <Link
              href="/app/billing"
              aria-current={path === "/app/billing" ? "page" : undefined}
            >
              <span aria-hidden="true">₽</span> Оплата
            </Link>
            <Link
              href="/app/settings"
              aria-current={path === "/app/settings" ? "page" : undefined}
            >
              <span aria-hidden="true">⚙</span> Настройки аккаунта
            </Link>
            {profile.account.role === "admin" && (
              <Link
                href="/app/admin"
                aria-current={path === "/app/admin" ? "page" : undefined}
              >
                <span aria-hidden="true">◈</span> Администрирование
              </Link>
            )}
          </nav>
          <div className="sidebar-bottom">
            <p className="small muted">
              Ваши сервисы.
              <br />В поле зрения.
            </p>
            <div className="account-mini">
              <span className="avatar">
                {profile.account.name.slice(0, 1).toUpperCase()}
              </span>
              <div>
                <strong>{profile.account.name}</strong>
                <span>{profile.email}</span>
              </div>
            </div>
            <button
              className="quiet logout"
              disabled={leaving}
              onClick={() => void logout()}
            >
              {leaving ? "Выходим…" : "Выйти из аккаунта"}
              <span aria-hidden="true">↗</span>
            </button>
          </div>
        </aside>
        <div className="console-content">
          <header className="console-top">
            <span>
              Heartbeat Checker <span className="slash">/</span>{" "}
              {path.includes("settings")
                ? "Аккаунт"
                : path.includes("admin")
                  ? "Администрирование"
                  : path.includes("billing")
                    ? "Оплата"
                    : "Мониторы"}
            </span>
            <div className="console-top-actions">
              <span className="top-label">HTTP MONITORING</span>
              <ThemeToggle />
            </div>
            <button
              className="mobile-logout quiet"
              disabled={leaving}
              onClick={() => void logout()}
            >
              Выйти ↗
            </button>
          </header>
          <main id="main" className="workspace">
            {error && <Notice>{error}</Notice>}
            {draftError && <Notice>{draftError}</Notice>}
            {children}
          </main>
          <footer className="console-footer">
            Heartbeat Checker<span>Каждая проверка имеет значение.</span>
          </footer>
        </div>
      </div>
    </SessionContext>
  );
}
