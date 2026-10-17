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
  action: "register" | "login" | "reset";
  resetKey: number;
  onToken: (token: string) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const theme = useTheme();
  useEffect(() => {
    // A new widget, including a theme change, always requires a fresh token.
    onToken("");
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
    const widget = window.turnstile.render(ref.current, {
      sitekey: key,
      action,
      theme,
      size: "flexible",
      language: "ru",
      "response-field": false,
      callback: (token: string) => {
        setError("");
        onToken(token);
      },
      "expired-callback": () => {
        onToken("");
        window.turnstile?.reset(widget);
      },
      "error-callback": () => {
        onToken("");
        setError(
          "Не удалось загрузить проверку безопасности. Обновите страницу или проверьте соединение.",
        );
      },
      "timeout-callback": () => {
        onToken("");
        window.turnstile?.reset(widget);
      },
    });
    return () => {
      window.turnstile?.remove(widget);
    };
  }, [ready, action, resetKey, onToken, theme]);
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
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
