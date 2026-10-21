import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";
import { createRequire } from "node:module";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import ts from "typescript";

const requireWeb = createRequire(new URL("../package.json", import.meta.url));
const challengeOrigin = "https://heartbeat-checker.ru";

function flattenStyle(style) {
  if (!style) return {};
  if (Array.isArray(style))
    return Object.assign({}, ...style.map(flattenStyle));
  return typeof style === "object" ? style : {};
}

function createNativeMocks() {
  const backHandlers = new Set();
  const BackHandler = {
    addEventListener(_event, listener) {
      backHandlers.add(listener);
      return { remove: () => backHandlers.delete(listener) };
    },
  };
  const component = (tag) => {
    function MockNativeComponent(props) {
      const {
        children,
        style,
        contentContainerStyle,
        accessibilityLabel,
        accessibilityRole,
        accessibilityState,
        ...nativeProps
      } = props;
      delete nativeProps.keyboardShouldPersistTaps;
      const css = flattenStyle(style ?? contentContainerStyle);
      const attrs = {
        ...(accessibilityLabel ? { "aria-label": accessibilityLabel } : {}),
        ...(accessibilityRole ? { role: accessibilityRole } : {}),
        ...(accessibilityState?.disabled ? { "aria-disabled": "true" } : {}),
        ...(css.minHeight !== undefined
          ? { "data-min-height": String(css.minHeight) }
          : {}),
        style: css,
        ...nativeProps,
      };
      return React.createElement(tag, attrs, children);
    }
    MockNativeComponent.displayName = `Mock${tag}`;
    return MockNativeComponent;
  };
  const TextInput = (props) => {
    const {
      value,
      onChangeText,
      secureTextEntry,
      accessibilityLabel,
      keyboardType,
      ...inputProps
    } = props;
    delete inputProps.autoCapitalize;
    delete inputProps.autoCorrect;
    delete inputProps.placeholderTextColor;
    return React.createElement("input", {
      ...inputProps,
      "aria-label": accessibilityLabel,
      type: secureTextEntry ? "password" : "text",
      inputMode: keyboardType === "email-address" ? "email" : undefined,
      value,
      onInput: (event) => onChangeText?.(event.target.value),
    });
  };
  const Pressable = ({
    children,
    onPress,
    disabled,
    accessibilityLabel,
    accessibilityRole,
    accessibilityState,
    ...props
  }) =>
    React.createElement(
      "button",
      {
        ...props,
        type: "button",
        onClick: onPress,
        disabled,
        "aria-label": accessibilityLabel,
        "aria-pressed": accessibilityState?.selected,
        role: accessibilityRole,
      },
      children,
    );
  const Image = ({ accessibilityLabel, ...props }) =>
    React.createElement("img", { ...props, alt: accessibilityLabel ?? "" });
  const WebView = React.forwardRef(function MockWebView(props, ref) {
    React.useImperativeHandle(
      ref,
      () => ({
        stopLoading() {
          props.onStopLoading?.();
        },
      }),
      [props],
    );
    globalThis.__authWebViewProps = props;
    return React.createElement("div", {
      "data-testid": "auth-webview",
      "data-uri": props.source?.uri,
    });
  });
  return {
    reactNative: {
      BackHandler,
      Image,
      KeyboardAvoidingView: component("section"),
      Pressable,
      ScrollView: component("div"),
      StyleSheet: { create: (styles) => styles },
      Text: component("span"),
      TextInput,
      View: component("div"),
      useColorScheme: () => "light",
    },
    webView: { WebView },
    backHandler: BackHandler,
    pressBack() {
      for (const listener of [...backHandlers]) listener();
    },
  };
}

function compile(relativePath, aliases, runtime = {}) {
  const source = fs.readFileSync(
    new URL(relativePath, import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
  const fixtureModule = { exports: {} };
  const exported = fixtureModule.exports;
  const runtimeRequire = runtime.require;
  const runtimeValues = { ...runtime };
  delete runtimeValues.require;
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: exported,
    require(id) {
      if (Object.hasOwn(aliases, id)) return aliases[id];
      if (runtimeRequire) return runtimeRequire(id);
      return requireWeb(id);
    },
    console,
    URL,
    Date,
    Promise,
    ...runtimeValues,
  });
  return fixtureModule.exports;
}

async function createHarness({
  resetToken,
  tokens = ["nonce_1", "nonce_2"],
  onAuthed = async () => {},
} = {}) {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "https://heartbeat-checker.ru/login",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.__authWebViewProps = null;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const mocks = createNativeMocks();
  const requests = [];
  const native = {
    API_ORIGIN: challengeOrigin,
    CHALLENGE_ORIGIN: challengeOrigin,
    authEpoch: async () => 0,
    randomToken: async () => tokens.shift() ?? "nonce_extra",
    restoreRegistration: async () => null,
    saveRegistration: async () => {},
    clearRegistration: async () => {},
    ...mocks.native,
  };
  let fetchImpl = async (url, options) => {
    requests.push({ url, options });
    return {
      ok: true,
      status: 200,
      headers: { get: () => "application/json" },
      json: async () => ({ account: {}, session: {} }),
    };
  };
  const apiModule = compile(
    "../../mobile/src/api.ts",
    { "./native": { native }, "./types": {} },
    {
      fetch: (...args) => fetchImpl(...args),
    },
  );
  const authModule = compile(
    "../../mobile/src/AuthScreen.tsx",
    {
      "react-native": mocks.reactNative,
      "react-native-webview": mocks.webView,
      "./api": apiModule,
      "./native": { native },
      "./types": {},
      "./ui": compile("../../mobile/src/ui.tsx", {
        "react-native": mocks.reactNative,
      }),
    },
    {
      require(id) {
        if (id.endsWith(".png")) return { uri: id };
        return requireWeb(id);
      },
    },
  );
  await act(async () =>
    root.render(
      React.createElement(authModule.AuthScreen, { onAuthed, resetToken }),
    ),
  );
  await act(async () => Promise.resolve());
  return {
    dom,
    host,
    requests,
    native,
    apiModule,
    pressBack: mocks.pressBack,
    setFetch(fn) {
      fetchImpl = fn;
    },
    async click(label) {
      const button = [...host.querySelectorAll("button")].find(
        (item) =>
          item.getAttribute("aria-label") === label ||
          item.textContent?.trim() === label,
      );
      assert.ok(button, `button not found: ${label}`);
      await act(async () => button.click());
    },
    async fill(label, value) {
      const input = host.querySelector(`input[aria-label="${label}"]`);
      assert.ok(input, `input not found: ${label}`);
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(
          dom.window.HTMLInputElement.prototype,
          "value",
        ).set;
        setter.call(input, value);
        input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
        input.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
      });
    },
    async flush() {
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      delete globalThis.window;
      delete globalThis.document;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
      delete globalThis.__authWebViewProps;
    },
  };
}

function loadAndroidBridge() {
  const source = fs.readFileSync(
    new URL("../src/shared/android-bridge/index.ts", import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  const fixtureModule = { exports: {} };
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: fixtureModule.exports,
    Response,
    process: { env: { NEXT_PUBLIC_TURNSTILE_SITE_KEY: "test-site-key" } },
  });
  return fixtureModule.exports.androidBridge;
}

function fakeClock(window) {
  let now = 0;
  let nextId = 1;
  const timers = new Map();
  window.setTimeout = (callback, delay = 0) => {
    const id = nextId++;
    timers.set(id, { callback, due: now + delay, interval: 0 });
    return id;
  };
  window.setInterval = (callback, delay = 0) => {
    const id = nextId++;
    timers.set(id, { callback, due: now + delay, interval: delay || 1 });
    return id;
  };
  window.clearTimeout = (id) => timers.delete(id);
  window.clearInterval = (id) => timers.delete(id);
  return {
    advance(milliseconds) {
      const end = now + milliseconds;
      while (true) {
        const due = [...timers.entries()]
          .filter(([, timer]) => timer.due <= end)
          .sort((left, right) => left[1].due - right[1].due)[0];
        if (!due) break;
        const [id, timer] = due;
        now = timer.due;
        if (timer.interval) timer.due += timer.interval;
        else timers.delete(id);
        timer.callback();
      }
      now = end;
    },
  };
}

async function createWebChallenge({
  url,
  state = null,
  turnstile = null,
} = {}) {
  const response = loadAndroidBridge()("/mobile/challenge");
  const html = await response.text();
  const dom = new JSDOM(html, {
    url:
      url ??
      `https://heartbeat-checker.ru/mobile/challenge?action=login&requestId=${"A".repeat(43)}`,
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  if (state)
    dom.window.history.replaceState(state, "", dom.window.location.href);
  const messages = [];
  const clock = fakeClock(dom.window);
  dom.window.ReactNativeWebView = {
    postMessage: (message) => messages.push(JSON.parse(message)),
  };
  if (turnstile) dom.window.turnstile = turnstile;
  dom.window.eval(
    fs.readFileSync(
      new URL("../public/android-bridge.js", import.meta.url),
      "utf8",
    ),
  );
  return { dom, response, messages, clock };
}

test("auth screens expose password visibility and preserve the shared card height", async () => {
  const login = await createHarness();
  try {
    assert.equal(
      login.host.querySelector('input[aria-label="Пароль"]').type,
      "password",
    );
    assert.equal(
      login.host.querySelector('[aria-label="Показать пароль"]') !== null,
      true,
    );
    assert.equal(
      login.host.querySelector('[data-min-height="520"]') !== null,
      true,
    );
    await login.click("Показать пароль");
    assert.equal(
      login.host.querySelector('input[aria-label="Пароль"]').type,
      "text",
    );
    assert.equal(
      login.host.querySelector('[aria-label="Скрыть пароль"]') !== null,
      true,
    );
    await login.click("Регистрация");
    assert.ok(login.host.textContent.includes("Регистрация"));
    assert.ok(login.host.querySelector('input[aria-label="Имя"]'));
    assert.ok(login.host.querySelector('[data-min-height="520"]'));
    await login.click("Ко входу");
    await login.click("Забыли пароль?");
    assert.ok(login.host.textContent.includes("Восстановление доступа"));
    assert.ok(login.host.querySelector('[data-min-height="520"]'));
  } finally {
    await login.unmount();
  }

  const reset = await createHarness({ resetToken: "reset-token" });
  try {
    assert.ok(reset.host.textContent.includes("Новый пароль"));
    assert.equal(
      reset.host.querySelector('input[aria-label="Новый пароль"]').type,
      "password",
    );
    assert.ok(reset.host.querySelector('[data-min-height="520"]'));
  } finally {
    await reset.unmount();
  }
});

test("API errors distinguish transport, backend, and native bridge failures", async () => {
  const harness = await createHarness();
  try {
    harness.setFetch(async () => {
      throw new Error("offline");
    });
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Не удалось подключиться. Проверьте сеть и повторите.",
        );
        return true;
      },
    );
    harness.setFetch(async () => ({
      ok: false,
      status: 401,
      headers: { get: () => "application/json" },
      json: async () => ({ error: "invalid_credentials" }),
    }));
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Неверный email или пароль.",
        );
        return true;
      },
    );
    harness.native.authEpoch = async () => {
      throw new Error("native bridge rejected");
    };
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Ошибка приложения. Перезапустите его и повторите действие.",
        );
        return true;
      },
    );
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA navigation is confined to the current challenge and provider resources", async () => {
  const harness = await createHarness();
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const props = globalThis.__authWebViewProps;
    assert.ok(props);
    assert.equal(
      props.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=nonce_1`,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({ url: props.source.uri }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: `${challengeOrigin}/mobile/challenge`,
      }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com/turnstile/v0/api.js",
      }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com.evil.test/x",
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com/x",
        isTopFrame: true,
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: `${challengeOrigin}/mobile/challenge#fragment`,
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "http://challenges.cloudflare.com/x",
      }),
      false,
    );
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA accepts one fresh, matching, main-frame token and submits it once", async () => {
  const authed = [];
  const harness = await createHarness({
    tokens: ["fresh_request_nonce_1", "fresh_request_nonce_2"],
    onAuthed: async (value) => authed.push(value),
  });
  try {
    harness.setFetch(async (url, options) => {
      harness.requests.push({ url, options });
      return {
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        json: async () => ({ account: {}, session: {} }),
      };
    });
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const props = globalThis.__authWebViewProps;
    const valid = {
      type: "turnstile",
      token: "cloudflare-token-long-enough",
      action: "login",
      requestId: "fresh_request_nonce_1",
    };
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify(valid),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify(valid),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(harness.requests.length, 1);
    assert.equal(
      harness.requests[0].url,
      `${challengeOrigin}/api/v1/auth/native/login`,
    );
    assert.deepEqual(JSON.parse(harness.requests[0].options.body), {
      email: "person@example.com",
      password: "correct horse battery",
      turnstileToken: "cloudflare-token-long-enough",
    });
    assert.equal(
      harness.host.querySelector('[data-testid="auth-webview"]'),
      null,
    );
    assert.equal(authed.length, 1);
  } finally {
    await harness.unmount();
  }
});

test("callbacks from a retried CAPTCHA generation cannot submit or invalidate the current attempt", async () => {
  const harness = await createHarness({
    tokens: ["old_request_nonce", "new_request_nonce"],
  });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const oldAttempt = globalThis.__authWebViewProps;
    await act(async () =>
      oldAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile_error",
            code: "timeout",
            action: "login",
            requestId: "old_request_nonce",
          }),
          url: challengeOrigin,
          isMainFrame: true,
        },
      }),
    );
    await harness.click("Повторить проверку");
    await harness.flush();
    const newAttempt = globalThis.__authWebViewProps;
    assert.equal(
      newAttempt.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=new_request_nonce`,
    );

    await act(async () =>
      oldAttempt.onNavigationStateChange({
        url: "https://evil.test/late-navigation",
      }),
    );
    await act(async () =>
      oldAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "old-token-long-enough",
            action: "login",
            requestId: "old_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(
      globalThis.__authWebViewProps.source.uri,
      newAttempt.source.uri,
    );
    assert.equal(harness.requests.length, 0);

    await act(async () =>
      globalThis.__authWebViewProps.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "new-token-long-enough",
            action: "login",
            requestId: "new_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(harness.requests.length, 1);
    assert.equal(
      JSON.parse(harness.requests[0].options.body).turnstileToken,
      "new-token-long-enough",
    );
  } finally {
    await harness.unmount();
  }
});

test("Android back press fences a CAPTCHA callback synchronously before cancellation", async () => {
  const harness = await createHarness({ tokens: ["back_request_nonce"] });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const cancelledAttempt = globalThis.__authWebViewProps;
    await act(async () => {
      harness.pressBack();
      cancelledAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "late-token-long-enough",
            action: "login",
            requestId: "back_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      });
    });
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Вход"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA rejects mismatched source/action/request/frame and offers a fresh retry", async () => {
  const harness = await createHarness({
    tokens: [
      "request_one_nonce",
      "request_two_nonce",
      "request_three_nonce",
      "request_four_nonce",
    ],
  });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    let props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "register",
            requestId: "request_one_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    assert.equal(
      props.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=request_two_nonce`,
    );
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "stale_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "request_three_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: false,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "request_four_nonce",
          }),
          url: "https://evil.test/",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA handles malformed/provider errors, navigation escapes, and cancellation", async () => {
  const harness = await createHarness();
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    let props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: "{",
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(
      harness.host.textContent.includes("Некорректный ответ проверки."),
    );
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile_error",
            code: "timeout",
            action: "login",
            requestId: "nonce_2",
          }),
          url: challengeOrigin,
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не завершилась"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onNavigationStateChange({ url: "https://evil.test/redirect" }),
    );
    await harness.flush();
    assert.ok(
      harness.host.textContent.includes("Проверка открыла другую страницу"),
    );
    await harness.click("Отмена");
    assert.ok(harness.host.textContent.includes("Вход"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("challenge page renders compact markup without browser fallback controls", async () => {
  const challenge = await createWebChallenge();
  try {
    assert.equal(challenge.response.status, 200);
    const { document } = challenge.dom.window;
    assert.ok(document.querySelector("main.challenge"));
    assert.ok(document.querySelector("#widget"));
    assert.equal(document.querySelector("h1"), null);
    assert.equal(document.querySelector("#open"), null);
    assert.equal(document.querySelector("#fallback"), null);
    assert.equal(document.querySelectorAll("button").length, 1);
    assert.equal(document.querySelector("#retry").hidden, true);
    assert.equal(
      challenge.dom.window.getComputedStyle(document.querySelector("#retry"))
        .display,
      "none",
    );
  } finally {
    challenge.dom.window.close();
  }
});

test("challenge bridge binds action and nonce, scrubs query, restores history, and consumes a token once", async () => {
  const requestId = "A".repeat(43);
  const optionsList = [];
  const removed = [];
  const turnstile = {
    render(_element, options) {
      optionsList.push(options);
      return optionsList.length;
    },
    remove(id) {
      removed.push(id);
    },
  };
  const first = await createWebChallenge({
    turnstile,
    url: `https://heartbeat-checker.ru/mobile/challenge?action=register&requestId=${requestId}`,
  });
  try {
    assert.equal(first.dom.window.location.pathname, "/mobile/challenge");
    assert.equal(first.dom.window.location.search, "");
    assert.deepEqual(
      JSON.parse(JSON.stringify(first.dom.window.history.state)),
      {
        androidChallenge: {
          path: "/mobile/challenge",
          action: "register",
          requestId,
        },
      },
    );
    assert.equal(optionsList.length, 1);
    assert.equal(optionsList[0].action, "register");
    assert.equal(optionsList[0].sitekey, "test-site-key");
    const savedState = JSON.parse(
      JSON.stringify(first.dom.window.history.state),
    );
    first.dom.window.close();

    const second = await createWebChallenge({
      state: savedState,
      url: "https://heartbeat-checker.ru/mobile/challenge",
      turnstile,
    });
    try {
      assert.equal(optionsList.length, 2);
      assert.equal(optionsList[1].action, "register");
      optionsList[1].callback("turnstile-token-long-enough");
      optionsList[1].callback("duplicate-token-long-enough");
      assert.equal(second.messages.length, 1);
      assert.deepEqual(second.messages[0], {
        type: "turnstile",
        requestId,
        action: "register",
        token: "turnstile-token-long-enough",
      });
      assert.deepEqual(removed, [2]);
      assert.equal(
        second.dom.window.document.querySelector("#retry").hidden,
        true,
      );
    } finally {
      second.dom.window.close();
    }
  } finally {
    first.dom.window.close();
  }
});

test("challenge bridge rejects fragments and duplicate parameters without creating a widget", async (t) => {
  const requestId = "A".repeat(43);
  for (const [name, query] of [
    ["fragment", `?action=login&requestId=${requestId}#unexpected`],
    [
      "duplicate action",
      `?action=login&action=register&requestId=${requestId}`,
    ],
    [
      "duplicate request id",
      `?action=login&requestId=${requestId}&requestId=${requestId}`,
    ],
  ]) {
    await t.test(name, async () => {
      let renderCount = 0;
      const challenge = await createWebChallenge({
        url: `https://heartbeat-checker.ru/mobile/challenge${query}`,
        turnstile: {
          render() {
            renderCount++;
            return 1;
          },
        },
      });
      try {
        assert.equal(renderCount, 0);
        assert.ok(
          challenge.dom.window.document
            .querySelector("#status")
            .textContent.includes("недействителен"),
        );
        assert.equal(challenge.dom.window.history.state, null);
        assert.equal(challenge.messages.length, 0);
      } finally {
        challenge.dom.window.close();
      }
    });
  }
});

test("challenge provider timeout is bounded and retry can attach a late provider", async () => {
  let renderCount = 0;
  const provider = {
    render(_element, options) {
      renderCount++;
      this.options = options;
      return 7;
    },
    reset() {},
  };
  const challenge = await createWebChallenge();
  try {
    challenge.clock.advance(15_000);
    assert.ok(
      challenge.dom.window.document
        .querySelector("#status")
        .textContent.includes("Проверка недоступна"),
    );
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      false,
    );
    assert.deepEqual(challenge.messages.at(-1), {
      type: "turnstile_error",
      requestId: "A".repeat(43),
      action: "login",
      code: "unavailable",
    });
    challenge.dom.window.turnstile = provider;
    challenge.dom.window.document.querySelector("#retry").click();
    assert.equal(renderCount, 1);
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      true,
    );
    provider.options["timeout-callback"]();
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      false,
    );
    challenge.dom.window.document.querySelector("#retry").click();
    provider.options.callback("turnstile-token-long-enough");
    assert.equal(challenge.messages.at(-1).type, "turnstile");
    assert.equal(
      challenge.messages.filter((message) => message.type === "turnstile")
        .length,
      1,
    );
  } finally {
    challenge.dom.window.close();
  }
});
