"use client";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { Profile, SessionContext } from "@/entities/session";
import { ThemeToggle } from "@/features/theme-toggle";
import { api, ApiError, errorMessage, safeAppReturnTo } from "@/shared/api";
import {
  captureMonitorDraft,
  clearMonitorDraft,
  readMonitorDraft,
} from "@/shared/android";
import { Brand, Loading, Notice, Icon } from "@/shared/ui";
export function ConsoleShell({ children }: { children: React.ReactNode }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");
  const [draftError, setDraftError] = useState("");
  const [leaving, setLeaving] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const sidebar = useRef<HTMLElement>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const path = usePathname();
  const router = useRouter();
  const profileRequest = useRef(0);
  const sessionTransitions = useRef(0);
  const beginSessionTransition = useCallback(() => {
    ++sessionTransitions.current;
    ++profileRequest.current;
  }, []);
  const endSessionTransition = useCallback(() => {
    sessionTransitions.current = Math.max(0, sessionTransitions.current - 1);
    ++profileRequest.current;
  }, []);
  const refresh = useCallback(async () => {
    if (sessionTransitions.current > 0)
      throw new DOMException("Session transition in progress", "AbortError");
    const request = ++profileRequest.current;
    try {
      const current = await api<Profile>("/me");
      if (request !== profileRequest.current)
        throw new DOMException("Session request superseded", "AbortError");
      readMonitorDraft(current.account.id);
      setProfile(current);
      setError("");
    } catch (e) {
      if (
        request !== profileRequest.current ||
        (e instanceof DOMException && e.name === "AbortError")
      )
        throw new DOMException("Session request superseded", "AbortError");
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
      throw e;
    }
  }, [router]);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    const problem = captureMonitorDraft();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (problem) setDraftError(problem);
    void refresh().catch(() => undefined);
    const visible = () => {
      if (document.visibilityState === "visible")
        void refresh().catch(() => undefined);
    };
    const focused = () => void refresh().catch(() => undefined);
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("focus", focused);
    return () => {
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate any latest in-flight request on cleanup.
      ++profileRequest.current;
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
  useEffect(() => {
    const header = sidebar.current;
    if (!menuOpen || !header) return;
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setMenuOpen(false);
      menuButton.current?.focus();
    };
    header.addEventListener("keydown", escape);
    return () => header.removeEventListener("keydown", escape);
  }, [menuOpen, profile?.account.id]);
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
            <button onClick={() => void refresh().catch(() => undefined)}>
              Повторить
            </button>
          </>
        ) : (
          <Loading text="Открываем кабинет…" />
        )}
      </main>
    );
  return (
    <SessionContext
      key={profile.account.id}
      value={{ profile, refresh, beginSessionTransition, endSessionTransition }}
    >
      <div className="console">
        <aside className="sidebar" ref={sidebar}>
          <div className="sidebar-heading">
            <Brand compact />
            <button
              ref={menuButton}
              type="button"
              className="secondary sidebar-menu-toggle"
              aria-expanded={menuOpen}
              aria-controls="console-navigation"
              onClick={() => setMenuOpen((value) => !value)}
            >
              Меню
            </button>
          </div>
          <p className="sidebar-label">РАБОЧЕЕ ПРОСТРАНСТВО</p>
          <nav
            id="console-navigation"
            aria-label="Кабинет"
            data-open={menuOpen}
          >
            <Link
              href="/app"
              onClick={() => setMenuOpen(false)}
              aria-current={path === "/app" ? "page" : undefined}
            >
              <Icon name="activity" /> Обзор мониторов
            </Link>
            <Link
              href="/app/billing"
              onClick={() => setMenuOpen(false)}
              aria-current={path === "/app/billing" ? "page" : undefined}
            >
              <Icon name="payment" /> Оплата
            </Link>
            <Link
              href="/app/settings"
              onClick={() => setMenuOpen(false)}
              aria-current={path === "/app/settings" ? "page" : undefined}
            >
              <Icon name="settings" /> Настройки аккаунта
            </Link>
            {profile.account.role === "admin" && (
              <Link
                href="/app/admin"
                onClick={() => setMenuOpen(false)}
                aria-current={path === "/app/admin" ? "page" : undefined}
              >
                <Icon name="shield" /> Администрирование
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
              <Icon name="logout" />
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
              <span className="top-label">SERVICE MONITORING</span>
              <ThemeToggle />
            </div>
            <button
              className="mobile-logout quiet"
              disabled={leaving}
              onClick={() => void logout()}
            >
              Выйти <Icon name="logout" />
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
