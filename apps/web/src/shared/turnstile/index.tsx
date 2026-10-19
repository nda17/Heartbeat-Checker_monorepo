"use client";
import Script from "next/script";
import { useEffect, useRef, useState } from "react";
import { useTheme } from "@/shared/theme";
type TurnstileAPI = {
  render: (container: HTMLElement, options: Record<string, unknown>) => string;
  remove: (id: string) => void;
  reset: (id: string) => void;
};
declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}
export function Turnstile({
  action,
  resetKey,
  onToken,
}: {
  action:
    | "register"
    | "login"
    | "reset"
    | "registration_confirm"
    | "registration_resend";
  resetKey: number;
  onToken: (token: string) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const theme = useTheme();
  const onTokenRef = useRef(onToken);
  const widgetRef = useRef<string | null>(null);
  const previousResetKey = useRef(resetKey);
  useEffect(() => {
    onTokenRef.current = onToken;
  }, [onToken]);
  useEffect(() => {
    // A new widget, including a theme change, always requires a fresh token.
    onTokenRef.current("");
    if (!ready || !ref.current || !window.turnstile) return;
    const key =
      process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY ?? "1x00000000000000000000AA";
    // Validate the real browser hostname before mounting a provider widget.
    if (
      !["localhost", "127.0.0.1", "[::1]"].includes(location.hostname) &&
      /^[123]x0/.test(key)
    ) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setError("Проверка безопасности не настроена для этого домена.");
      return;
    }
    let active = true;
    const widget = window.turnstile.render(ref.current, {
      sitekey: key,
      action,
      theme,
      size: "flexible",
      language: "ru",
      "response-field": false,
      retry: "never",
      "refresh-expired": "auto",
      "refresh-timeout": "never",
      callback: (token: string) => {
        if (!active) return;
        setError("");
        onTokenRef.current(token);
      },
      "expired-callback": () => {
        if (active) onTokenRef.current("");
      },
      "error-callback": (code: string) => {
        if (!active) return;
        onTokenRef.current("");
        setError(
          `Не удалось выполнить проверку безопасности.${/^\d{5,6}$/.test(code) ? ` Код: ${code}.` : ""} Повторите попытку или проверьте соединение.`,
        );
      },
      "timeout-callback": () => {
        if (!active) return;
        onTokenRef.current("");
        setError("Время проверки безопасности истекло. Повторите попытку.");
      },
    });
    widgetRef.current = widget;
    return () => {
      active = false;
      widgetRef.current = null;
      window.turnstile?.remove(widget);
    };
  }, [ready, action, theme]);
  useEffect(() => {
    if (resetKey === previousResetKey.current) return;
    previousResetKey.current = resetKey;
    onTokenRef.current("");
    if (widgetRef.current) window.turnstile?.reset(widgetRef.current);
  }, [resetKey]);
  function retry() {
    onTokenRef.current("");
    setError("");
    if (widgetRef.current) window.turnstile?.reset(widgetRef.current);
    else window.location.reload();
  }
  return (
    <div className="captcha">
      <Script
        src="https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit"
        onReady={() => setReady(true)}
        onError={() =>
          setError(
            "Проверка безопасности недоступна. Проверьте соединение и обновите страницу.",
          )
        }
      />
      <div ref={ref} />
      {!ready && !error && (
        <p className="small muted" role="status">
          Загружаем проверку безопасности…
        </p>
      )}
      {error && (
        <>
          <p className="field-error" role="alert">
            {error}
          </p>
          <button type="button" className="quiet" onClick={retry}>
            Повторить проверку
          </button>
        </>
      )}
    </div>
  );
}
