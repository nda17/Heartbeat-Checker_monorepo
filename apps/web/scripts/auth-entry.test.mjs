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

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function createHarness(url = "http://localhost/login") {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: dom.window.navigator,
  });
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.FormData = dom.window.FormData;
  globalThis.DOMException = dom.window.DOMException;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  return {
    dom,
    host,
    async render(element) {
      await act(async () => root.render(element));
      await act(async () => Promise.resolve());
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      delete globalThis.window;
      delete globalThis.document;
      delete globalThis.navigator;
      delete globalThis.HTMLElement;
      delete globalThis.Event;
      delete globalThis.FormData;
      delete globalThis.DOMException;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
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
  const exports = {};
  const fixtureModule = { exports };
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports,
    require(id) {
      if (Object.hasOwn(aliases, id)) return aliases[id];
      return requireWeb(id);
    },
    console,
    Date,
    TextEncoder,
    URL,
    URLSearchParams,
    AbortController,
    DOMException: globalThis.DOMException,
    window: globalThis.window,
    document: globalThis.document,
    FormData: globalThis.FormData,
    ...runtime,
  });
  return fixtureModule.exports;
}

function loadSafeAppReturnTo() {
  const source = fs.readFileSync(
    new URL("../src/shared/api/index.ts", import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  const exports = {};
  vm.runInNewContext(compiled, { exports, module: { exports }, URL });
  return exports.safeAppReturnTo;
}

function authEntry(api, routes, options = {}) {
  class ApiError extends Error {
    constructor(status, code) {
      super(code);
      this.status = status;
      this.code = code;
    }
  }
  const aliases = {
    "./public-auth-links": { PublicAuthLinks: () => null },
    "next/link": ({ href, className, children }) =>
      React.createElement("a", { href, className }, children),
    "next/navigation": { useRouter: () => routes },
    "@/shared/api": {
      api,
      ApiError,
      errorMessage: (error) =>
        error?.code === "temporarily_unavailable"
          ? "Сервис временно недоступен. Попробуйте немного позже."
          : error?.code === "network_error"
            ? "Не удалось связаться с сервером. Проверьте соединение и повторите попытку."
            : "Не удалось выполнить действие. Попробуйте ещё раз.",
      safeAppReturnTo: loadSafeAppReturnTo(),
      oauthCallbackMessage: (code) =>
        code === "oauth_cancelled"
          ? "Вход через провайдера отменён. Вы можете попробовать снова."
          : "Не удалось завершить вход через провайдера. Попробуйте ещё раз.",
      oauthAuthorizationUrl: (_provider, value) => value,
    },
    "@/shared/turnstile": {
      Turnstile: () =>
        React.createElement("div", { "data-testid": "turnstile" }),
    },
    "@/shared/ui": {
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children, success }) =>
        React.createElement(
          "div",
          { role: "alert", "data-success": success ? "true" : undefined },
          children,
        ),
      PasswordInput: (props) =>
        React.createElement("input", { ...props, type: "password" }),
      Spinner: () => React.createElement("span", null, "…"),
    },
  };
  return {
    ...compile("../src/features/auth/index.tsx", aliases, options),
    ApiError,
  };
}

function publicAuthLinks(api, timers = { setTimeout, clearTimeout }) {
  class ApiError extends Error {
    constructor(status, code) {
      super(code);
      this.status = status;
      this.code = code;
    }
  }
  const component = compile(
    "../src/features/auth/public-auth-links.tsx",
    {
      "next/link": ({ href, className, children }) =>
        React.createElement("a", { href, className }, children),
      "@/shared/api": { api, ApiError },
      "@/shared/ui": {
        Arrow: () =>
          React.createElement("span", { "aria-hidden": "true" }, "→"),
      },
    },
    timers,
  );
  return { ...component, ApiError };
}

function apiStub(handler, calls) {
  return async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/auth/oauth/providers") return { providers: [] };
    if (path !== "/me") throw new Error(`Unexpected API call: ${path}`);
    return handler(options);
  };
}

async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

test("a valid session opens the requested reviewed app destination", async () => {
  const harness = createHarness(
    "http://localhost/login?returnTo=%2Fapp%2Fsettings",
  );
  const request = deferred();
  const calls = [];
  const routes = { replace: (path) => calls.push({ route: path }) };
  const { AuthEntry } = authEntry(
    apiStub(() => request.promise, calls),
    routes,
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  assert.match(harness.host.textContent, /Проверяем вход/);
  assert.equal(calls.filter((call) => call.path === "/me").length, 1);
  assert.ok(calls.find((call) => call.path === "/me").options.signal);
  await act(async () => request.resolve({}));
  assert.deepEqual(
    calls.filter((call) => call.route),
    [{ route: "/app/settings" }],
  );
  await harness.unmount();
});

test("an untrusted returnTo falls back to /app", async () => {
  const harness = createHarness(
    "http://localhost/register?returnTo=https%3A%2F%2Fevil.example%2F",
  );
  const routes = [];
  const { AuthEntry } = authEntry(
    apiStub(async () => ({}), []),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "register" }));
  await settle();
  assert.deepEqual(routes, ["/app"]);
  assert.ok(routes.every((path) => !/^https?:/i.test(path)));
  await harness.unmount();
});

test("401 keeps the login form available without redirecting", async () => {
  const harness = createHarness();
  const routes = [];
  const calls = [];
  const { AuthEntry, ApiError } = authEntry(
    apiStub(async () => {
      throw new ApiError(401, "unauthorized");
    }, calls),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  await settle();
  assert.ok(
    harness.host.querySelector('form input[name="email"]'),
    harness.host.textContent,
  );
  assert.ok(harness.host.querySelector('form input[name="password"]'));
  assert.deepEqual(routes, []);
  assert.equal(calls.filter((call) => call.path === "/me").length, 1);
  await harness.unmount();
});

for (const [label, code] of [
  ["503", "temporarily_unavailable"],
  ["network failure", "network_error"],
]) {
  test(`${label} can be retried and a later session check succeeds`, async () => {
    const harness = createHarness();
    const routes = [];
    let attempts = 0;
    const calls = [];
    const { AuthEntry, ApiError } = authEntry(
      apiStub(async () => {
        attempts += 1;
        if (attempts === 1)
          throw new ApiError(code === "network_error" ? 0 : 503, code);
        return {};
      }, calls),
      { replace: (path) => routes.push(path) },
    );
    await harness.render(React.createElement(AuthEntry, { mode: "login" }));
    await settle();
    assert.match(harness.host.textContent, /Попробуйте|повторите попытку/i);
    const retry = [...harness.host.querySelectorAll("button")].find(
      (button) => button.textContent.trim() === "Повторить",
    );
    assert.ok(retry, "retry button is visible after a temporary failure");
    await act(async () => retry.click());
    await settle();
    assert.equal(calls.filter((call) => call.path === "/me").length, 2);
    assert.deepEqual(routes, ["/app"]);
    await harness.unmount();
  });
}

test("unmount aborts the pending session check and its late result cannot redirect", async () => {
  const harness = createHarness();
  const request = deferred();
  const routes = [];
  let signal;
  const { AuthEntry } = authEntry(
    apiStub((options) => {
      signal = options.signal;
      return request.promise;
    }, []),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  assert.ok(signal);
  await harness.unmount();
  assert.equal(signal.aborted, true);
  request.resolve({});
  await settle();
  assert.deepEqual(routes, []);
});

for (const [query, expected] of [
  ["account=deleted", /Аккаунт удалён/],
  ["account=unknown", /Результат удаления неизвестен/],
  ["oauthError=oauth_cancelled", /Вход через провайдера отменён/],
]) {
  test(`${query} keeps its notice and bypasses automatic session routing`, async () => {
    const harness = createHarness(
      `http://localhost/login?${query}&returnTo=%2Fapp`,
    );
    const calls = [];
    const routes = [];
    const { AuthEntry } = authEntry(
      apiStub(async () => ({}), calls),
      { replace: (path) => routes.push(path) },
    );
    await harness.render(React.createElement(AuthEntry, { mode: "login" }));
    await settle();
    assert.match(harness.host.textContent, expected);
    assert.ok(harness.host.querySelector('form input[name="email"]'));
    assert.equal(calls.filter((call) => call.path === "/me").length, 0);
    assert.deepEqual(routes, []);
    assert.equal(
      new URL(harness.dom.window.location.href).searchParams.get("returnTo"),
      "/app",
    );
    await harness.unmount();
  });
}

test("frontend preview auth page renders the form without checking a session", async () => {
  const harness = createHarness();
  const calls = [];
  const { AuthEntry, AuthForm } = authEntry(
    apiStub(async () => ({}), calls),
    { replace() {} },
  );
  const page = compile(
    "../src/_pages/auth/index.tsx",
    {
      "next/link": ({ href, children }) =>
        React.createElement("a", { href }, children),
      "@/features/auth": { AuthEntry, AuthForm, OAuthRegistration: () => null },
      "@/features/theme-toggle": { ThemeToggle: () => null },
      "@/shared/ui": {
        Brand: () => null,
        FooterLegalNotice: () => null,
      },
    },
    { process: { env: { WEB_MODE: "frontend" } } },
  );
  await harness.render(React.createElement(page.AuthPage, { mode: "login" }));
  await settle();
  assert.ok(harness.host.querySelector('form input[name="email"]'));
  assert.equal(calls.filter((call) => call.path === "/me").length, 0);
  await harness.unmount();
});

function runThemeInit({
  standalone = false,
  referrer = "",
  storedMarker = null,
  denySessionStorage = false,
} = {}) {
  const domOptions = { url: "https://heartbeat-checker.test/" };
  if (referrer) domOptions.referrer = referrer;
  const dom = new JSDOM(
    "<!doctype html><html><body></body></html>",
    domOptions,
  );
  const values = new Map();
  if (storedMarker !== null)
    values.set("heartbeat-installed-app", storedMarker);
  const sessionStorage = {
    getItem(key) {
      if (denySessionStorage) throw new Error("storage is unavailable");
      return values.get(key) ?? null;
    },
    setItem(key, value) {
      if (denySessionStorage) throw new Error("storage is unavailable");
      values.set(key, String(value));
    },
    removeItem(key) {
      if (denySessionStorage) throw new Error("storage is unavailable");
      values.delete(key);
    },
  };
  const media = {
    matches: standalone,
    addEventListener: (type, listener) => {
      media.listener = type === "change" ? listener : undefined;
    },
  };
  dom.window.matchMedia = () => media;
  const source = fs.readFileSync(
    new URL("../public/theme-init.js", import.meta.url),
    "utf8",
  );
  vm.runInNewContext(source, {
    document: dom.window.document,
    window: dom.window,
    localStorage: dom.window.localStorage,
    sessionStorage,
  });
  return {
    dom,
    values,
    media,
    changeStandalone(matches) {
      media.matches = matches;
      media.listener?.({ matches });
    },
    close: () => dom.window.close(),
  };
}

test("theme init leaves an ordinary browser unmarked and marks standalone mode", () => {
  const browser = runThemeInit();
  assert.equal(
    browser.dom.window.document.documentElement.dataset.installedApp,
    "false",
  );
  assert.equal(browser.values.has("heartbeat-installed-app"), false);
  browser.close();

  const standalone = runThemeInit({ standalone: true });
  assert.equal(
    standalone.dom.window.document.documentElement.dataset.installedApp,
    "true",
  );
  assert.equal(standalone.values.get("heartbeat-installed-app"), "1");
  standalone.close();
});

test("theme init trusts only the exact Android app referrer, with optional trailing slash", () => {
  for (const referrer of [
    "android-app://ru.heartbeatchecker.app",
    "android-app://ru.heartbeatchecker.app/",
  ]) {
    const accepted = runThemeInit({ referrer });
    assert.equal(
      accepted.dom.window.document.documentElement.dataset.installedApp,
      "true",
    );
    accepted.close();
  }

  for (const referrer of [
    "android-app://ru.other.app/",
    "android-app://ru.heartbeatchecker.app.attacker/",
    "https://ru.heartbeatchecker.app/",
    "android-app://ru.heartbeatchecker.app/path",
  ]) {
    const rejected = runThemeInit({ referrer });
    assert.equal(
      rejected.dom.window.document.documentElement.dataset.installedApp,
      "false",
      referrer,
    );
    rejected.close();
  }
});

test("theme init keeps live standalone detection when session storage is blocked", () => {
  const result = runThemeInit({ standalone: true, denySessionStorage: true });
  assert.equal(
    result.dom.window.document.documentElement.dataset.installedApp,
    "true",
  );
  result.close();
});

test("theme init restores the installed marker saved for this tab", () => {
  const result = runThemeInit({ storedMarker: "1" });
  assert.equal(
    result.dom.window.document.documentElement.dataset.installedApp,
    "true",
  );
  assert.equal(result.values.get("heartbeat-installed-app"), "1");
  result.close();
});

test("theme init clears standalone-only context after leaving standalone mode", () => {
  const standalone = runThemeInit({ standalone: true });
  standalone.changeStandalone(false);
  assert.equal(
    standalone.dom.window.document.documentElement.dataset.installedApp,
    "false",
  );
  assert.equal(standalone.values.has("heartbeat-installed-app"), false);
  standalone.close();
});

test("theme init preserves a referrer-only context with a non-standalone display mode", () => {
  const referrer = runThemeInit({
    referrer: "android-app://ru.heartbeatchecker.app/",
  });
  assert.equal(
    referrer.dom.window.document.documentElement.dataset.installedApp,
    "true",
  );
  assert.equal(referrer.values.get("heartbeat-installed-app"), "1");
  referrer.close();
});

test("installation offers are tagged in the home navigation, footer, and Android details", async () => {
  const harness = createHarness("http://localhost/");
  const link = ({ href, className, children, ...props }) =>
    React.createElement("a", { href, className, ...props }, children);
  const ui = compile(
    "../src/shared/ui/index.tsx",
    {
      "next/link": link,
      "./legal-notice": { FooterLegalNotice: () => null },
      "./password-input": { PasswordInput: () => null },
    },
    { process: { env: { WEB_MODE: "app" } } },
  );
  const { HomePage } = compile(
    "../src/_pages/home/index.tsx",
    {
      "next/link": link,
      "@/features/auth": { PublicAuthLinks: () => null },
      "@/features/theme-toggle": { ThemeToggle: () => null },
      "@/shared/ui": ui,
      "./monitoring-demo": { MonitoringDemo: () => null },
    },
    { process: { env: { WEB_MODE: "app" } } },
  );
  await harness.render(React.createElement(HomePage));
  assert.ok(
    harness.host.querySelector(
      'nav[aria-label="Главная навигация"] a[href="/android"][data-install-promotion]',
    ),
  );
  assert.ok(
    harness.host.querySelector(
      "nav.landing-footer-install[data-install-promotion]",
    ),
  );

  const { default: AndroidPage } = compile("../app/android/page.tsx", {
    "next/image": (props) => {
      const imageProps = { ...props };
      delete imageProps.unoptimized;
      return React.createElement("img", imageProps);
    },
    "next/link": link,
    "@/shared/ui": ui,
    "@/features/theme-toggle": { ThemeToggle: () => null },
    "@/shared/seo": {
      publicPageMetadata: () => ({}),
      PublicPageStructuredData: () => null,
    },
    "@/shared/android": {
      androidRelease: () => ({
        versionName: "1.0.0",
        versionCode: 1,
        sizeBytes: 1024,
        releasedAt: "2026-01-01T00:00:00Z",
        sha256: "a".repeat(64),
        signingCertificateSha256: "b".repeat(64),
      }),
    },
  });
  await harness.render(React.createElement(AndroidPage));
  assert.ok(harness.host.querySelector("aside[data-install-promotion]"));
  assert.ok(harness.host.querySelector("details[data-install-promotion]"));
  await harness.unmount();
});

function queuedRequests() {
  const calls = [];
  const requests = [];
  const api = (path, options) => {
    calls.push({ path, options });
    const request = deferred();
    requests.push(request);
    return request.promise;
  };
  return { api, calls, requests };
}

test("public auth links start neutral and retain the cabinet link after a 200", async () => {
  const harness = createHarness("http://localhost/");
  const { api, calls, requests } = queuedRequests();
  const { PublicAuthLinks } = publicAuthLinks(api);
  await harness.render(React.createElement(PublicAuthLinks));
  assert.equal(harness.host.querySelector("a")?.getAttribute("href"), "/app");
  assert.equal(harness.host.textContent, "Личный кабинет");
  assert.equal(harness.host.querySelector('[role="status"]'), null);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, "/me");
  assert.ok(calls[0].options.signal);

  await act(async () => requests[0].resolve({}));
  assert.equal(harness.host.querySelector("a")?.getAttribute("href"), "/app");
  assert.equal(harness.host.textContent, "Личный кабинет");
  await harness.unmount();
});

test("public auth links switch to guest links after 401 and preserve them on network error", async () => {
  const harness = createHarness("http://localhost/");
  const { api, calls, requests } = queuedRequests();
  const { PublicAuthLinks, ApiError } = publicAuthLinks(api);
  await harness.render(React.createElement(PublicAuthLinks));
  await act(async () => requests[0].reject(new ApiError(401, "unauthorized")));
  await settle();
  assert.equal(
    harness.host.querySelector('a[href="/login"]')?.textContent,
    "Войти",
  );
  assert.match(
    harness.host.querySelector('a[href="/register"]')?.textContent ?? "",
    /Создать аккаунт/,
  );

  await act(async () => window.dispatchEvent(new window.Event("focus")));
  assert.equal(calls.length, 2);
  await act(async () => requests[1].reject(new ApiError(0, "network_error")));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/login"]'));
  assert.ok(harness.host.querySelector('a[href="/register"]'));
  await harness.unmount();
});

test("public auth links refresh on focus, visible, and persisted pageshow without overlapping checks", async () => {
  const harness = createHarness("http://localhost/");
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  });
  const { api, calls, requests } = queuedRequests();
  const { PublicAuthLinks, ApiError } = publicAuthLinks(api);
  await harness.render(React.createElement(PublicAuthLinks));
  await act(async () => requests[0].resolve({}));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/app"]'));

  await act(async () => window.dispatchEvent(new window.Event("focus")));
  document.dispatchEvent(new window.Event("visibilitychange"));
  const persisted = new window.Event("pageshow");
  Object.defineProperty(persisted, "persisted", { value: true });
  window.dispatchEvent(persisted);
  await settle();
  assert.equal(
    calls.length,
    2,
    "overlapping refresh triggers share one request",
  );
  await act(async () => requests[1].reject(new ApiError(401, "unauthorized")));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/login"]'));

  await act(async () => window.dispatchEvent(new window.Event("focus")));
  assert.equal(calls.length, 3);
  await act(async () => requests[2].reject(new ApiError(0, "network_error")));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/login"]'));

  await act(async () =>
    document.dispatchEvent(new window.Event("visibilitychange")),
  );
  await act(async () => window.dispatchEvent(new window.Event("focus")));
  const secondPersisted = new window.Event("pageshow");
  Object.defineProperty(secondPersisted, "persisted", { value: true });
  window.dispatchEvent(secondPersisted);
  await settle();
  assert.equal(
    calls.length,
    4,
    "visible, focus and persisted pageshow are deduplicated",
  );
  await act(async () => requests[3].resolve({}));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/app"]'));
  await harness.unmount();
});

test("public auth links abort on unmount and ignore a late session response", async () => {
  const harness = createHarness("http://localhost/");
  const { api, calls, requests } = queuedRequests();
  const { PublicAuthLinks } = publicAuthLinks(api);
  await harness.render(React.createElement(PublicAuthLinks));
  const signal = calls[0].options.signal;
  assert.equal(signal.aborted, false);
  await harness.unmount();
  assert.equal(signal.aborted, true);
  requests[0].resolve({});
  await settle();
});

test("public home mode checks a session only in app mode", async () => {
  const link = ({ href, className, children, ...props }) =>
    React.createElement("a", { href, className, ...props }, children);
  const modes = [
    ["app", true],
    ["frontend", false],
    ["landing", false],
  ];
  for (const [mode, shouldCheck] of modes) {
    const harness = createHarness("http://localhost/");
    const calls = [];
    const { PublicAuthLinks } = publicAuthLinks(async (path) => {
      calls.push(path);
      return {};
    });
    const ui = compile(
      "../src/shared/ui/index.tsx",
      {
        "next/link": link,
        "./legal-notice": { FooterLegalNotice: () => null },
        "./password-input": { PasswordInput: () => null },
      },
      { process: { env: { WEB_MODE: mode } } },
    );
    const { HomePage } = compile(
      "../src/_pages/home/index.tsx",
      {
        "next/link": link,
        "@/features/auth": { PublicAuthLinks },
        "@/features/theme-toggle": { ThemeToggle: () => null },
        "@/shared/ui": ui,
        "./monitoring-demo": { MonitoringDemo: () => null },
      },
      { process: { env: { WEB_MODE: mode } } },
    );
    await harness.render(React.createElement(HomePage));
    await settle();
    assert.equal(
      calls.filter((path) => path === "/me").length,
      shouldCheck ? 1 : 0,
      mode,
    );
    const primaryNav = harness.host.querySelector(
      'nav[aria-label="Главная навигация"]',
    );
    if (mode === "app")
      assert.equal(
        primaryNav.querySelector('a[href="/app"]')?.textContent,
        "Личный кабинет",
      );
    if (mode === "frontend") {
      assert.ok(primaryNav.querySelector('a[href="/login"]'));
      assert.ok(primaryNav.querySelector('a[href="/register"]'));
    }
    if (mode === "landing") {
      assert.ok(primaryNav.querySelector('a[href="#demo"]'));
      assert.equal(primaryNav.querySelector('a[href="/login"]'), null);
    }
    await harness.unmount();
  }
});

test("public auth link timeout aborts the request and stale success cannot replace guest links", async () => {
  const harness = createHarness("http://localhost/");
  const { api, calls, requests } = queuedRequests();
  const scheduled = [];
  const { PublicAuthLinks, ApiError } = publicAuthLinks(api, {
    setTimeout(callback, delay) {
      const timer = { callback, delay };
      scheduled.push(timer);
      return timer;
    },
    clearTimeout() {},
  });
  await harness.render(React.createElement(PublicAuthLinks));
  assert.equal(scheduled[0].delay, 5000);
  await act(async () => scheduled[0].callback());
  assert.equal(calls[0].options.signal.aborted, true);
  await act(async () => window.dispatchEvent(new window.Event("focus")));
  assert.equal(calls.length, 2);
  await act(async () => requests[1].reject(new ApiError(401, "unauthorized")));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/login"]'));
  await act(async () => requests[0].resolve({}));
  await settle();
  assert.ok(harness.host.querySelector('a[href="/login"]'));
  await harness.unmount();
});

function transientFailureApi(responses) {
  const calls = [];
  const attempts = new Map();
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    const attempt = (attempts.get(path) ?? 0) + 1;
    attempts.set(path, attempt);
    const response = responses[path];
    if (!response) throw new Error(`Unexpected API call: ${path}`);
    if (attempt === 1) throw new Error("network_error");
    return typeof response === "function" ? response(attempt) : response;
  };
  return { api, calls };
}

function settingsPage(api) {
  const link = ({ href, children, ...props }) =>
    React.createElement("a", { href, ...props }, children);
  const ui = {
    dateTime: (value) => value ?? "—",
    Loading: ({ text }) => React.createElement("div", { role: "status" }, text),
    Notice: ({ children }) =>
      React.createElement("div", { role: "alert" }, children),
    PasswordInput: (props) =>
      React.createElement("input", { ...props, type: "password" }),
  };
  return compile("../src/_pages/settings/index.tsx", {
    "next/link": link,
    "next/navigation": { useRouter: () => ({ replace() {} }) },
    "@/entities/session": {
      useSession: () => ({
        profile: {
          account: {
            id: "user-1",
            name: "Тестовый пользователь",
            role: "user",
            status: "active",
            createdAt: "2026-01-01T00:00:00Z",
          },
          email: "user@example.test",
          hasPassword: true,
        },
        refresh: async () => {},
        beginSessionTransition() {},
        endSessionTransition() {},
      }),
    },
    "@/features/messenger-links": { MessengerLinks: () => null },
    "@/features/oauth-identities": { OAuthIdentities: () => null },
    "@/features/account-deletion": { AccountDeletion: () => null },
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
    },
    "@/shared/ui": ui,
  });
}

function retryableFeatureAliases(api) {
  return {
    "next/link": ({ href, children, ...props }) =>
      React.createElement("a", { href, ...props }, children),
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
      oauthCallbackMessage: () => "OAuth callback error",
      oauthAuthorizationUrl: (_provider, value) => value,
    },
    "@/shared/ui": {
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
      PasswordInput: (props) =>
        React.createElement("input", { ...props, type: "password" }),
    },
    "@/entities/session": {
      useSession: () => ({
        beginSessionTransition() {},
        endSessionTransition() {},
      }),
    },
    "@/entities/messenger": {
      destinationKindName: {
        private: "личный чат",
        group: "группа",
        topic: "топик",
      },
      providerName: { telegram: "Telegram", max: "MAX" },
    },
    "@/shared/dialog": {
      Dialog: ({ title, children }) =>
        React.createElement(
          "div",
          { role: "dialog", "aria-label": title },
          children,
        ),
    },
  };
}

test("settings sessions recover from an initial GET failure with the retry control", async () => {
  const harness = createHarness("http://localhost/app/settings");
  const { api, calls } = transientFailureApi({
    "/me/sessions": {
      sessions: [
        {
          id: "session-recovered",
          current: false,
          userAgent: "Recovered browser",
          ip: "192.0.2.1",
          lastUsedAt: "2026-01-01T00:00:00Z",
          expiresAt: "2027-01-01T00:00:00Z",
          createdAt: "2026-01-01T00:00:00Z",
        },
      ],
    },
  });
  const { SettingsPage } = settingsPage(api);
  await harness.render(React.createElement(SettingsPage));
  await settle();
  assert.match(harness.host.textContent, /network_error/);
  assert.equal(harness.host.querySelector('[role="status"]'), null);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить загрузку сессий"),
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  assert.match(harness.host.textContent, /Recovered browser/);
  assert.equal(calls.filter(({ path }) => path === "/me/sessions").length, 2);
  assert.ok(
    calls.every(({ options }) => options.signal instanceof AbortSignal),
  );
  await harness.unmount();
});

test("billing recovers its initial data load and retries both required GETs", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const { api, calls } = transientFailureApi({
    "/billing": {
      provider: "mock",
      canSimulate: false,
      pricePerMonitorMinor: 2900,
      orders: [
        {
          id: "order-recovered",
          provider: "mock",
          status: "succeeded",
          monitorCount: 1,
          amountMinor: 2900,
          currency: "RUB",
          createdAt: "2026-01-01T00:00:00Z",
          expiresAt: "2026-01-01T00:15:00Z",
          completedAt: "2026-01-01T00:01:00Z",
          activeUntil: "2027-01-01T00:00:00Z",
        },
      ],
    },
    "/entitlement": {
      entitlement: { quota: 2, activeUntil: null, source: "none" },
    },
  });
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.match(harness.host.textContent, /network_error/);
  assert.equal(harness.host.querySelector('[role="status"]'), null);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить загрузку оплаты"),
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  assert.match(harness.host.textContent, /История заказов/);
  assert.equal(calls.filter(({ path }) => path === "/billing").length, 2);
  assert.equal(calls.filter(({ path }) => path === "/entitlement").length, 2);
  assert.ok(
    calls.every(({ options }) => options.signal instanceof AbortSignal),
  );
  await harness.unmount();
});

test("OAuth identity loading recovers both provider and linked-identity GETs", async () => {
  const harness = createHarness("http://localhost/app/settings");
  const { api, calls } = transientFailureApi({
    "/auth/oauth/providers": {
      providers: [{ id: "yandex", name: "Яндекс", enabled: true }],
    },
    "/me/identities": { identities: [] },
  });
  const component = compile(
    "../src/features/oauth-identities/index.tsx",
    retryableFeatureAliases(api),
  );
  await harness.render(
    React.createElement(component.OAuthIdentities, {
      hasPassword: true,
      isAdmin: false,
      onChange: async () => {},
    }),
  );
  await settle();
  assert.match(harness.host.textContent, /network_error/);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить загрузку способов входа"),
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  assert.match(harness.host.textContent, /Подключить/);
  assert.equal(
    calls.filter(({ path }) => path === "/auth/oauth/providers").length,
    2,
  );
  assert.equal(calls.filter(({ path }) => path === "/me/identities").length, 2);
  assert.ok(
    calls.every(({ options }) => options.signal instanceof AbortSignal),
  );
  await harness.unmount();
});

test("messenger destinations recover from an initial GET failure", async () => {
  const harness = createHarness("http://localhost/app/settings");
  const { api, calls } = transientFailureApi({
    "/messenger-destinations": {
      enabled: true,
      destinations: [
        {
          id: "destination-recovered",
          provider: "telegram",
          kind: "private",
          label: "Recovered Telegram chat",
          chatId: "qa-123",
          userId: "qa-user-123",
          threadId: null,
          ready: true,
          version: 1,
        },
      ],
    },
  });
  const component = compile(
    "../src/features/messenger-links/index.tsx",
    retryableFeatureAliases(api),
  );
  await harness.render(React.createElement(component.MessengerLinks));
  await settle();
  assert.match(harness.host.textContent, /network_error/);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить загрузку чатов"),
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  assert.match(harness.host.textContent, /Recovered Telegram chat/);
  assert.equal(
    calls.filter(({ path }) => path === "/messenger-destinations").length,
    2,
  );
  assert.ok(
    calls.every(({ options }) => options.signal instanceof AbortSignal),
  );
  await harness.unmount();
});

test("AuthEntry times out a hanging session check, retries, and ignores its late success", async () => {
  const harness = createHarness(
    "http://localhost/login?returnTo=%2Fapp%2Fsettings",
  );
  const scheduled = [];
  window.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    scheduled.push(timer);
    return timer;
  };
  window.clearTimeout = () => {};
  const calls = [];
  const requests = [];
  const routes = [];
  const api = (path, options = {}) => {
    calls.push({ path, options });
    const request = deferred();
    requests.push(request);
    return request.promise;
  };
  const { AuthEntry } = authEntry(api, {
    replace: (path) => routes.push(path),
  });
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  assert.equal(scheduled[0].delay, 10_000);
  assert.equal(calls.length, 1);
  await act(async () => scheduled[0].callback());
  await settle();
  assert.equal(calls[0].options.signal.aborted, true);
  assert.match(
    harness.host.textContent,
    /Проверка входа заняла слишком много времени/,
  );
  const retry = [...harness.host.querySelectorAll("button")].find(
    (button) => button.textContent.trim() === "Повторить",
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].options.signal.aborted, false);

  await act(async () => requests[0].resolve({}));
  await settle();
  assert.deepEqual(
    routes,
    [],
    "the timed-out first request cannot redirect late",
  );
  await act(async () => requests[1].resolve({}));
  await settle();
  assert.deepEqual(routes, ["/app/settings"]);
  await harness.unmount();
});

test("ConsoleShell menu is keyboard operable, closes on Escape, restores focus, and closes on navigation", async () => {
  const harness = createHarness("http://localhost/app");
  const profile = {
    account: {
      id: "account-menu-test",
      name: "Мобильный QA",
      role: "user",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    },
    email: "qa@example.test",
    hasPassword: true,
  };
  const sessionContext = React.createContext(null);
  const component = compile("../src/widgets/console-shell/index.tsx", {
    "next/link": ({ href, children, onClick, ...props }) =>
      React.createElement(
        "a",
        {
          href,
          ...props,
          onClick(event) {
            event.preventDefault();
            onClick?.(event);
          },
        },
        children,
      ),
    "next/navigation": {
      usePathname: () => "/app",
      useRouter: () => ({ replace() {} }),
    },
    "@/entities/session": { SessionContext: sessionContext },
    "@/features/theme-toggle": { ThemeToggle: () => null },
    "@/shared/api": {
      api: async (path) => {
        if (path === "/me") return profile;
        return {};
      },
      ApiError: class ApiError extends Error {},
      errorMessage: (error) => String(error?.message ?? error),
      safeAppReturnTo: (path) => path,
    },
    "@/shared/android": {
      captureMonitorDraft: () => null,
      clearMonitorDraft() {},
      readMonitorDraft() {},
    },
    "@/shared/ui": {
      Brand: () => React.createElement("span", null, "Heartbeat Checker"),
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(
    React.createElement(component.ConsoleShell, null, "Page content"),
  );
  await settle();
  const toggle = harness.host.querySelector(".sidebar-menu-toggle");
  const nav = harness.host.querySelector('nav[aria-label="Кабинет"]');
  assert.ok(toggle);
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(nav.dataset.open, "false");

  toggle.focus();
  toggle.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter" }));
  await act(async () => toggle.click());
  await settle();
  assert.equal(toggle.getAttribute("aria-expanded"), "true");
  assert.equal(nav.dataset.open, "true");

  await act(async () =>
    nav
      .querySelector('a[href="/app/billing"]')
      .dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
  );
  await settle();
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(nav.dataset.open, "false");
  assert.equal(document.activeElement, toggle);

  toggle.focus();
  toggle.dispatchEvent(new window.KeyboardEvent("keydown", { key: " " }));
  await act(async () => toggle.click());
  await settle();
  assert.equal(nav.dataset.open, "true");
  await act(async () => nav.querySelector('a[href="/app/settings"]').click());
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(nav.dataset.open, "false");
  await harness.unmount();
});
