"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  FormEvent,
  ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  api,
  ApiError,
  boundedRequest,
  errorMessage,
  safeAppReturnTo,
} from "@/shared/api";
import {
  captureEmailCode,
  EmailCodeState,
  normalizeEmailCode,
  useEmailCodeClock,
} from "@/shared/email-code";
import { Turnstile } from "@/shared/turnstile";
import { dateTime, Loading, Notice, PasswordInput, Spinner } from "@/shared/ui";

type RegistrationResponse = { challenge: unknown };
export function RegistrationForm({ oauth }: { oauth?: ReactNode }) {
  const router = useRouter();
  const appMode = (process.env.WEB_MODE ?? "app") === "app";
  const [ready, setReady] = useState(!appMode);
  const [reading, setReading] = useState(false);
  const [state, setState] = useState<EmailCodeState | null>(null);
  const challenge = state?.challenge;
  const snapshot = useRef<EmailCodeState | null>(null);
  const [code, setCode] = useState("");
  const [captcha, setCaptcha] = useState("");
  const [resetKey, setResetKey] = useState(0);
  const [resendMode, setResendMode] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [notice, setNotice] = useState("");
  const [uncertain, setUncertain] = useState(false);
  const uncertainConfirm = useRef(false);
  const uncertainOperation = useRef(false);
  const editing = useRef(false);
  const [draft, setDraft] = useState({ name: "", email: "" });
  const [rateUntil, setRateUntil] = useState(0);
  const clock = useEmailCodeClock(state);
  const rateSeconds = Math.max(0, Math.ceil((rateUntil - clock.now) / 1000));
  const resendSeconds = Math.max(clock.resendSeconds, rateSeconds);
  const operation = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const alive = useRef(false);
  const goToApp = useCallback(() => {
    const returnTo = new URLSearchParams(window.location.search).get(
      "returnTo",
    );
    router.replace(safeAppReturnTo(returnTo) ?? "/app");
    router.refresh();
  }, [router]);
  const accept = useCallback((value: unknown, clear = false) => {
    const next = value === null ? null : captureEmailCode(value);
    if (clear || next?.challenge.id !== snapshot.current?.challenge.id) {
      setCode("");
      setCaptcha("");
      setResendMode(false);
      setResetKey((key) => key + 1);
    }
    snapshot.current = next;
    setState(next);
  }, []);
  const restore = useCallback(
    async (checkSession = false) => {
      if (operation.current || !alive.current) return;
      const controller = new AbortController();
      operation.current = controller;
      const view = ++generation.current;
      setReading(true);
      try {
        const result = await boundedRequest(controller, async (signal) => {
          if (checkSession) {
            try {
              await api("/me", { signal });
              return { authenticated: true, challenge: null };
            } catch (cause) {
              if (!(cause instanceof ApiError) || cause.status !== 401)
                throw cause;
            }
          }
          return {
            authenticated: false,
            ...(await api<RegistrationResponse>("/auth/registration", {
              signal,
            })),
          };
        });
        if (
          !alive.current ||
          operation.current !== controller ||
          generation.current !== view ||
          controller.signal.aborted
        )
          return;
        if (result.authenticated) {
          goToApp();
          return;
        }
        accept(result.challenge);
        setReadError("");
        if (result.challenge !== null) {
          uncertainConfirm.current = false;
          if (uncertainOperation.current)
            setNotice(
              "Попытка найдена. Используйте последнее письмо; запрос не повторялся автоматически.",
            );
          uncertainOperation.current = false;
          setUncertain(false);
        } else if (checkSession || uncertainOperation.current) {
          setUncertain(true);
          setNotice(
            "Не удалось однозначно подтвердить результат. Проверьте вход или войдите в аккаунт. Запрос подтверждения не повторяется автоматически.",
          );
        }
      } catch (cause) {
        if (
          alive.current &&
          operation.current === controller &&
          generation.current === view
        )
          setReadError(
            controller.signal.aborted
              ? "Не удалось проверить состояние вовремя. Повторите проверку."
              : errorMessage(cause),
          );
      } finally {
        if (operation.current === controller) {
          operation.current = null;
          if (alive.current && generation.current === view) {
            setReading(false);
            setReady(true);
          }
        }
      }
    },
    [accept, goToApp],
  );
  useEffect(() => {
    alive.current = true;
    const refresh = () => {
      if (appMode && document.visibilityState !== "hidden" && !editing.current)
        void restore(uncertainConfirm.current);
    };
    if (appMode) refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("pageshow", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      alive.current = false;
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the latest operation when leaving this attempt.
      ++generation.current;
      operation.current?.abort();
      operation.current = null;
      window.removeEventListener("focus", refresh);
      window.removeEventListener("pageshow", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [appMode, restore]);
  function changeEmail() {
    ++generation.current;
    operation.current?.abort();
    operation.current = null;
    editing.current = true;
    uncertainConfirm.current = false;
    uncertainOperation.current = false;
    if (challenge)
      setDraft((current) => ({ ...current, email: challenge.email }));
    accept(null, true);
    setReady(true);
    setReading(false);
    setBusy("");
    setError("");
    setReadError("");
    setNotice("");
    setUncertain(false);
    setRateUntil(0);
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (operation.current || reading || uncertain || !captcha) return;
    const kind = challenge ? (resendMode ? "resend" : "confirm") : "start";
    if (challenge?.status && challenge.status !== "pending") return;
    if (kind === "resend" && resendSeconds > 0) return;
    if (rateSeconds > 0) return;
    const normalized = normalizeEmailCode(code);
    if (kind === "confirm" && !normalized) {
      setError("Введите шесть цифр из последнего письма. Пробелы допустимы.");
      return;
    }
    const form = event.currentTarget;
    const fields = new FormData(form);
    const body =
      kind === "start"
        ? {
            name: String(fields.get("name") ?? ""),
            email: String(fields.get("email") ?? ""),
            password: String(fields.get("password") ?? ""),
            turnstileToken: captcha,
          }
        : kind === "confirm"
          ? { code: normalized!, turnstileToken: captcha }
          : { turnstileToken: captcha };
    if (
      "password" in body &&
      new TextEncoder().encode(body.password).length > 1024
    ) {
      setError("Пароль слишком длинный: максимум 1024 байта.");
      return;
    }
    if ("email" in body)
      setDraft({
        name: String(body.name ?? ""),
        email: String(body.email ?? ""),
      });
    const controller = new AbortController();
    operation.current = controller;
    const view = ++generation.current;
    setBusy(kind);
    setError("");
    setNotice("");
    setCaptcha("");
    let reconcile = false;
    let checkSession = false;
    try {
      const path =
        kind === "start"
          ? "/auth/registration"
          : `/auth/registration/${encodeURIComponent(challenge!.id)}/${kind}`;
      const response = await boundedRequest(controller, (signal) =>
        api<{ challenge?: unknown; account?: { id: string } }>(path, {
          method: "POST",
          body,
          signal,
        }),
      );
      if (
        !alive.current ||
        operation.current !== controller ||
        generation.current !== view ||
        controller.signal.aborted
      )
        return;
      if (kind === "confirm") {
        if (!response?.account?.id)
          throw new Error("Invalid registration response");
        form.reset();
        goToApp();
      } else {
        if (!response?.challenge)
          throw new Error("Invalid registration response");
        if (kind === "start") form.reset();
        editing.current = false;
        setRateUntil(0);
        setUncertain(false);
        accept(response.challenge, true);
        setNotice(
          "Если регистрация доступна для этого адреса, код поставлен в очередь. Проверьте почту и папку «Спам». Используйте последнее письмо.",
        );
      }
    } catch (cause) {
      if (
        !alive.current ||
        operation.current !== controller ||
        generation.current !== view
      )
        return;
      const unknown =
        controller.signal.aborted ||
        !(cause instanceof ApiError) ||
        cause.status === 0 ||
        cause.status >= 500;
      if (unknown) {
        setUncertain(true);
        setNotice(
          "Ответ на запрос не получен. Проверяем состояние; запрос не повторяется автоматически.",
        );
        reconcile = true;
        uncertainOperation.current = true;
        checkSession = kind === "confirm";
        uncertainConfirm.current = checkSession;
      } else {
        setError(errorMessage(cause));
        if (cause.status === 429)
          setRateUntil(
            Date.now() + Math.max(60, cause.retryAfterSeconds ?? 60) * 1000,
          );
        reconcile = kind !== "start" && cause.status !== 409;
      }
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        if (alive.current && generation.current === view) {
          setBusy("");
          setResetKey((key) => key + 1);
        }
      }
    }
    if (reconcile && alive.current) await restore(checkSession);
  }
  const terminal = challenge?.status && challenge.status !== "pending";
  return (
    <>
      <div className="auth-heading">
        <p className="eyebrow">ВАШЕ РАБОЧЕЕ ПРОСТРАНСТВО</p>
        <h1>{challenge ? "Введите код из письма" : "Начните с аккаунта"}</h1>
        <p>
          {challenge
            ? `Подтверждение адреса ${challenge.email}`
            : "Создайте своё рабочее пространство для мониторинга сайтов и серверов."}
        </p>
      </div>
      {error && <Notice>{error}</Notice>}
      {readError && <Notice>{readError}</Notice>}
      {notice && (
        <p className="small" role="status">
          {notice}
        </p>
      )}
      {!ready ? (
        <Loading text="Проверяем незавершённую регистрацию…" />
      ) : terminal ? (
        <div className="form-stack">
          <p>
            {challenge.status === "completed"
              ? "Аккаунт создан. Войдите в него, если вход не завершился автоматически."
              : challenge.status === "locked"
                ? "Лимит неверных кодов исчерпан. Начните регистрацию заново."
                : "Срок этой попытки истёк. Начните регистрацию заново."}
          </p>
          <button type="button" className="secondary" onClick={changeEmail}>
            Начать заново
          </button>
          <Link className="button" href="/login">
            Войти
          </Link>
        </div>
      ) : (
        <form className="form-stack" onSubmit={submit}>
          {challenge ? (
            <>
              <p className="field-hint">
                Код действует до {dateTime(challenge.expiresAt)}. Повторная
                отправка не продлевает срок и отменяет прежний код. Допустимо не
                более пяти неверных попыток.
              </p>
              {!resendMode && (
                <label>
                  Код из письма
                  <input
                    name="code"
                    value={code}
                    onChange={(event) => setCode(event.target.value)}
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    maxLength={32}
                    required
                    disabled={!!busy}
                  />
                </label>
              )}
            </>
          ) : (
            <>
              <label>
                Ваше имя
                <input
                  name="name"
                  autoComplete="name"
                  minLength={2}
                  maxLength={100}
                  defaultValue={draft.name}
                  required
                  placeholder="Александр"
                />
              </label>
              <label>
                Email
                <input
                  name="email"
                  type="email"
                  autoComplete="email"
                  maxLength={254}
                  defaultValue={draft.email}
                  required
                  placeholder="you@company.ru"
                />
              </label>
              <label>
                Пароль
                <PasswordInput
                  name="password"
                  autoComplete="new-password"
                  minLength={12}
                  maxLength={1024}
                  required
                />
                <span className="field-hint">
                  Не менее 12 символов. Можно использовать парольную фразу.
                </span>
              </label>
            </>
          )}
          {!uncertain && (
            <Turnstile
              action={
                challenge
                  ? resendMode
                    ? "registration_resend"
                    : "registration_confirm"
                  : "register"
              }
              resetKey={resetKey}
              onToken={setCaptcha}
            />
          )}
          <button
            className="primary full"
            disabled={
              !!busy ||
              reading ||
              !!readError ||
              uncertain ||
              !captcha ||
              rateSeconds > 0 ||
              (resendMode && resendSeconds > 0)
            }
          >
            {busy ? (
              <>
                <Spinner />
                Отправляем…
              </>
            ) : challenge ? (
              resendMode ? (
                "Отправить новый код"
              ) : (
                "Подтвердить email и войти"
              )
            ) : (
              "Получить код"
            )}
          </button>
          {challenge && (
            <>
              <button
                type="button"
                className="quiet"
                disabled={
                  !!busy ||
                  reading ||
                  uncertain ||
                  (!resendMode && resendSeconds > 0)
                }
                onClick={() => {
                  setResendMode((value) => !value);
                  setCaptcha("");
                  setResetKey((value) => value + 1);
                }}
              >
                {resendMode
                  ? "Вернуться к вводу кода"
                  : `Отправить повторно${resendSeconds > 0 ? ` (${resendSeconds} с)` : ""}`}
              </button>
              <button
                type="button"
                className="quiet"
                disabled={!!busy}
                onClick={changeEmail}
              >
                Изменить email
              </button>
            </>
          )}
        </form>
      )}
      {(readError || uncertain) && (
        <div className="button-row">
          <button
            type="button"
            className="secondary"
            disabled={reading || !!busy}
            onClick={() => void restore(uncertainConfirm.current)}
          >
            {reading ? "Проверяем…" : "Проверить состояние"}
          </button>
          <Link href="/login">Войти в аккаунт</Link>
          {!challenge && (
            <button type="button" className="quiet" onClick={changeEmail}>
              Начать заново
            </button>
          )}
        </div>
      )}
      {!challenge && !uncertain && oauth}
      <p className="auth-switch">
        Уже есть аккаунт? <Link href="/login">Войти</Link>
      </p>
    </>
  );
}
