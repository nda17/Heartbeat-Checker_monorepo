"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { FormEvent, useEffect, useRef, useState } from "react";
import { api, errorMessage } from "@/shared/api";
import { Turnstile } from "@/shared/turnstile";
import { Notice, PasswordInput, Spinner } from "@/shared/ui";
export type AuthMode =
  "login" | "register" | "verify" | "reset-request" | "reset-confirm";
const config = {
  login: {
    title: "С возвращением",
    text: "Войдите, чтобы ваши сервисы снова были в поле зрения.",
    button: "Войти в кабинет",
    endpoint: "/auth/login",
  },
  register: {
    title: "Начните с аккаунта",
    text: "Создайте своё рабочее пространство для мониторинга сайтов и API.",
    button: "Создать аккаунт",
    endpoint: "/auth/register",
  },
  verify: {
    title: "Подтвердите email",
    text: "Введите пароль, указанный при регистрации. Это завершит создание аккаунта.",
    button: "Подтвердить email",
    endpoint: "/auth/verify",
  },
  "reset-request": {
    title: "Восстановить доступ",
    text: "Отправим на почту ссылку для установки нового пароля.",
    button: "Отправить ссылку",
    endpoint: "/auth/password/reset/request",
  },
  "reset-confirm": {
    title: "Новый пароль",
    text: "Придумайте новый пароль. После сохранения все прежние сессии завершатся.",
    button: "Сохранить пароль",
    endpoint: "/auth/password/reset/confirm",
  },
};
export function AuthForm({ mode }: { mode: AuthMode }) {
  const cfg = config[mode];
  const router = useRouter();
  const [token, setToken] = useState("");
  const [linkReady, setLinkReady] = useState(false);
  const [captchaToken, setCaptchaToken] = useState("");
  const [resetKey, setResetKey] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  const consumedLink = useRef(false);
  const isLink = mode === "verify" || mode === "reset-confirm";
  const captcha =
    mode === "register" || mode === "login" || mode === "reset-request";
  useEffect(() => {
    if (isLink && !consumedLink.current) {
      consumedLink.current = true;
      const params = new URLSearchParams(window.location.hash.slice(1));
      setToken(params.get("token") ?? "");
      window.history.replaceState(null, "", window.location.pathname);
    }
    // The fragment is available only after hydration and is immediately removed from the URL.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLinkReady(true);
  }, [isLink]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    const form = event.currentTarget;
    const fields = new FormData(form);
    if (captcha && !captchaToken) {
      setError("Дождитесь завершения проверки безопасности.");
      return;
    }
    const password = fields.get("password");
    if (
      typeof password === "string" &&
      new TextEncoder().encode(password).length > 1024
    ) {
      setError("Пароль слишком длинный: максимум 1024 байта.");
      return;
    }
    setBusy(true);
    setError("");
    const body: Record<string, string> = {};
    for (const key of ["name", "email", "password"]) {
      const value = fields.get(key);
      if (typeof value === "string") body[key] = value;
    }
    if (isLink) body.token = token;
    if (captcha) {
      body.turnstileToken = captchaToken;
      setCaptchaToken("");
    }
    try {
      await api(cfg.endpoint, { method: "POST", body });
      form.reset();
      if (mode === "login") {
        router.replace("/app");
        router.refresh();
      } else {
        setDone(true);
        setToken("");
      }
    } catch (e) {
      setError(errorMessage(e));
      if (captcha) setResetKey((v) => v + 1);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <div className="auth-heading">
        <p className="eyebrow">
          {mode === "register"
            ? "ВАШЕ РАБОЧЕЕ ПРОСТРАНСТВО"
            : "HEARTBEAT CHECKER"}
        </p>
        <h1>{cfg.title}</h1>
        <p>{cfg.text}</p>
      </div>
      {done ? (
        <div className="auth-done">
          <span className="success-icon" aria-hidden="true">
            ✓
          </span>
          <h2>
            {mode === "register" || mode === "reset-request"
              ? "Проверьте почту"
              : "Готово"}
          </h2>
          <p>
            {mode === "register"
              ? "Если регистрация доступна для этого адреса, вы получите письмо со ссылкой. Для подтверждения понадобится указанный пароль."
              : mode === "reset-request"
                ? "Если аккаунт с таким email существует, письмо со ссылкой уже отправлено. Проверьте также папку «Спам»."
                : mode === "verify"
                  ? "Email подтверждён. Теперь можно войти в свой кабинет."
                  : "Пароль обновлён. Войдите с новым паролем."}
          </p>
          <Link href="/login" className="button full">
            Перейти ко входу <span aria-hidden="true">→</span>
          </Link>
          {(mode === "register" || mode === "reset-request") && (
            <button
              className="quiet full"
              onClick={() => {
                setDone(false);
              }}
            >
              Отправить ещё раз
            </button>
          )}
        </div>
      ) : (
        <form onSubmit={submit} className="form-stack">
          {error && <Notice>{error}</Notice>}
          {isLink && linkReady && !token && (
            <Notice>
              В ссылке нет токена. Откройте полную ссылку из письма или
              запросите новую. После обновления этой страницы нужно снова
              открыть письмо.
            </Notice>
          )}
          {mode === "register" && (
            <label>
              Ваше имя
              <input
                name="name"
                autoComplete="name"
                minLength={2}
                maxLength={100}
                required
                placeholder="Александр"
              />
            </label>
          )}
          {!isLink && (
            <label>
              Email
              <input
                name="email"
                type="email"
                autoComplete="email"
                maxLength={254}
                required
                placeholder="you@company.ru"
              />
            </label>
          )}
          {mode !== "reset-request" && (
            <label>
              {mode === "reset-confirm" ? "Новый пароль" : "Пароль"}
              <PasswordInput
                name="password"
                autoComplete={
                  mode === "register" || mode === "reset-confirm"
                    ? "new-password"
                    : "current-password"
                }
                minLength={
                  mode === "login" || mode === "verify" ? undefined : 12
                }
                required
                maxLength={1024}
              />
              {(mode === "register" || mode === "reset-confirm") && (
                <span className="field-hint">
                  Не менее 12 символов. Можно использовать парольную фразу.
                </span>
              )}
            </label>
          )}
          {mode === "login" && (
            <Link href="/forgot-password" className="small auth-forgot">
              Забыли пароль?
            </Link>
          )}
          {captcha && (
            <Turnstile
              action={
                mode === "reset-request"
                  ? "reset"
                  : mode === "register"
                    ? "register"
                    : "login"
              }
              resetKey={resetKey}
              onToken={setCaptchaToken}
            />
          )}
          <button
            className="primary full"
            disabled={
              busy ||
              (captcha && !captchaToken) ||
              (isLink && (!linkReady || !token))
            }
          >
            {busy ? (
              <>
                <Spinner />
                Отправляем…
              </>
            ) : (
              <>
                {cfg.button}
                <span aria-hidden="true">→</span>
              </>
            )}
          </button>
          {isLink && !token && linkReady && (
            <Link
              href={mode === "verify" ? "/register" : "/forgot-password"}
              className="button secondary full"
            >
              Запросить новую ссылку
            </Link>
          )}
        </form>
      )}
      {!done && (
        <p className="auth-switch">
          {mode === "login" ? (
            <>
              Ещё нет аккаунта? <Link href="/register">Зарегистрироваться</Link>
            </>
          ) : mode === "register" ? (
            <>
              Уже есть аккаунт? <Link href="/login">Войти</Link>
            </>
          ) : (
            <Link href="/login">← Вернуться ко входу</Link>
          )}
        </p>
      )}
    </>
  );
}
