"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { api, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice, PasswordInput, Icon } from "@/shared/ui";

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
type Verification = { verifiedUntil: string; stepUpUntil: string };

export function AdminMfa({
  onState,
  onSessionVerified,
  onBusyChange,
  disabled = false,
}: {
  onState: (state: MfaState | null) => void;
  onSessionVerified: () => Promise<void>;
  onBusyChange: (busy: boolean) => void;
  disabled?: boolean;
}) {
  const [state, setState] = useState<MfaState | null>(null);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [recoveredAccess, setRecoveredAccess] = useState(false);
  const [showVerify, setShowVerify] = useState(false);
  const [now, setNow] = useState(0);
  const statusRequest = useRef(0);
  const operation = useRef(false);
  const mounted = useRef(false);

  const refresh = useCallback(
    async (preserve = false) => {
      const request = ++statusRequest.current;
      try {
        const next = await api<MfaState>("/auth/mfa");
        if (!mounted.current || request !== statusRequest.current) return;
        setState(next);
        setNow(Date.now());
        onState(next);
        if (!preserve) setError("");
      } catch (cause) {
        if (!mounted.current || request !== statusRequest.current) return;
        if (!preserve) {
          setState(null);
          onState(null);
        }
        setError(
          preserve
            ? "Вход подтверждён, но не удалось обновить статус защиты. Повторите обновление статуса."
            : errorMessage(cause, "mfa"),
        );
      }
    },
    [onState],
  );
  useEffect(() => {
    mounted.current = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Status is populated after the asynchronous GET.
    void refresh();
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => {
      mounted.current = false;
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the latest request, not the mount-time generation.
      ++statusRequest.current;
      window.clearInterval(timer);
    };
  }, [refresh]);
  useEffect(() => {
    if (!enrollment) return;
    const timer = window.setTimeout(
      () => {
        setEnrollment(null);
        setError("Время настройки истекло. Начните настройку заново.");
      },
      Math.max(0, Date.parse(enrollment.expiresAt) - Date.now()),
    );
    return () => window.clearTimeout(timer);
  }, [enrollment]);

  async function run(kind: string, action: () => Promise<void>) {
    if (operation.current || disabled) return;
    operation.current = true;
    ++statusRequest.current;
    setBusy(kind);
    setError("");
    onBusyChange(true);
    try {
      await action();
    } catch (cause) {
      if (mounted.current)
        setError(
          errorMessage(cause, kind === "confirm" ? "mfa-confirm" : "mfa"),
        );
    } finally {
      operation.current = false;
      onBusyChange(false);
      if (mounted.current) {
        setBusy("");
      }
    }
  }
  async function verified(result: Verification, remaining: number) {
    if (!mounted.current) return;
    ++statusRequest.current;
    const next = {
      enabled: true,
      verifiedUntil: result.verifiedUntil,
      stepUpUntil: result.stepUpUntil,
      recoveryCodesRemaining: remaining,
    };
    setState(next);
    setNow(Date.now());
    onState(next);
    setShowVerify(false);
    try {
      await onSessionVerified();
    } catch {
      setError(
        "Вход подтверждён, но данные кабинета не обновились. Повторите обновление данных.",
      );
    }
    await refresh(true);
  }
  function enroll(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const password = new FormData(form).get("password");
    void run("enroll", async () => {
      const next = await api<Enrollment>("/auth/mfa/enrollment", {
        method: "POST",
        authenticated: true,
        body: { password },
      });
      if (mounted.current) setEnrollment(next);
      form.reset();
    });
  }
  function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!enrollment) return;
    const form = event.currentTarget;
    const body = {
      enrollmentId: enrollment.enrollmentId,
      code: new FormData(form).get("code"),
    };
    void run("confirm", async () => {
      const result = await api<Verification & { recoveryCodes: string[] }>(
        "/auth/mfa/enrollment/confirm",
        {
          method: "POST",
          authenticated: true,
          body,
        },
      );
      if (!mounted.current) return;
      setRecoveryCodes(result.recoveryCodes);
      setRecoveredAccess(false);
      setEnrollment(null);
      form.reset();
      await verified(result, result.recoveryCodes.length);
    });
  }
  function verify(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const recovery = useRecovery;
    void run("verify", async () => {
      const result = await api<Verification>(
        `/auth/mfa/${recovery ? "recovery" : "verify"}`,
        {
          method: "POST",
          authenticated: true,
          body: recovery
            ? { password: data.get("password"), recoveryCode: data.get("code") }
            : { password: data.get("password"), code: data.get("code") },
        },
      );
      if (!mounted.current) return;
      setRecoveredAccess(recovery);
      form.reset();
      await verified(
        result,
        Math.max(0, (state?.recoveryCodesRemaining ?? 0) - (recovery ? 1 : 0)),
      );
    });
  }
  function replaceCodes(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    void run("codes", async () => {
      const result = await api<{ recoveryCodes: string[] }>(
        "/auth/mfa/recovery-codes",
        {
          method: "POST",
          authenticated: true,
          body: { password: data.get("password"), code: data.get("code") },
        },
      );
      if (!mounted.current) return;
      setRecoveryCodes(result.recoveryCodes);
      form.reset();
      await refresh(true);
    });
  }

  const hasReadAccess =
    state?.enabled &&
    !!state.verifiedUntil &&
    Date.parse(state.verifiedUntil) > now;
  const hasWriteAccess =
    hasReadAccess &&
    !!state?.stepUpUntil &&
    Date.parse(state.stepUpUntil) > now;
  const blocked = !!busy || disabled;
  return (
    <section
      className="panel admin-mfa-panel"
      aria-labelledby="admin-mfa-title"
      aria-busy={!!busy}
    >
      <div className="admin-mfa-heading">
        <div>
          <h2 id="admin-mfa-title">Защита доступа</h2>
          <p>
            {hasWriteAccess
              ? "Просмотр и изменения доступны."
              : hasReadAccess
                ? "Просмотр доступен. Для изменения данных подтвердите действия."
                : "Подтвердите вход, чтобы открыть административные данные."}
          </p>
        </div>
        {state?.enabled && !enrollment && hasReadAccess && (
          <button
            className={hasWriteAccess ? "secondary" : "primary"}
            type="button"
            disabled={blocked}
            onClick={() => setShowVerify((value) => !value)}
          >
            {showVerify ? "Скрыть подтверждение" : "Подтвердить действия"}
          </button>
        )}
      </div>
      {error && <Notice>{error}</Notice>}
      {!state && !error && <Loading />}
      {!state && error && (
        <button
          className="secondary"
          type="button"
          disabled={blocked}
          onClick={() => void refresh()}
        >
          Повторить загрузку
        </button>
      )}
      {recoveryCodes.length > 0 && (
        <div className="admin-recovery-codes" role="status">
          <strong>Сохраните новые резервные коды</strong>
          <p>
            Они показываются один раз. Каждый код можно использовать только один
            раз; прежние коды больше не действуют.
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
          <p>
            Подключите приложение-аутентификатор. После настройки вы получите
            резервные коды для восстановления доступа.
          </p>
          <label>
            Пароль аккаунта
            <PasswordInput
              name="password"
              autoComplete="current-password"
              required
            />
          </label>
          <button className="primary" disabled={blocked}>
            {busy === "enroll" ? "Начинаем настройку…" : "Настроить MFA"}
          </button>
        </form>
      )}
      {enrollment && (
        <div className="admin-enrollment">
          {state?.enabled && (
            <Notice>
              Старый аутентификатор работает до подтверждения нового. После
              подтверждения прежний фактор и резервные коды перестанут
              действовать.
            </Notice>
          )}
          <div className="admin-enrollment-grid">
            <div className="admin-qr">
              <QRCodeSVG
                value={enrollment.otpauthUri}
                size={192}
                marginSize={4}
                level="M"
                bgColor="#ffffff"
                fgColor="#000000"
                title="QR-код настройки аутентификатора"
              />
            </div>
            <div>
              <h3>Добавьте аккаунт в аутентификатор</h3>
              <p>
                Отсканируйте QR-код. На этом устройстве можно открыть приложение
                по ссылке или ввести ключ вручную.
              </p>
              <a className="text-link" href={enrollment.otpauthUri}>
                Открыть аутентификатор <Icon name="arrow-up-right" />
              </a>
              <div className="admin-secret">
                <span>Ключ для ручного ввода</span>
                <code>{enrollment.secret}</code>
              </div>
              <p className="small muted">
                Настройка действует до {dateTime(enrollment.expiresAt)}.
              </p>
            </div>
          </div>
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
            <div className="admin-form-actions">
              <button className="primary" disabled={blocked}>
                {busy === "confirm" ? "Подтверждаем…" : "Подтвердить настройку"}
              </button>
              <button
                className="secondary"
                type="button"
                disabled={blocked}
                onClick={() => {
                  setEnrollment(null);
                  setError("");
                }}
              >
                Отменить настройку
              </button>
            </div>
          </form>
        </div>
      )}
      {state?.enabled && !enrollment && (
        <>
          <p className="admin-mfa-status">
            {hasReadAccess
              ? `Просмотр до ${dateTime(state.verifiedUntil)}.`
              : "Вход не подтверждён."}{" "}
            {hasWriteAccess
              ? `Изменения до ${dateTime(state.stepUpUntil)}.`
              : "Для изменений нужен новый код."}{" "}
            Резервных кодов: {state.recoveryCodesRemaining}.
          </p>
          {recoveredAccess && (
            <Notice>
              Вы вошли по резервному коду. Если аутентификатор недоступен,
              подтвердите действия и замените его в настройках защиты.
            </Notice>
          )}
          {(!hasReadAccess || showVerify) && (
            <form
              className="form-stack admin-mfa-verify"
              onSubmit={verify}
              key={useRecovery ? "recovery" : "totp"}
            >
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
              <div className="admin-form-actions">
                <button className="primary" disabled={blocked}>
                  {busy === "verify" ? "Подтверждаем…" : "Подтвердить вход"}
                </button>
                <button
                  className="quiet"
                  type="button"
                  disabled={blocked}
                  onClick={() => setUseRecovery((value) => !value)}
                >
                  {useRecovery
                    ? "Использовать приложение"
                    : "Использовать резервный код"}
                </button>
              </div>
            </form>
          )}
          <details className="admin-mfa-settings">
            <summary>Настройки защиты</summary>
            {hasWriteAccess ? (
              <div className="admin-security-forms">
                <form className="form-stack" onSubmit={enroll}>
                  <h3>Заменить аутентификатор</h3>
                  <p>
                    Прежний фактор остаётся активным до подтверждения нового
                    кода.
                  </p>
                  <label>
                    Пароль аккаунта
                    <PasswordInput
                      name="password"
                      autoComplete="current-password"
                      required
                    />
                  </label>
                  <button className="secondary" disabled={blocked}>
                    Начать замену
                  </button>
                </form>
                <form className="form-stack" onSubmit={replaceCodes}>
                  <h3>Новые резервные коды</h3>
                  <p>После перевыпуска прежние коды перестанут работать.</p>
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
                  <button className="secondary" disabled={blocked}>
                    Создать новые коды
                  </button>
                </form>
              </div>
            ) : (
              <p>Сначала подтвердите действия новым кодом выше.</p>
            )}
          </details>
          <button
            className="quiet"
            type="button"
            disabled={blocked}
            onClick={() => void refresh()}
          >
            Обновить статус
          </button>
        </>
      )}
    </section>
  );
}
