"use client";
import Link from "next/link";
import { FormEvent, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Session, useSession } from "@/entities/session";
import { MessengerLinks } from "@/features/messenger-links";
import { OAuthIdentities } from "@/features/oauth-identities";
import { AccountDeletion } from "@/features/account-deletion";
import { api, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice, PasswordInput } from "@/shared/ui";
export function SettingsPage() {
  const { profile, refresh, beginSessionTransition, endSessionTransition } =
    useSession();
  const router = useRouter();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [busy, setBusy] = useState("");
  const loadSessions = useCallback(async () => {
    setSessions((await api<{ sessions: Session[] }>("/me/sessions")).sessions);
  }, []);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadSessions().catch((e) => setError(errorMessage(e)));
  }, [loadSessions]);
  async function saveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setBusy("profile");
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
      {error && <Notice>{error}</Notice>}
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
              <span className="field-hint">Подтверждённый адрес аккаунта.</span>
            </label>
            <button disabled={!!busy} className="primary">
              {busy === "profile" ? "Сохраняем…" : "Сохранить изменения"}
            </button>
          </form>
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
                Установить пароль через email →
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
        {!sessions ? (
          <Loading />
        ) : (
          <ul className="session-list">
            {sessions.map((session) => (
              <li key={session.id}>
                <span className="device-icon" aria-hidden="true">
                  ▣
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
      <MessengerLinks />
      <AccountDeletion />
    </>
  );
}
