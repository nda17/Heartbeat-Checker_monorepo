(() => {
  "use strict";

  const source = new URL(window.location.href);
  window.history.replaceState(null, "", source.pathname);

  const status = document.getElementById("status");
  const open = document.getElementById("open");
  const retry = document.getElementById("retry");
  const fallback = document.getElementById("fallback");
  const params = source.searchParams;
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  const nonce = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;
  const actions = new Set([
    "login",
    "register",
    "reset",
    "registration_confirm",
    "registration_resend",
  ]);
  const packageName = "ru.heartbeatchecker.app";

  function exactKeys(keys) {
    return (
      params.size === keys.length &&
      keys.every((key) => params.getAll(key).length === 1)
    );
  }

  function valid(value, pattern) {
    return typeof value === "string" && pattern.test(value);
  }

  function showFallback(message) {
    status.textContent = message;
    fallback.hidden = false;
  }

  function offer(intent, message) {
    status.textContent = message;
    open.hidden = false;
    open.addEventListener("click", () => window.location.assign(intent));
    fallback.hidden = false;
  }

  function intent(path, query = "", extras = "") {
    return `intent://heartbeat-checker.ru${path}${query}#Intent;scheme=https;package=${packageName};${extras}end`;
  }

  function challenge() {
    const action = params.get("action");
    const requestId = params.get("requestId");
    if (!exactKeys(["action", "requestId"]) || !actions.has(action) || !valid(requestId, nonce)) {
      showFallback("Запрос проверки недействителен. Начните действие заново в приложении.");
      return;
    }

    const bridge = window.ReactNativeWebView;
    function post(type, detail) {
      if (!bridge || typeof bridge.postMessage !== "function") return;
      bridge.postMessage(JSON.stringify({ type, requestId, action, ...detail }));
    }
    const siteKey = document.currentScript?.dataset.siteKey || document.querySelector('script[data-site-key]')?.dataset.siteKey;
    if (!siteKey) {
      status.textContent = "Проверка безопасности недоступна. Повторите попытку в приложении.";
      post("turnstile_error", { code: "unavailable" });
      return;
    }

    let widget = null;
    let waitTimer = null;
    let deadlineTimer = null;
    function fail(code, message) {
      status.textContent = message;
      retry.hidden = false;
      post("turnstile_error", { code });
    }
    function render() {
      if (!window.turnstile) return false;
      clearInterval(waitTimer);
      clearTimeout(deadlineTimer);
      status.textContent = "Выполните проверку безопасности.";
      widget = window.turnstile.render(document.getElementById("widget"), {
        sitekey: siteKey,
        action,
        theme: "auto",
        size: "flexible",
        language: "ru",
        "response-field": false,
        retry: "never",
        "refresh-expired": "never",
        "refresh-timeout": "never",
        callback(token) {
          if (typeof token !== "string" || !token || token.length > 2048) {
            fail("failed", "Проверка не удалась. Повторите попытку.");
            return;
          }
          status.textContent = "Проверка завершена. Возвращаемся в приложение…";
          retry.hidden = true;
          post("turnstile", { token });
        },
        "expired-callback"() {
          fail("expired", "Время проверки истекло. Повторите попытку.");
        },
        "timeout-callback"() {
          fail("timeout", "Время проверки истекло. Повторите попытку.");
        },
        "error-callback"() {
          fail("failed", "Проверка не удалась. Повторите попытку.");
        },
      });
      return true;
    }
    function waitForProvider() {
      if (render()) return;
      status.textContent = "Загружаем проверку безопасности…";
      waitTimer = window.setInterval(render, 100);
      deadlineTimer = window.setTimeout(() => {
        clearInterval(waitTimer);
        fail("unavailable", "Проверка недоступна. Проверьте соединение и повторите попытку.");
      }, 15000);
    }
    retry.addEventListener("click", () => {
      retry.hidden = true;
      if (widget !== null && window.turnstile) {
        window.turnstile.reset(widget);
        status.textContent = "Выполните проверку безопасности.";
      } else {
        waitForProvider();
      }
    });
    waitForProvider();
  }

  if (source.pathname === "/mobile/challenge") {
    challenge();
    return;
  }

  if (source.pathname === "/android/auth/complete") {
    const flowId = params.get("flowId");
    const state = params.get("state");
    const code = params.get("code");
    const deviceId = params.get("deviceId");
    const error = params.get("error");
    const baseValid = valid(flowId, uuid) && valid(state, nonce);
    if (!baseValid) {
      showFallback("Возврат устарел. Начните вход заново в приложении.");
      return;
    }
    if (error !== null) {
      if (!exactKeys(["flowId", "state", "error"]) || !["oauth_cancelled", "oauth_provider_unavailable"].includes(error)) {
        showFallback("Не удалось завершить вход. Начните его заново в приложении.");
        return;
      }
      const query = `?flowId=${flowId}&state=${state}&error=${error}`;
      offer(intent(source.pathname, query), "Вход не завершён. Вернитесь в приложение и повторите попытку.");
      return;
    }
    if (!exactKeys(deviceId === null ? ["flowId", "state", "code"] : ["flowId", "state", "code", "deviceId"]) ||
      typeof code !== "string" || !code || code.length > 8192 ||
      (deviceId !== null && (!deviceId || deviceId.length > 4096))) {
      showFallback("Возврат недействителен. Начните вход заново в приложении.");
      return;
    }
    const query = `?flowId=${flowId}&state=${state}&code=${encodeURIComponent(code)}${deviceId === null ? "" : `&deviceId=${encodeURIComponent(deviceId)}`}`;
    offer(intent(source.pathname, query), "Продолжите вход в приложении.");
    return;
  }

  if (source.pathname === "/android/auth/reset" || source.pathname === "/android/auth/verify") {
    const reset = source.pathname.endsWith("/reset");
    const token = new URLSearchParams(source.hash.slice(1)).get("token");
    const hash = new URLSearchParams(source.hash.slice(1));
    if ((!reset && (!exactKeys(["oauth"]) || params.get("oauth") !== "1")) ||
      (reset && params.size !== 0) || hash.size !== 1 || !valid(token, nonce)) {
      showFallback("Ссылка недействительна. Запросите новое письмо в приложении.");
      return;
    }
    offer(intent(source.pathname, reset ? "" : "?oauth=1", `S.token=${token};`),
      reset ? "Продолжите восстановление пароля в приложении." : "Подтвердите адрес в приложении.");
    return;
  }

  if (source.pathname === "/android/payments/return") {
    const order = params.get("order");
    if (!exactKeys(["order"]) || !valid(order, uuid)) {
      showFallback("Откройте приложение и проверьте свои транзакции.");
      return;
    }
    offer(intent(source.pathname, `?order=${order.toLowerCase()}`),
      "Проверьте статус заказа в приложении. Возврат не подтверждает оплату.");
    fallback.href = `/app/billing?order=${order.toLowerCase()}`;
    fallback.textContent = "Проверить на сайте";
  }
})();
