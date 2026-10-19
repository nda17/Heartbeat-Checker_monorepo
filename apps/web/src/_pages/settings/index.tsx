"use client";
import Link from "next/link";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Session, useSession } from "@/entities/session";
import { MessengerLinks } from "@/features/messenger-links";
import { OAuthIdentities } from "@/features/oauth-identities";
import { AccountDeletion } from "@/features/account-deletion";
import { api, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice, PasswordInput, Icon } from "@/shared/ui";

function EmailVerification() {
  const { profile, refresh } = useSession();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const request = useRef<AbortController | null>(null);
  const requestTimeout = useRef<number | undefined>(undefined);
  useEffect(() => {
    const params = new URLSearchParams(window.location.hash.slice(1));
    const value = params.get("verify-email");
    if (value) {
      // Capture the private link only inside the authenticated settings page.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setToken(value);
      params.delete("verify-email");
      const remaining = params.toString();
      window.history.replaceState(
        null,
        "",
        window.location.pathname +
          window.location.search +
          (remaining ? `#${remaining}` : ""),
      );
    }
    return () => {
      request.current?.abort();
      window.clearTimeout(requestTimeout.current);
    };
  }, []);
  async function submit(confirm: boolean) {
    if (request.current) return;
    if (confirm && !/^[A-Za-z0-9_-]{43}$/.test(token)) {
      setError("Ссылка некорректна. Запросите новое письмо.");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setSuccess("");
    const timeout = window.setTimeout(() => {
      controller.abort();
      if (request.current === controller) request.current = null;
      setBusy(false);
      setError(
        "Ответ занял слишком много времени. Обновите страницу и проверьте состояние email перед повтором.",
      );
    }, 10_000);
    requestTimeout.current = timeout;
    try {
      await api(`/me/email-verification${confirm ? "/confirm" : ""}`, {
        method: "POST",
        authenticated: true,
        body: confirm ? { token } : {},
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      // The POST outcome is known. A slow profile refresh cannot make the
      // committed confirmation look like an ambiguous write.
      window.clearTimeout(timeout);
      setBusy(false);
      if (confirm) {
        setToken("");
        setConfirmed(true);
      }
      setSuccess(
        confirm
          ? "Email подтверждён. Теперь можно подключить email-уведомления."
          : "Письмо с подтверждением запрошено. Откройте ссылку в этом аккаунте; она действует 30 минут.",
      );
      if (confirm) {
        try {
          await refresh();
        } catch (cause) {
          if (!controller.signal.aborted)
            setError(
              `Email подтверждён, но профиль не обновился: ${errorMessage(cause)}`,
            );
        }
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause));
    } finally {
      window.clearTimeout(timeout);
      if (request.current === controller) request.current = null;
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  return (
    <div className="form-stack" style={{ marginTop: 20 }}>
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      {profile.emailVerified || confirmed ? (
        <p className="small muted">Email подтверждён.</p>
      ) : (
        <>
          <p className="small muted">
            Для email-уведомлений подтвердите адрес аккаунта. Пароль и текущие
            сеансы не изменятся. Если ссылка предложила войти, после входа
            откройте её ещё раз.
          </p>
          {token && (
            <button
              className="primary"
              disabled={busy}
              onClick={() => void submit(true)}
            >
              Подтвердить email
            </button>
          )}
          <button
            className="secondary"
            disabled={busy}
            onClick={() => void submit(false)}
          >
            {busy ? "Отправляем…" : "Отправить письмо для подтверждения email"}
          </button>
        </>
      )}
    </div>
  );
}

export function SettingsPage() {
  const { profile, refresh, beginSessionTransition, endSessionTransition } =
    useSession();
  const router = useRouter();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [sessionsError, setSessionsError] = useState("");
  const [sessionsAttempt, setSessionsAttempt] = useState(0);
  const [error, setError] = useState("");
  const [errorTarget, setErrorTarget] = useState("profile");
  const [success, setSuccess] = useState("");
  const [busy, setBusy] = useState("");
  const loadSessions = useCallback(async (signal?: AbortSignal) => {
    const result = await api<{ sessions: Session[] }>("/me/sessions", {
      signal,
    });
    if (!signal?.aborted) setSessions(result.sessions);
  }, []);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSessionsError("");
    const timer = window.setTimeout(() => {
      controller.abort();
      setSessionsError(
        "Загрузка сессий заняла слишком много времени. Повторите попытку.",
      );
    }, 10_000);
    void loadSessions(controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) setSessionsError(errorMessage(cause));
      })
      .finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [loadSessions, sessionsAttempt]);
  async function saveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setBusy("profile");
    setErrorTarget("profile");
    setError("");
    setSuccess("");
    try {
      await api("/me", {
        method: "PATCH",
        authenticated: true,
        body: { name: data.get("name") },
      });
      setSuccess("Имя сохранено.");
      try {
        await refresh();
      } catch (e) {
        setError(`Имя сохранено, но профиль не обновился: ${errorMessage(e)}`);
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }
  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy("password");
    setErrorTarget("password");
    setError("");
    setSuccess("");
    beginSessionTransition();
    try {
      await api("/auth/password/change", {
        method: "POST",
        authenticated: true,
        body: {
          currentPassword: data.get("currentPassword"),
          newPassword: data.get("newPassword"),
        },
      });
      form.reset();
      router.replace("/login");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      endSessionTransition();
      setBusy("");
    }
  }
  async function revoke(id?: string, current = false) {
    setBusy(id ?? "all");
    setErrorTarget(id ?? "sessions");
    setError("");
    setSuccess("");
    const rotatesSession = !id || current;
    if (rotatesSession) beginSessionTransition();
    try {
      await api(`/me/sessions${id ? `/${encodeURIComponent(id)}` : ""}`, {
        method: "DELETE",
        authenticated: true,
      });
      if (!id || current) router.replace("/login");
      else {
        setSuccess("Сессия завершена.");
        try {
          await loadSessions();
        } catch (e) {
          setError(
            `Сессия завершена, но список не обновился: ${errorMessage(e)}`,
          );
        }
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      if (rotatesSession) endSessionTransition();
      setBusy("");
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">ВАШ АККАУНТ</p>
          <h1>Настройки</h1>
          <p>Личные данные и контроль доступа к рабочему пространству.</p>
        </div>
      </div>
      {error && errorTarget === "profile" && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      <div className="settings-grid">
        <section className="panel settings-panel">
          <div className="panel-heading">
            <span className="panel-index">01</span>
            <div>
              <h2>Профиль</h2>
              <p>Как к вам обращаться в кабинете.</p>
            </div>
          </div>
          <form className="form-stack" onSubmit={saveProfile}>
            <label>
              Имя
              <input
                key={profile.account.name}
                name="name"
                defaultValue={profile.account.name}
                minLength={2}
                maxLength={100}
                required
                autoComplete="name"
              />
            </label>
            <label>
              Email
              <input value={profile.email} readOnly type="email" />
              <span className="field-hint">
                {profile.emailVerified
                  ? "Подтверждённый адрес аккаунта."
                  : "Подтверждение адреса нужно для email-уведомлений."}
              </span>
            </label>
            <button disabled={!!busy} className="primary">
              {busy === "profile" ? "Сохраняем…" : "Сохранить изменения"}
            </button>
          </form>
          <EmailVerification key={profile.account.id} />
        </section>
        <section className="panel settings-panel">
          <div className="panel-heading">
            <span className="panel-index">02</span>
            <div>
              <h2>Пароль</h2>
              <p>
                {profile.hasPassword
                  ? "Обновите пароль и завершите прежние сессии."
                  : "Установите пароль для управления способами входа и административной MFA."}
              </p>
            </div>
          </div>
          {profile.hasPassword ? (
            <form className="form-stack" onSubmit={changePassword}>
              <label>
                Текущий пароль
                <PasswordInput
                  name="currentPassword"
                  autoComplete="current-password"
                  required
                  maxLength={1024}
                />
              </label>
              <label>
                Новый пароль
                <PasswordInput
                  name="newPassword"
                  autoComplete="new-password"
                  required
                  minLength={12}
                  maxLength={1024}
                />
                <span className="field-hint">
                  От 12 символов. После смены понадобится войти снова.
                </span>
              </label>
              {error && errorTarget === "password" && <Notice>{error}</Notice>}
              <button className="secondary" disabled={!!busy}>
                {busy === "password" ? "Обновляем…" : "Изменить пароль"}
              </button>
            </form>
          ) : (
            <p className="small">
              У вас пока нет пароля. Запросите письмо для его установки, затем
              вернитесь в настройки.
              <br />
              <Link href="/forgot-password">
                Установить пароль через email <Icon name="arrow-right" />
              </Link>
            </p>
          )}
        </section>
      </div>
      <OAuthIdentities
        hasPassword={profile.hasPassword}
        isAdmin={profile.account.role === "admin"}
        onChange={refresh}
      />
      <section className="panel sessions-panel">
        <div className="panel-heading">
          <span className="panel-index">04</span>
          <div>
            <h2>Активные сессии</h2>
            <p>Устройства, на которых выполнен вход в аккаунт.</p>
          </div>
          <button
            className="quiet danger-text"
            disabled={!!busy}
            onClick={() => void revoke()}
          >
            Завершить все сессии
          </button>
        </div>
        {error && errorTarget === "sessions" && <Notice>{error}</Notice>}
        {sessionsError ? (
          <div className="form-stack">
            <Notice>{sessionsError}</Notice>
            <button
              type="button"
              className="secondary"
              onClick={() => setSessionsAttempt((value) => value + 1)}
            >
              Повторить загрузку сессий
            </button>
          </div>
        ) : !sessions ? (
          <Loading />
        ) : (
          <ul className="session-list">
            {sessions.map((session) => (
              <li key={session.id}>
                <span className="device-icon" aria-hidden="true">
                  <Icon name="device" />
                </span>
                <div className="session-info">
                  <strong>
                    {session.current ? "Текущее устройство" : "Устройство"}
                    {session.current && (
                      <span className="badge up">Вы здесь</span>
                    )}
                  </strong>
                  <p className="session-agent">
                    {session.userAgent || "Браузер не определён"}
                  </p>
                  <p className="small muted">
                    {session.ip || "IP не определён"} · Последняя активность:{" "}
                    {dateTime(session.lastUsedAt)} · До{" "}
                    {dateTime(session.expiresAt)}
                  </p>
                  {error && errorTarget === session.id && (
                    <Notice>{error}</Notice>
                  )}
                </div>
                <button
                  className="quiet"
                  disabled={!!busy}
                  onClick={() => void revoke(session.id, session.current)}
                >
                  {busy === session.id ? "Завершаем…" : "Завершить"}
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
      <MessengerLinks email={profile.email} />
      <AccountDeletion />
    </>
  );
}
