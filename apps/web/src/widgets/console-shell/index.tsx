"use client";
import { notify } from "@/shared/notifications";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Profile,
  SessionContext,
  signalSessionTransition,
} from "@/entities/session";
import { ThemeToggle } from "@/features/theme-toggle";
import {
  api,
  ApiError,
  boundedRequest,
  errorMessage,
  safeAppReturnTo,
} from "@/shared/api";
import { Brand, Loading, Notice, Icon } from "@/shared/ui";
export function ConsoleShell({ children }: { children: React.ReactNode }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");
  const [leaving, setLeaving] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const sidebar = useRef<HTMLElement>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const path = usePathname();
  const router = useRouter();
  const profileRequest = useRef(0);
  const profileController = useRef<AbortController | null>(null);
  const sessionTransitions = useRef(0);
  const beginSessionTransition = useCallback(() => {
    ++sessionTransitions.current;
    if (sessionTransitions.current === 1) signalSessionTransition("begin");
    ++profileRequest.current;
    profileController.current?.abort();
  }, []);
  const endSessionTransition = useCallback(() => {
    const wasTransitioning = sessionTransitions.current > 0;
    sessionTransitions.current = Math.max(0, sessionTransitions.current - 1);
    if (wasTransitioning && sessionTransitions.current === 0)
      signalSessionTransition("end");
    ++profileRequest.current;
  }, []);
  const refresh = useCallback(async () => {
    if (sessionTransitions.current > 0)
      throw new DOMException("Session transition in progress", "AbortError");
    const request = ++profileRequest.current;
    profileController.current?.abort();
    const controller = new AbortController();
    profileController.current = controller;
    try {
      const current = await boundedRequest(controller, (signal) =>
        api<Profile>("/me", { signal }),
      );
      if (request !== profileRequest.current)
        throw new DOMException("Session request superseded", "AbortError");
      setProfile(current);
      setError("");
    } catch (e) {
      if (request !== profileRequest.current)
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
      } else
        setError(
          controller.signal.aborted
            ? "Проверка входа заняла слишком много времени. Повторите попытку."
            : errorMessage(e),
        );
      throw e;
    } finally {
      if (profileController.current === controller)
        profileController.current = null;
    }
  }, [router]);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Session state follows the asynchronous API response.
    void refresh().catch(() => undefined);
    const visible = () => {
      if (document.visibilityState === "visible")
        void refresh().catch(() => undefined);
    };
    const focused = () => void refresh().catch(() => undefined);
    const restored = (event: PageTransitionEvent) => {
      if (!event.persisted || sessionTransitions.current > 0) return;
      setProfile(null);
      setError("");
      setLeaving(false);
      void refresh().catch(() => undefined);
    };
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("focus", focused);
    window.addEventListener("pageshow", restored);
    return () => {
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate any latest in-flight request on cleanup.
      ++profileRequest.current;
      profileController.current?.abort();
      profileController.current = null;
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("focus", focused);
      window.removeEventListener("pageshow", restored);
    };
  }, [refresh]);
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
    if (leaving || sessionTransitions.current > 0) return;
    setLeaving(true);
    beginSessionTransition();
    try {
      await api("/auth/logout", { method: "POST", authenticated: true });
      setProfile(null);
      router.replace("/login");
    } catch (e) {
      notify.error(errorMessage(e));
      setLeaving(false);
    } finally {
      endSessionTransition();
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
