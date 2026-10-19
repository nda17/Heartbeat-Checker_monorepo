"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice, PasswordInput } from "@/shared/ui";

export type MfaState = {
  enabled: boolean;
  verifiedUntil: string | null;
  stepUpUntil: string | null;
  recoveryCodesRemaining: number;
};
type Enrollment = {
  enrollmentId: string;
  secret: string;
  otpauthUri: string;
  expiresAt: string;
};

export function AdminMfa({
  onState,
}: {
  onState: (state: MfaState | null) => void;
}) {
  const [state, setState] = useState<MfaState | null>(null);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [recoveredAccess, setRecoveredAccess] = useState(false);
  const [now, setNow] = useState(0);

  const refresh = useCallback(async () => {
    try {
      const next = await api<MfaState>("/auth/mfa");
      setState(next);
      onState(next);
      setError("");
    } catch (cause) {
      setState(null);
      setError(errorMessage(cause));
      onState(null);
    }
  }, [onState]);
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => window.clearInterval(timer);
  }, []);

  async function enroll(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    setBusy("enroll");
    setError("");
    try {
      const { password } = Object.fromEntries(new FormData(form));
      setEnrollment(
        await api<Enrollment>("/auth/mfa/enrollment", {
          method: "POST",
          authenticated: true,
          body: { password },
        }),
      );
      form.reset();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy("");
    }
  }
  async function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!enrollment) return;
    const form = event.currentTarget;
    setBusy("confirm");
    setError("");
    try {
      const { code } = Object.fromEntries(new FormData(form));
      const result = await api<{ recoveryCodes: string[] }>(
        "/auth/mfa/enrollment/confirm",
        {
          method: "POST",
          authenticated: true,
          body: { enrollmentId: enrollment.enrollmentId, code },
        },
      );
      setRecoveryCodes(result.recoveryCodes);
      setRecoveredAccess(false);
      setEnrollment(null);
      form.reset();
      await refresh();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy("");
    }
  }
  async function verify(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy("verify");
    setError("");
    try {
      await api(`/auth/mfa/${useRecovery ? "recovery" : "verify"}`, {
        method: "POST",
        authenticated: true,
        body: useRecovery
          ? { password: data.get("password"), recoveryCode: data.get("code") }
          : { password: data.get("password"), code: data.get("code") },
      });
      setRecoveredAccess(useRecovery);
      form.reset();
      await refresh();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy("");
    }
  }
  async function replaceCodes(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy("codes");
    setError("");
    try {
      const result = await api<{ recoveryCodes: string[] }>(
        "/auth/mfa/recovery-codes",
        {
          method: "POST",
          authenticated: true,
          body: { password: data.get("password"), code: data.get("code") },
        },
      );
      setRecoveryCodes(result.recoveryCodes);
      form.reset();
      await refresh();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy("");
    }
  }

  const verified =
    !!state?.verifiedUntil && Date.parse(state.verifiedUntil) > now;
  const stepUp = !!state?.stepUpUntil && Date.parse(state.stepUpUntil) > now;
  return (
    <section
      className="panel admin-mfa-panel"
      aria-labelledby="admin-mfa-title"
    >
      <div className="panel-heading">
        <div>
          <h2 id="admin-mfa-title">Защита административного доступа</h2>
          <p>
            Для просмотра данных подтвердите вход. Изменения требуют свежего
            кода.
          </p>
        </div>
      </div>
      {error && <Notice>{error}</Notice>}
      {!state && !error && <Loading />}
      {recoveryCodes.length > 0 && (
        <div className="admin-recovery-codes" role="status">
          <strong>Сохраните новые резервные коды сейчас</strong>
          <p>
            Коды показываются один раз. Каждый можно использовать только один
            раз.
          </p>
          <ul>
            {recoveryCodes.map((code) => (
              <li key={code}>
                <code>{code}</code>
              </li>
            ))}
          </ul>
          <button
            className="secondary"
            type="button"
            onClick={() => setRecoveryCodes([])}
          >
            Я сохранил коды
          </button>
        </div>
      )}
      {state && !state.enabled && !enrollment && (
        <form className="form-stack" onSubmit={enroll}>
          <p>Подключите приложение-аутентификатор к вашему аккаунту.</p>
          <label>
            Пароль аккаунта
            <PasswordInput
              name="password"
              autoComplete="current-password"
              required
            />
          </label>
          <button className="primary" disabled={!!busy}>
            Начать настройку MFA
          </button>
        </form>
      )}
      {enrollment && (
        <div className="admin-enrollment">
          {state?.enabled && (
            <p>
              Старый аутентификатор продолжит работать до подтверждения нового
              кода. После подтверждения прежний фактор и резервные коды будут
              недействительны.
            </p>
          )}
          <p>
            Откройте ссылку в приложении-аутентификаторе или введите ключ
            вручную.
          </p>
          <a href={enrollment.otpauthUri}>Добавить в аутентификатор ↗</a>
          <p>
            Ключ: <code>{enrollment.secret}</code>
          </p>
          <p className="small muted">
            Настройка действительна до {dateTime(enrollment.expiresAt)}.
          </p>
          <form className="form-stack" onSubmit={confirm}>
            <label>
              Код из приложения
              <input
                name="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                required
                maxLength={12}
              />
            </label>
            <button className="primary" disabled={!!busy}>
              Подтвердить настройку
            </button>
          </form>
        </div>
      )}
      {state?.enabled && !enrollment && (
        <>
          <p className="small muted">
            {verified
              ? `Просмотр разрешён до ${dateTime(state.verifiedUntil)}.`
              : "Подтвердите вход для просмотра данных."}{" "}
            {stepUp
              ? `Изменения разрешены до ${dateTime(state.stepUpUntil)}.`
              : "Для изменений нужен новый код."}{" "}
            Резервных кодов: {state.recoveryCodesRemaining}.
          </p>
          {recoveredAccess && (
            <Notice>
              Вход подтверждён резервным кодом. Если приложение-аутентификатор
              недоступно, замените его сейчас: старый фактор сохранится до
              подтверждения нового.
            </Notice>
          )}
          <form className="form-stack" onSubmit={verify}>
            <label>
              Пароль аккаунта
              <PasswordInput
                name="password"
                autoComplete="current-password"
                required
              />
            </label>
            <label>
              {useRecovery ? "Резервный код" : "Код из приложения"}
              <input
                name="code"
                autoComplete={useRecovery ? "off" : "one-time-code"}
                inputMode={useRecovery ? "text" : "numeric"}
                required
                maxLength={64}
              />
            </label>
            <button className="secondary" disabled={!!busy}>
              Подтвердить вход
            </button>
            <button
              className="quiet"
              type="button"
              onClick={() => setUseRecovery((value) => !value)}
            >
              {useRecovery
                ? "Использовать приложение"
                : "Использовать резервный код"}
            </button>
          </form>
          {stepUp && (
            <>
              <form className="form-stack admin-reissue" onSubmit={enroll}>
                <h3>Заменить аутентификатор</h3>
                <p>
                  Для начала нужен текущий пароль. Прежний фактор останется
                  активным до подтверждения нового кода.
                </p>
                <label>
                  Пароль аккаунта
                  <PasswordInput
                    name="password"
                    autoComplete="current-password"
                    required
                  />
                </label>
                <button className="secondary" disabled={!!busy}>
                  Начать замену
                </button>
              </form>
              <form
                className="form-stack admin-reissue"
                onSubmit={replaceCodes}
              >
                <h3>Перевыпустить резервные коды</h3>
                <p>Прежние коды перестанут работать.</p>
                <label>
                  Пароль аккаунта
                  <PasswordInput
                    name="password"
                    autoComplete="current-password"
                    required
                  />
                </label>
                <label>
                  Код из приложения
                  <input
                    name="code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    required
                    maxLength={12}
                  />
                </label>
                <button className="secondary" disabled={!!busy}>
                  Создать новые коды
                </button>
              </form>
            </>
          )}
        </>
      )}
    </section>
  );
}
