"use client";
import { notify } from "@/shared/notifications";
import Link from "next/link";
import { RegistrationForm } from "./registration";
import { useRouter } from "next/navigation";
import { FormEvent, useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  errorMessage,
  oauthAuthorizationUrl,
  oauthCallbackMessage,
  safeAppReturnTo,
} from "@/shared/api";
import { Turnstile } from "@/shared/turnstile";
import {
  Loading,
  Notice,
  PasswordInput,
  Spinner,
  Icon,
  OAuthIcon,
} from "@/shared/ui";
export { PublicAuthLinks } from "./public-auth-links";
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
type OAuthProvider = "yandex" | "vk";
type Provider = { id: OAuthProvider; name: string; enabled: boolean };
function OAuthButtons() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void api<{ providers: Provider[] }>("/auth/oauth/providers", {
      signal: controller.signal,
    })
      .then((result) =>
        setProviders(result.providers.filter((item) => item.enabled)),
      )
      .catch((failure) => {
        if (!(failure instanceof DOMException && failure.name === "AbortError"))
          setError(errorMessage(failure));
      });
    return () => controller.abort();
  }, []);
  async function start(provider: OAuthProvider) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const returnTo = safeAppReturnTo(
        new URLSearchParams(window.location.search).get("returnTo"),
      );
      const result = await api<{ authorizationUrl: string }>(
        `/auth/oauth/${provider}/start`,
        {
          method: "POST",
          body: { mode: "login", ...(returnTo ? { returnTo } : {}) },
        },
      );
      window.location.replace(
        oauthAuthorizationUrl(provider, result.authorizationUrl),
      );
    } catch (failure) {
      notify.error(errorMessage(failure));
      setBusy(false);
    }
  }
  if (providers.length === 0 && !error) return null;
  return (
    <div className="oauth-options">
      {providers.length > 0 && (
        <>
          <p className="oauth-divider">или продолжить через</p>
          <div className="oauth-buttons">
            {providers.map((provider) => (
              <button
                key={provider.id}
                className="secondary"
                type="button"
                disabled={busy}
                onClick={() => void start(provider.id)}
              >
                <OAuthIcon provider={provider.id} />
                {provider.name}
              </button>
            ))}
          </div>
        </>
      )}
      {error && <Notice>{error}</Notice>}
    </div>
  );
}
export function AuthEntry({ mode }: { mode: "login" | "register" }) {
  const router = useRouter();
  const [showForm, setShowForm] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let controller: AbortController | undefined;
    let timer: number | undefined;
    const checkSession = (restored = false) => {
      controller?.abort();
      window.clearTimeout(timer);
      const params = new URLSearchParams(window.location.search);
      const account = params.get("account");
      if (
        mode === "login" &&
        (params.get("oauthError") ||
          account === "deleted" ||
          account === "unknown")
      ) {
        // Let the form display one-time action results before clearing the URL.
        setShowForm(true);
        return;
      }
      if (restored) {
        setShowForm(false);
        setError("");
      }
      const request = new AbortController();
      controller = request;
      const deadline = window.setTimeout(() => {
        if (controller !== request || request.signal.aborted) return;
        request.abort();
        setError(
          "Проверка входа заняла слишком много времени. Повторите попытку.",
        );
      }, 10_000);
      timer = deadline;
      void api("/me", { signal: request.signal })
        .then(() => {
          if (controller === request && !request.signal.aborted)
            router.replace(safeAppReturnTo(params.get("returnTo")) ?? "/app");
        })
        .catch((failure) => {
          if (controller !== request || request.signal.aborted) return;
          if (failure instanceof ApiError && failure.status === 401)
            setShowForm(true);
          else setError(errorMessage(failure));
        })
        .finally(() => window.clearTimeout(deadline));
    };
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) checkSession(true);
    };
    window.addEventListener("pageshow", restored);
    checkSession();
    return () => {
      window.removeEventListener("pageshow", restored);
      controller?.abort();
      window.clearTimeout(timer);
    };
  }, [mode, router, attempt]);
  if (showForm) return <AuthForm mode={mode} />;
  return error ? (
    <div className="form-stack">
      <Notice>{error}</Notice>
      <button
        type="button"
        onClick={() => {
          setError("");
          setAttempt((value) => value + 1);
        }}
      >
        Повторить
      </button>
    </div>
  ) : (
    <Loading text="Проверяем вход…" />
  );
}
export function AuthForm({ mode }: { mode: AuthMode }) {
  const [returnTo, setReturnTo] = useState<string | null>(null);
  useEffect(() => {
    const values = new URLSearchParams(window.location.search).getAll(
      "returnTo",
    );
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Auth stage links retain only the reviewed browser destination.
    setReturnTo(values.length === 1 ? safeAppReturnTo(values[0]) : null);
  }, []);
  const loginHref = returnTo
    ? `/login?${new URLSearchParams({ returnTo })}`
    : "/login";
  return mode === "register" ? (
    <RegistrationForm oauth={<OAuthButtons />} loginHref={loginHref} />
  ) : (
    <LegacyAuthForm mode={mode} returnTo={returnTo} />
  );
}
function LegacyAuthForm({
  mode,
  returnTo,
}: {
  mode: AuthMode;
  returnTo: string | null;
}) {
  const cfg = config[mode];
  const authHref = (path: string) =>
    returnTo ? `${path}?${new URLSearchParams({ returnTo })}` : path;
  const router = useRouter();
  const [token, setToken] = useState("");
  const [linkReady, setLinkReady] = useState(false);
  const [captchaToken, setCaptchaToken] = useState("");
  const [resetKey, setResetKey] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [accountResult, setAccountResult] = useState<
    "deleted" | "unknown" | null
  >(null);
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
      const values = new URLSearchParams(window.location.search).getAll(
        "returnTo",
      );
      const target = values.length === 1 ? safeAppReturnTo(values[0]) : null;
      window.history.replaceState(
        null,
        "",
        window.location.pathname +
          (target ? `?${new URLSearchParams({ returnTo: target })}` : ""),
      );
    }
    // The fragment is available only after hydration and is immediately removed from the URL.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLinkReady(true);
  }, [isLink]);
  useEffect(() => {
    if (mode !== "login") return;
    const url = new URL(window.location.href);
    const code = url.searchParams.get("oauthError");
    const account = url.searchParams.get("account");
    const accountOutcome =
      account === "deleted" || account === "unknown" ? account : null;
    if (!code && !accountOutcome) return;
    // Capture one-time action results before removing them from the browser URL.
    if (code) notify.error(oauthCallbackMessage(code));
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (accountOutcome) setAccountResult(accountOutcome);
    url.searchParams.delete("oauthError");
    url.searchParams.delete("account");
    const destinations = url.searchParams.getAll("returnTo");
    const returnTo =
      destinations.length === 1 ? safeAppReturnTo(destinations[0]) : null;
    window.history.replaceState(
      null,
      "",
      returnTo ? `/login?${new URLSearchParams({ returnTo })}` : "/login",
    );
  }, [mode]);
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
        const returnTo = new URLSearchParams(window.location.search).get(
          "returnTo",
        );
        router.replace(safeAppReturnTo(returnTo) ?? "/app");
        router.refresh();
      } else {
        setDone(true);
        setToken("");
      }
    } catch (e) {
      notify.error(errorMessage(e));
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
            <Icon name="check" size={24} />
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
          <Link href={authHref("/login")} replace className="button full">
            Перейти ко входу <Icon name="arrow-right" />
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
          {mode === "login" && accountResult && (
            <Notice success={accountResult === "deleted"}>
              {accountResult === "deleted"
                ? "Аккаунт удалён. Данные пространства обезличены; восстановление недоступно. При новой регистрации вы получите пустой аккаунт без прежней квоты."
                : "Результат удаления неизвестен. Запрос не повторяется автоматически. Войдите заново, чтобы проверить доступ. Если удаление завершилось, восстановить прежние данные нельзя."}
            </Notice>
          )}
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
            <Link
              href={authHref("/forgot-password")}
              replace
              className="small auth-forgot"
            >
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
                <Icon name="arrow-right" />
              </>
            )}
          </button>
          {isLink && !token && linkReady && (
            <Link
              href={authHref(
                mode === "verify" ? "/register" : "/forgot-password",
              )}
              replace
              className="button secondary full"
            >
              Запросить новую ссылку
            </Link>
          )}
        </form>
      )}
      {!done && (mode === "login" || mode === "register") && <OAuthButtons />}
      {!done && (
        <p className="auth-switch">
          {mode === "login" ? (
            <>
              Ещё нет аккаунта?{" "}
              <Link href={authHref("/register")} replace>
                Зарегистрироваться
              </Link>
            </>
          ) : mode === "register" ? (
            <>
              Уже есть аккаунт?{" "}
              <Link href={authHref("/login")} replace>
                Войти
              </Link>
            </>
          ) : (
            <Link href={authHref("/login")} replace>
              <Icon name="arrow-left" /> Вернуться ко входу
            </Link>
          )}
        </p>
      )}
    </>
  );
}

type OAuthRegistrationInfo = {
  provider: OAuthProvider;
  email: string;
  name: string;
  expiresAt: string;
};

export function OAuthRegistration() {
  const router = useRouter();
  const consumedLink = useRef(false);
  const requestVersion = useRef(0);
  const confirmation = useRef<AbortController | null>(null);
  const [registration, setRegistration] =
    useState<OAuthRegistrationInfo | null>(null);
  const [token, setToken] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!consumedLink.current) {
      consumedLink.current = true;
      const params = new URLSearchParams(window.location.hash.slice(1));
      // The email token is available only after hydration and is immediately removed from the URL.
      setToken(params.get("token") ?? "");
      if (window.location.hash)
        window.history.replaceState(
          null,
          "",
          window.location.pathname + window.location.search,
        );
    }
    let controller: AbortController | undefined;
    let timer: number | undefined;
    const load = (restored = false) => {
      controller?.abort();
      confirmation.current?.abort();
      const version = ++requestVersion.current;
      window.clearTimeout(timer);
      const request = new AbortController();
      controller = request;
      setLoading(true);
      setBusy(false);
      setError("");
      setRegistration(null);
      const current = () =>
        version === requestVersion.current && !request.signal.aborted;
      const deadline = window.setTimeout(() => {
        if (!current()) return;
        request.abort();
        setLoading(false);
        setError(
          "Проверка регистрации заняла слишком много времени. Повторите попытку.",
        );
      }, 10_000);
      timer = deadline;
      const check = async () => {
        if (restored) {
          try {
            await api("/me", { signal: request.signal });
            if (current()) {
              const targets = new URLSearchParams(
                window.location.search,
              ).getAll("returnTo");
              router.replace(
                (targets.length === 1 && safeAppReturnTo(targets[0])) || "/app",
              );
            }
            return;
          } catch (failure) {
            if (!current()) return;
            if (!(failure instanceof ApiError && failure.status === 401))
              throw failure;
          }
        }
        const result = await api<OAuthRegistrationInfo>(
          "/auth/oauth/registration",
          {
            signal: request.signal,
          },
        );
        if (current()) setRegistration(result);
      };
      void check()
        .catch((failure) => {
          if (current()) setError(errorMessage(failure));
        })
        .finally(() => {
          window.clearTimeout(deadline);
          if (current()) setLoading(false);
        });
    };
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) load(true);
    };
    window.addEventListener("pageshow", restored);
    load(attempt > 0);
    return () => {
      controller?.abort();
      confirmation.current?.abort();
      window.clearTimeout(timer);
      window.removeEventListener("pageshow", restored);
    };
  }, [router, attempt]);
  async function confirm() {
    if (!token || busy || loading) return;
    const version = ++requestVersion.current;
    const controller = new AbortController();
    confirmation.current = controller;
    const current = () =>
      version === requestVersion.current && !controller.signal.aborted;
    setBusy(true);
    setError("");
    try {
      const result = await api<{ returnTo: string }>(
        "/auth/oauth/registration/confirm",
        { method: "POST", body: { token }, signal: controller.signal },
      );
      if (!current()) return;
      setToken("");
      router.replace(safeAppReturnTo(result.returnTo) ?? "/app");
      router.refresh();
    } catch (failure) {
      if (!current()) return;
      notify.error(errorMessage(failure));
      setBusy(false);
    }
  }
  return (
    <>
      <div className="auth-heading">
        <p className="eyebrow">HEARTBEAT CHECKER</p>
        <h1>Подтвердите email</h1>
        <p>
          Для регистрации через Яндекс или VK подтвердите адрес из письма в этом
          браузере. Аккаунт появится только после подтверждения.
        </p>
      </div>
      {error && (
        <div className="form-stack">
          <Notice>{error}</Notice>
          <button
            type="button"
            onClick={() => setAttempt((value) => value + 1)}
          >
            Повторить проверку
          </button>
        </div>
      )}
      {loading ? (
        <div className="auth-done">
          <Spinner /> Проверяем регистрацию…
        </div>
      ) : registration ? (
        <div className="form-stack">
          <p className="small">
            Письмо отправлено на <strong>{registration.email}</strong>.
            Проверьте также папку «Спам» и откройте ссылку в этом браузере.
          </p>
          {token ? (
            <button
              className="primary full"
              disabled={busy}
              onClick={() => void confirm()}
            >
              {busy ? "Подтверждаем…" : "Подтвердить email и войти"}
            </button>
          ) : (
            <p className="small muted">
              Откройте ссылку из письма, чтобы продолжить.
            </p>
          )}
        </div>
      ) : (
        <p className="small muted">
          Начните вход через провайдера заново. Если ссылка устарела, новое
          письмо будет отправлено при следующей регистрации.
        </p>
      )}
      <p className="auth-switch">
        <Link href="/login" replace>
          <Icon name="arrow-left" /> Вернуться ко входу
        </Link>
      </p>
    </>
  );
}
