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
const toastCalls = [];
const toastMock = {
  notify: {
    success(message, id) {
      toastCalls.push({ kind: "success", message, id });
    },
    error(message, id) {
      toastCalls.push({ kind: "error", message, id });
    },
  },
};
const billingEntity = (() => {
  const source = fs.readFileSync(
    new URL("../src/entities/billing/index.ts", import.meta.url),
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
  });
  return fixtureModule.exports;
})();

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
  toastCalls.length = 0;
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
      if (id === "@/shared/notifications") return toastMock;
      if (id === "@/entities/billing") return billingEntity;
      if (id === "@/features/public-pricing")
        return { PublicPricing: () => null };
      if (id === "@/features/admin-pricing")
        return { AdminPricing: () => null };
      if (id === "@/features/android-push")
        return { AndroidPushSettings: () => null };
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
  vm.runInNewContext(compiled, {
    exports,
    module: { exports },
    URL,
    URLSearchParams,
  });
  return exports.safeAppReturnTo;
}

function loadOAuthAuthorizationUrl() {
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
  return exports.oauthAuthorizationUrl;
}

function loadBoundedRequest() {
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
  vm.runInNewContext(compiled, {
    exports,
    module: { exports },
    window: globalThis.window,
    DOMException: globalThis.DOMException,
  });
  return exports.boundedRequest;
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
    "./registration": { RegistrationForm: registrationComponent(api, routes) },
    "next/link": ({ href, className, children, replace }) =>
      React.createElement(
        "a",
        { href, className, ...(replace ? { "data-replace": "true" } : {}) },
        children,
      ),
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
      oauthAuthorizationUrl: loadOAuthAuthorizationUrl(),
    },
    "@/shared/turnstile": {
      Turnstile: ({ action, onToken }) =>
        React.createElement(
          "button",
          {
            type: "button",
            "data-captcha-action": action,
            onClick: () => onToken(`synthetic-${action}`),
          },
          `captcha:${action}`,
        ),
    },
    "@/shared/ui": {
      Icon: () => null,
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
      OAuthIcon: () => null,
    },
  };
  return {
    ...compile("../src/features/auth/index.tsx", aliases, options),
    ApiError,
  };
}

function registrationComponent(api, router) {
  class RegistrationApiError extends Error {
    constructor(status, code) {
      super(code);
      this.status = status;
      this.code = code;
    }
  }
  const challengeTools = {
    captureEmailCode(value) {
      return { challenge: value, receivedAt: Date.now() };
    },
    normalizeEmailCode(value) {
      const code = value.replace(/\s/g, "");
      return /^[0-9]{6}$/.test(code) ? code : null;
    },
    useEmailCodeClock() {
      return { now: Date.now(), resendSeconds: 0 };
    },
  };
  const routes = {
    "next/link": ({ href, children, replace, ...props }) =>
      React.createElement(
        "a",
        { href, ...props, ...(replace ? { "data-replace": "true" } : {}) },
        children,
      ),
    "next/navigation": { useRouter: () => router },
    "@/shared/api": {
      api,
      ApiError: RegistrationApiError,
      boundedRequest: (controller, request) => request(controller.signal),
      errorMessage: (error) =>
        String(error?.code ?? error?.message ?? "Ошибка"),
      safeAppReturnTo: loadSafeAppReturnTo(),
    },
    "@/shared/email-code": challengeTools,
    "@/shared/turnstile": {
      Turnstile: ({ action, onToken }) =>
        React.createElement(
          "button",
          {
            type: "button",
            "data-captcha-action": action,
            onClick: () => onToken(`synthetic-${action}`),
          },
          `captcha:${action}`,
        ),
    },
    "@/shared/ui": {
      dateTime: (value) => value,
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
      PasswordInput: (props) =>
        React.createElement("input", { ...props, type: "password" }),
      Spinner: () => React.createElement("span", null, "…"),
    },
  };
  return compile("../src/features/auth/registration.tsx", routes, {
    process: { env: { WEB_MODE: "app" } },
  }).RegistrationForm;
}

async function setReactInputValue(input, value) {
  await act(async () => {
    const propsKey = Object.keys(input).find((key) =>
      key.startsWith("__reactProps"),
    );
    if (typeof input[propsKey]?.onChange === "function") {
      input[propsKey].onChange({ target: { value } });
    } else {
      Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        "value",
      ).set.call(input, value);
      input.dispatchEvent(new window.Event("input", { bubbles: true }));
    }
  });
}

async function submitReactForm(form) {
  const propsKey = Object.keys(form).find((key) =>
    key.startsWith("__reactProps"),
  );
  assert.equal(typeof form[propsKey]?.onSubmit, "function");
  await act(async () =>
    form[propsKey].onSubmit({ preventDefault() {}, currentTarget: form }),
  );
  await settle();
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
        Icon: () => null,
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

for (const [provider, name, authorizationUrl] of [
  ["vk", "VK", "https://id.vk.ru/authorize?client_id=synthetic"],
  ["yandex", "Яндекс", "https://oauth.yandex.ru/authorize?client_id=synthetic"],
]) {
  test(`${name} login replaces the page with its validated URL and keeps a safe returnTo`, async () => {
    const harness = createHarness();
    const calls = [];
    const replaced = [];
    const oauthWindow = {
      location: {
        href: "http://localhost/login?returnTo=%2Fapp%2Fsettings",
        pathname: "/login",
        search: "?returnTo=%2Fapp%2Fsettings",
        replace: (url) => replaced.push(url),
      },
      history: { replaceState() {} },
    };
    const api = async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/providers")
        return { providers: [{ id: provider, name, enabled: true }] };
      if (path === `/auth/oauth/${provider}/start`) return { authorizationUrl };
      throw new Error(`Unexpected API call: ${path}`);
    };
    const { AuthForm } = authEntry(api, {}, { window: oauthWindow });
    await harness.render(React.createElement(AuthForm, { mode: "login" }));
    await settle();
    const button = [...harness.host.querySelectorAll("button")].find((item) =>
      item.textContent.includes(name),
    );
    assert.ok(button, `enabled ${name} provider is available`);
    await act(async () => button.click());
    await settle();
    const start = calls.find(
      (call) => call.path === `/auth/oauth/${provider}/start`,
    );
    assert.equal(start.options.method, "POST");
    assert.deepEqual(JSON.parse(JSON.stringify(start.options.body)), {
      mode: "login",
      returnTo: "/app/settings",
    });
    assert.deepEqual(replaced, [authorizationUrl]);
    await harness.unmount();
  });
}

test("OAuth rejects an untrusted authorization URL without leaving the page", async () => {
  const harness = createHarness();
  const replaced = [];
  const oauthWindow = {
    location: {
      href: "http://localhost/login?returnTo=%2Fapp",
      pathname: "/login",
      search: "?returnTo=%2Fapp",
      replace: (url) => replaced.push(url),
    },
    history: { replaceState() {} },
  };
  const api = async (path) => {
    if (path === "/auth/oauth/providers")
      return { providers: [{ id: "vk", name: "VK", enabled: true }] };
    if (path === "/auth/oauth/vk/start")
      return { authorizationUrl: "https://attacker.example/authorize" };
    throw new Error(`Unexpected API call: ${path}`);
  };
  const { AuthForm } = authEntry(api, {}, { window: oauthWindow });
  await harness.render(React.createElement(AuthForm, { mode: "login" }));
  await settle();
  const button = [...harness.host.querySelectorAll("button")].find((item) =>
    item.textContent.includes("VK"),
  );
  assert.ok(button);
  await act(async () => button.click());
  await settle();
  assert.deepEqual(replaced, []);
  assert.equal(button.disabled, false, "provider login can be retried");
  assert.ok(toastCalls.some((call) => call.kind === "error"));
  await harness.unmount();
});

for (const [mode, url, expectedHref, description] of [
  [
    "login",
    "http://localhost/login?returnTo=%2Fapp%2Fsettings",
    "/register?returnTo=%2Fapp%2Fsettings",
    "carries a safe returnTo to registration",
  ],
  [
    "register",
    "http://localhost/register?returnTo=%2Fapp%2Fsettings",
    "/login?returnTo=%2Fapp%2Fsettings",
    "carries a safe returnTo to login",
  ],
  [
    "login",
    "http://localhost/login?returnTo=https%3A%2F%2Fevil.example%2F",
    "/register",
    "drops an external returnTo",
  ],
  [
    "register",
    "http://localhost/register?returnTo=%2Fapp&returnTo=%2Fapp%2Fsettings",
    "/login",
    "drops duplicate returnTo parameters",
  ],
]) {
  test(`${mode} stage link ${description}`, async () => {
    const harness = createHarness(url);
    const calls = [];
    const api = async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/providers") return { providers: [] };
      if (path === "/auth/registration" && !options.method)
        return { challenge: null };
      throw new Error(`Unexpected API call: ${path}`);
    };
    const { AuthForm } = authEntry(api, { replace() {}, refresh() {} });
    await harness.render(React.createElement(AuthForm, { mode }));
    await settle();
    const switchLink = harness.host.querySelector(".auth-switch a");
    assert.ok(switchLink, harness.host.textContent);
    assert.equal(switchLink.getAttribute("href"), expectedHref);
    assert.equal(switchLink.getAttribute("data-replace"), "true");
    await harness.unmount();
  });
}

for (const [returnTo, expectedPath, description] of [
  ["/app/settings", "/app/settings", "preserves safe returnTo"],
  ["https://evil.example/", "/app", "rejects external returnTo"],
]) {
  test(`successful email and password login replaces to the reviewed destination and ${description}`, async () => {
    const harness = createHarness(
      `http://localhost/login?returnTo=${encodeURIComponent(returnTo)}`,
    );
    const calls = [];
    const routes = [];
    const api = async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/providers") return { providers: [] };
      if (path === "/auth/login") return {};
      throw new Error(`Unexpected API call: ${path}`);
    };
    const { AuthForm } = authEntry(api, {
      replace: (path) => routes.push(path),
      refresh() {},
    });
    await harness.render(React.createElement(AuthForm, { mode: "login" }));
    await setReactInputValue(
      harness.host.querySelector('input[name="email"]'),
      "owner@example.test",
    );
    await setReactInputValue(
      harness.host.querySelector('input[name="password"]'),
      "synthetic passphrase",
    );
    await act(async () =>
      harness.host.querySelector('[data-captcha-action="login"]').click(),
    );
    await submitReactForm(harness.host.querySelector("form"));
    assert.deepEqual(routes, [expectedPath]);
    assert.equal(
      calls.find((call) => call.path === "/auth/login").options.method,
      "POST",
    );
    await harness.unmount();
  });
}

async function dispatchPageShow(persisted, target = window) {
  const event = new target.Event("pageshow");
  Object.defineProperty(event, "persisted", { value: persisted });
  await act(async () => target.dispatchEvent(event));
}

test("a persisted login page rechecks the session and routes only to its safe returnTo", async () => {
  const harness = createHarness(
    "http://localhost/login?returnTo=%2Fapp%2Fsettings",
  );
  const calls = [];
  let attempts = 0;
  const routes = [];
  const { AuthEntry, ApiError } = authEntry(
    apiStub(async () => {
      attempts += 1;
      if (attempts === 1) throw new ApiError(401, "unauthorized");
      return {};
    }, calls),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  await settle();
  assert.ok(harness.host.querySelector('form input[name="email"]'));
  assert.equal(calls.filter((call) => call.path === "/me").length, 1);

  await dispatchPageShow(false);
  await settle();
  assert.equal(
    calls.filter((call) => call.path === "/me").length,
    1,
    "ordinary pageshow does not duplicate the check",
  );
  await dispatchPageShow(true, harness.dom.window);
  await settle();
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  assert.deepEqual(routes, ["/app/settings"]);
  await harness.unmount();
});

test("a persisted login that remains signed out keeps the editable form", async () => {
  const harness = createHarness();
  const calls = [];
  const routes = [];
  const { AuthEntry, ApiError } = authEntry(
    apiStub(async () => {
      throw new ApiError(401, "unauthorized");
    }, calls),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  await settle();
  await dispatchPageShow(true);
  await settle();
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  assert.ok(harness.host.querySelector('form input[name="email"]'));
  assert.ok(harness.host.querySelector('form input[name="password"]'));
  assert.deepEqual(routes, []);
  await harness.unmount();
});

test("a failed persisted session check can be retried", async () => {
  const harness = createHarness();
  const calls = [];
  const routes = [];
  let attempts = 0;
  const { AuthEntry, ApiError } = authEntry(
    apiStub(async () => {
      attempts += 1;
      if (attempts === 1) throw new ApiError(401, "unauthorized");
      if (attempts === 2) throw new ApiError(503, "temporarily_unavailable");
      return {};
    }, calls),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  await settle();
  await dispatchPageShow(true);
  await settle();
  assert.match(harness.host.textContent, /Попробуйте немного позже/);
  const retry = [...harness.host.querySelectorAll("button")].find(
    (button) => button.textContent.trim() === "Повторить",
  );
  assert.ok(
    retry,
    "the restored page offers a retry after the temporary failure",
  );
  await act(async () => retry.click());
  await settle();
  assert.equal(calls.filter((call) => call.path === "/me").length, 3);
  assert.deepEqual(routes, ["/app"]);
  await harness.unmount();
});

test("a persisted check aborts obsolete work and cleanup ignores late responses", async () => {
  const harness = createHarness();
  const first = deferred();
  const restored = deferred();
  const calls = [];
  const routes = [];
  let count = 0;
  const { AuthEntry } = authEntry(
    apiStub(() => {
      count += 1;
      return count === 1 ? first.promise : restored.promise;
    }, calls),
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(AuthEntry, { mode: "login" }));
  const initialSignal = calls[0].options.signal;
  await dispatchPageShow(true);
  await settle();
  assert.equal(initialSignal.aborted, true);
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  first.resolve({});
  await settle();
  assert.deepEqual(routes, [], "an obsolete success is ignored");

  await harness.unmount();
  assert.equal(calls[1].options.signal.aborted, true);
  await dispatchPageShow(true, harness.dom.window);
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  restored.resolve({});
  await settle();
  assert.deepEqual(routes, [], "unmount also fences a late success");
});

test("OAuthRegistration BFCache revalidates session before replacing to a safe destination", async () => {
  const returnTo =
    "/app/settings?pushPair=123e4567-e89b-12d3-a456-426614174000";
  const harness = createHarness(
    `http://localhost/auth/oauth/registration?returnTo=${encodeURIComponent(returnTo)}#token=email-confirmation-secret`,
  );
  const sessionCheck = deferred();
  const calls = [];
  const routes = [];
  const { OAuthRegistration } = authEntry(
    async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/registration")
        return {
          provider: "vk",
          email: "owner@example.test",
          name: "Owner",
          expiresAt: "later",
        };
      if (path === "/me") return sessionCheck.promise;
      throw new Error(`Unexpected API call: ${path}`);
    },
    { replace: (path) => routes.push(path), refresh() {} },
  );
  await harness.render(React.createElement(OAuthRegistration));
  await settle();
  assert.match(harness.host.textContent, /owner@example\.test/);
  assert.equal(
    harness.dom.window.location.hash,
    "",
    "the confirmation token is removed from the URL",
  );

  await dispatchPageShow(true, harness.dom.window);
  await settle();
  assert.match(harness.host.textContent, /Проверяем регистрацию/);
  assert.doesNotMatch(harness.host.textContent, /owner@example\.test/);
  const check = calls.find((call) => call.path === "/me");
  assert.ok(check);
  assert.ok(check.options.signal);
  sessionCheck.resolve({});
  await settle();

  assert.deepEqual(routes, [returnTo]);
  assert.equal(
    calls.filter((call) => call.path === "/auth/oauth/registration").length,
    1,
  );
  assert.equal(
    calls.some((call) => call.options.method === "POST"),
    false,
  );
  await harness.unmount();
});

test("OAuthRegistration BFCache 401 performs a fresh read without replaying confirmation", async () => {
  const harness = createHarness(
    "http://localhost/auth/oauth/registration#token=email-confirmation-secret",
  );
  const calls = [];
  const routes = [];
  const { OAuthRegistration, ApiError } = authEntry(
    async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/registration")
        return {
          provider: "yandex",
          email: "owner@example.test",
          name: "Owner",
          expiresAt: "later",
        };
      if (path === "/me") throw new ApiError(401, "unauthorized");
      throw new Error(`Unexpected API call: ${path}`);
    },
    { replace: (path) => routes.push(path), refresh() {} },
  );
  await harness.render(React.createElement(OAuthRegistration));
  await settle();
  await dispatchPageShow(true, harness.dom.window);
  await settle();

  const registrationReads = calls.filter(
    (call) => call.path === "/auth/oauth/registration",
  );
  assert.equal(registrationReads.length, 2);
  assert.ok(registrationReads.every((call) => call.options.method !== "POST"));
  assert.match(harness.host.textContent, /owner@example\.test/);
  assert.ok(
    [...harness.host.querySelectorAll("button")].some((button) =>
      button.textContent.includes("Подтвердить email"),
    ),
    "the pending confirmation stays available after a fresh signed-out check",
  );
  assert.equal(
    calls.some((call) => call.path === "/auth/oauth/registration/confirm"),
    false,
  );
  assert.deepEqual(routes, []);
  await harness.unmount();
});

test("OAuthRegistration restore aborts a stale confirmation and ignores its late success", async () => {
  const harness = createHarness(
    "http://localhost/auth/oauth/registration#token=email-confirmation-secret",
  );
  const confirmation = deferred();
  const calls = [];
  const routes = [];
  const { OAuthRegistration, ApiError } = authEntry(
    async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/auth/oauth/registration")
        return {
          provider: "vk",
          email: "owner@example.test",
          name: "Owner",
          expiresAt: "later",
        };
      if (path === "/auth/oauth/registration/confirm")
        return confirmation.promise;
      if (path === "/me") throw new ApiError(401, "unauthorized");
      throw new Error(`Unexpected API call: ${path}`);
    },
    { replace: (path) => routes.push(path), refresh() {} },
  );
  await harness.render(React.createElement(OAuthRegistration));
  await settle();
  const confirm = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Подтвердить email"),
  );
  assert.ok(confirm);
  await act(async () => confirm.click());
  const post = calls.find(
    (call) => call.path === "/auth/oauth/registration/confirm",
  );
  assert.ok(post);
  assert.equal(post.options.method, "POST");

  await dispatchPageShow(true, harness.dom.window);
  await settle();
  assert.equal(post.options.signal.aborted, true);
  assert.equal(calls.filter((call) => call.path === "/me").length, 1);
  assert.equal(
    calls.filter((call) => call.path === "/auth/oauth/registration").length,
    2,
  );
  confirmation.resolve({ returnTo: "/app/settings" });
  await settle();
  assert.deepEqual(
    routes,
    [],
    "the stale confirmation cannot navigate after restoration",
  );
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
    const returnTo =
      "/app/settings?pushPair=10000000-0000-4000-8000-000000000001";
    const harness = createHarness(
      `http://localhost/login?${query}&returnTo=${encodeURIComponent(returnTo)}`,
    );
    const calls = [];
    const routes = [];
    const { AuthEntry } = authEntry(
      apiStub(async () => ({}), calls),
      { replace: (path) => routes.push(path) },
    );
    await harness.render(React.createElement(AuthEntry, { mode: "login" }));
    await settle();
    if (query.startsWith("oauthError=")) {
      assert.ok(
        toastCalls.some(
          (call) => call.kind === "error" && expected.test(call.message),
        ),
        "OAuth callback failure is announced through a toast",
      );
    } else assert.match(harness.host.textContent, expected);
    assert.ok(harness.host.querySelector('form input[name="email"]'));
    assert.equal(calls.filter((call) => call.path === "/me").length, 0);
    assert.deepEqual(routes, []);
    assert.equal(
      new URL(harness.dom.window.location.href).searchParams.get("returnTo"),
      returnTo,
    );
    if (query.startsWith("oauthError="))
      assert.equal(
        harness.host.querySelector(".auth-switch a")?.getAttribute("href"),
        `/register?returnTo=${encodeURIComponent(returnTo)}`,
        "the provider-cancel retry route retains the reviewed destination",
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
        Icon: () => null,
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
      "./icon": { Icon: () => null },
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
        "./icon": { Icon: () => null },
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

function billingPricingApi(api, fallbackPrice = 2900) {
  return async (path, options = {}) => {
    if (path === "/billing/pricing") {
      return {
        pricing: {
          pricePerMonitorMinor: fallbackPrice,
          currency: "RUB",
          period: "month",
          version: 1,
          updatedAt: "2026-10-19T10:00:00Z",
        },
      };
    }
    if (path === "/billing") {
      const result = await api(path, options);
      return { ...result, priceVersion: result.priceVersion ?? 1 };
    }
    return api(path, options);
  };
}

function settingsPage(api, options = {}) {
  const link = ({ href, children, ...props }) =>
    React.createElement("a", { href, ...props }, children);
  const ui = {
    Icon: () => null,
    dateTime: (value) => value ?? "—",
    Loading: ({ text }) => React.createElement("div", { role: "status" }, text),
    Notice: ({ children }) =>
      React.createElement("div", { role: "alert" }, children),
    PasswordInput: (props) =>
      React.createElement("input", { ...props, type: "password" }),
  };
  const profile = options.profile ?? {
    account: {
      id: "user-1",
      name: "Тестовый пользователь",
      role: "user",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    },
    email: "user@example.test",
    emailVerified: true,
    hasPassword: true,
  };
  class SettingsApiError extends Error {
    constructor(status, code, retryAfterSeconds) {
      super(code);
      this.status = status;
      this.code = code;
      this.retryAfterSeconds = retryAfterSeconds;
    }
  }
  const component = compile(
    "../src/_pages/settings/index.tsx",
    {
      "next/link": link,
      "next/navigation": { useRouter: () => ({ replace() {} }) },
      "@/entities/session": {
        useSession: () => ({
          profile:
            typeof options.profile === "function" ? options.profile() : profile,
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
        ApiError: SettingsApiError,
        errorMessage: (error) => String(error?.message ?? error),
      },
      "@/shared/ui": ui,
    },
    options.runtime,
  );
  return { ...component, SettingsApiError };
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
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
      PasswordInput: (props) =>
        React.createElement("input", { ...props, type: "password" }),
      OAuthIcon: () => null,
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

test("email verification prevents duplicate sends and restores an account-scoped countdown", async () => {
  const harness = createHarness("http://localhost/app/settings");
  const pending = deferred();
  const calls = [];
  let browserNow = Date.now();
  class BrowserDate extends Date {
    static now() {
      return browserNow;
    }
  }
  const profile = {
    account: {
      id: "email-account-one",
      name: "Synthetic account",
      role: "user",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    },
    email: "qa@example.test",
    emailVerified: false,
    hasPassword: true,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/me/sessions") return { sessions: [] };
    if (path === "/me/email-verification") return pending.promise;
    throw new Error(`Unexpected request: ${path}`);
  };
  const component = settingsPage(api, {
    profile: () => profile,
    runtime: { Date: BrowserDate },
  });
  await harness.render(React.createElement(component.SettingsPage));
  await settle();
  const resend = () =>
    [...harness.host.querySelectorAll("button")].find(
      (button) =>
        button.textContent.includes(
          "Отправить письмо для подтверждения email",
        ) || button.textContent.includes("Отправить повторно"),
    );
  const first = resend();
  assert.ok(first);
  await act(async () => {
    first.click();
    first.click();
  });
  assert.equal(
    calls.filter((call) => call.path === "/me/email-verification").length,
    1,
    "double activation while a send is pending must issue one POST",
  );
  await act(async () => pending.resolve({ sent: true }));
  await settle();
  assert.ok(
    toastCalls.some(
      (call) =>
        call.kind === "success" &&
        call.message.startsWith("Письмо с подтверждением запрошено."),
    ),
    "an accepted verification-email request should be announced as a transient toast",
  );
  assert.match(resend().textContent, /Отправить повторно \(60 с\)/);
  assert.equal(resend().disabled, true);
  assert.ok(
    harness.dom.window.sessionStorage.getItem(
      "heartbeat-email-verification:email-account-one",
    ),
  );

  browserNow += 1_000;
  await act(async () => window.dispatchEvent(new Event("focus")));
  await settle();
  assert.match(resend().textContent, /Отправить повторно \(59 с\)/);

  await harness.render(null);
  await harness.render(React.createElement(component.SettingsPage));
  await settle();
  assert.match(resend().textContent, /Отправить повторно \(59 с\)/);
  assert.equal(
    resend().disabled,
    true,
    "same-account remount restores the stored deadline",
  );

  profile.account.id = "email-account-two";
  await harness.render(React.createElement(component.SettingsPage));
  await settle();
  assert.match(
    resend().textContent,
    /Отправить письмо для подтверждения email/,
  );
  assert.equal(resend().disabled, false);
  await harness.unmount();
});

test("email confirmation is independent of the resend deadline", async () => {
  const harness = createHarness(
    "http://localhost/app/settings#verify-email=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  );
  const calls = [];
  const profile = {
    account: {
      id: "email-confirm-account",
      name: "Synthetic account",
      role: "user",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    },
    email: "qa@example.test",
    emailVerified: false,
    hasPassword: true,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/me/sessions") return { sessions: [] };
    if (path === "/me/email-verification/confirm") return undefined;
    throw new Error(`Unexpected request: ${path}`);
  };
  const { SettingsPage } = settingsPage(api, { profile });
  await harness.render(React.createElement(SettingsPage));
  await settle();
  assert.equal(harness.dom.window.location.hash, "");
  const confirm = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Подтвердить email"),
  );
  assert.ok(confirm);
  await act(async () => confirm.click());
  await settle();
  assert.equal(
    calls.filter((call) => call.path === "/me/email-verification/confirm")
      .length,
    1,
  );
  assert.match(harness.host.textContent, /Email подтверждён/);
  assert.equal(
    harness.dom.window.sessionStorage.getItem(
      "heartbeat-email-verification:email-confirm-account",
    ),
    null,
  );
  await harness.unmount();
});

test("email verification 429 respects Retry-After and timeout ignores a late success", async (t) => {
  await t.test("429 deadline", async () => {
    const harness = createHarness("http://localhost/app/settings");
    const calls = [];
    const profile = {
      account: {
        id: "email-429-account",
        name: "Synthetic account",
        role: "user",
        status: "active",
        createdAt: "2026-01-01T00:00:00Z",
      },
      email: "qa@example.test",
      emailVerified: false,
      hasPassword: true,
    };
    const { SettingsPage, SettingsApiError } = settingsPage(
      async (path, options = {}) => {
        calls.push({ path, options });
        if (path === "/me/sessions") return { sessions: [] };
        if (path === "/me/email-verification")
          throw new SettingsApiError(429, "rate_limited", 127);
        throw new Error(`Unexpected request: ${path}`);
      },
      { profile },
    );
    await harness.render(React.createElement(SettingsPage));
    await settle();
    const button = [...harness.host.querySelectorAll("button")].find((item) =>
      item.textContent.includes("Отправить письмо для подтверждения email"),
    );
    await act(async () => button.click());
    await settle();
    const cooldown = [...harness.host.querySelectorAll("button")].find((item) =>
      item.textContent.includes("Отправить повторно"),
    );
    assert.match(cooldown.textContent, /127 с/);
    assert.equal(cooldown.disabled, true);
    assert.equal(
      calls.filter((call) => call.path === "/me/email-verification").length,
      1,
    );
    await harness.unmount();
  });

  await t.test(
    "request timeout keeps a conservative deadline and ignores late success",
    async () => {
      const harness = createHarness("http://localhost/app/settings");
      const pending = deferred();
      const timers = [];
      const originalSetTimeout = harness.dom.window.setTimeout.bind(
        harness.dom.window,
      );
      harness.dom.window.setTimeout = (callback, delay, ...args) => {
        if (delay === 10_000) {
          const entry = { callback: () => callback(...args), delay };
          timers.push(entry);
          return timers.length;
        }
        return originalSetTimeout(callback, delay, ...args);
      };
      harness.dom.window.clearTimeout = (id) => {
        const entry = timers[id - 1];
        if (entry) entry.cleared = true;
      };
      const calls = [];
      const profile = {
        account: {
          id: "email-timeout-account",
          name: "Synthetic account",
          role: "user",
          status: "active",
          createdAt: "2026-01-01T00:00:00Z",
        },
        email: "qa@example.test",
        emailVerified: false,
        hasPassword: true,
      };
      const { SettingsPage } = settingsPage(
        async (path, options = {}) => {
          calls.push({ path, options });
          if (path === "/me/sessions") return { sessions: [] };
          if (path === "/me/email-verification") return pending.promise;
          throw new Error(`Unexpected request: ${path}`);
        },
        { profile },
      );
      await harness.render(React.createElement(SettingsPage));
      await settle();
      const button = [...harness.host.querySelectorAll("button")].find((item) =>
        item.textContent.includes("Отправить письмо для подтверждения email"),
      );
      await act(async () => button.click());
      const requestTimer = timers.filter((entry) => !entry.cleared).at(-1);
      assert.ok(requestTimer);
      await act(async () => requestTimer.callback());
      await settle();
      assert.equal(
        calls.find((call) => call.path === "/me/email-verification")?.options
          .signal?.aborted,
        true,
        "the timed-out request must be aborted before a late completion",
      );
      assert.match(
        harness.host.textContent,
        /Ответ занял слишком много времени/,
      );
      const cooldown = [...harness.host.querySelectorAll("button")].find(
        (item) => item.textContent.includes("Отправить повторно"),
      );
      assert.match(cooldown.textContent, /60 с/);
      await act(async () => pending.resolve({ sent: true }));
      await settle();
      assert.doesNotMatch(
        harness.host.textContent,
        /Письмо с подтверждением запрошено/,
      );
      assert.equal(
        toastCalls.some((call) => call.kind === "success"),
        false,
        "a late response after timeout must not announce a committed send",
      );
      assert.equal(
        calls.filter((call) => call.path === "/me/email-verification").length,
        1,
      );
      await harness.unmount();
    },
  );
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
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "billing-test-account" } },
      }),
    },
    "@/shared/api": {
      api: billingPricingApi(api),
      errorMessage: (error) => String(error?.message ?? error),
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
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

test("YooKassa checkout retries an ambiguous create with the same key and never starts a mock timer", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const timers = [];
  const originalSetTimeout = harness.dom.window.setTimeout.bind(
    harness.dom.window,
  );
  const originalClearTimeout = harness.dom.window.clearTimeout.bind(
    harness.dom.window,
  );
  harness.dom.window.setTimeout = (callback, delay = 0, ...args) => {
    const timer = originalSetTimeout(callback, delay, ...args);
    timers.push({ callback, delay, timer, active: true });
    return timer;
  };
  harness.dom.window.clearTimeout = (timer) => {
    const entry = timers.find((item) => item.timer === timer);
    if (entry) entry.active = false;
    originalClearTimeout(timer);
  };

  const firstCreate = deferred();
  const calls = [];
  const pendingOrder = {
    id: "8b9d2155-3d8a-44f0-a2a0-0fc3a8d2c8b4",
    provider: "yookassa",
    status: "pending",
    reconciliationStatus: "pending",
    monitorCount: 1,
    amountMinor: 2900,
    currency: "RUB",
    confirmationUrl: "https://yookassa.ru/checkout/abc",
    createdAt: "2026-01-01T00:00:00Z",
    expiresAt: null,
    completedAt: null,
    activeUntil: null,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 2900,
        orders: [],
      };
    if (path === "/entitlement")
      return {
        entitlement: { quota: 0, activeUntil: null, source: "none" },
      };
    if (path === "/billing/orders" && options.method === "POST")
      return calls.filter(
        (call) => call.path === path && call.options.method === "POST",
      ).length === 1
        ? firstCreate.promise
        : { order: pendingOrder };
    if (path === `/billing/orders/${pendingOrder.id}`)
      return { order: pendingOrder };
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile(
    "../src/_pages/billing/index.tsx",
    {
      "@/entities/session": {
        useSession: () => ({
          profile: { account: { id: "yookassa-test-owner" } },
        }),
      },
      "@/shared/api": {
        api: billingPricingApi(api),
        errorMessage: (error) => String(error?.message ?? error),
        paymentConfirmationUrl: shared.paymentConfirmationUrl,
      },
      "@/entities/monitor": {},
      "@/shared/ui": {
        Icon: () => null,
        dateTime: (value) => value ?? "—",
        Loading: ({ text }) =>
          React.createElement("div", { role: "status" }, text),
        Notice: ({ children }) =>
          React.createElement("div", { role: "alert" }, children),
      },
    },
    { crypto: { randomUUID: () => "33333333-3333-4333-8333-333333333333" } },
  );
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  const form = harness.host.querySelector(".billing-checkout form");
  assert.ok(form, "the configured test checkout should render");
  await act(async () => {
    form.dispatchEvent(
      new harness.dom.window.Event("submit", {
        bubbles: true,
        cancelable: true,
      }),
    );
    await Promise.resolve();
  });
  const timeout = timers.findLast(
    (item) => item.active && item.delay === 10_000,
  );
  assert.ok(timeout, "the in-flight create should have a bounded timeout");
  await act(async () => {
    originalClearTimeout(timeout.timer);
    timeout.active = false;
    timeout.callback();
    await Promise.resolve();
  });
  assert.match(harness.host.textContent, /Повторите запрос на этой странице/);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить запрос заказа"),
  );
  assert.ok(retry);
  await act(async () => retry.click());
  await settle();
  const posts = calls.filter(
    (call) => call.path === "/billing/orders" && call.options.method === "POST",
  );
  assert.equal(posts.length, 2);
  assert.equal(
    posts[0].options.body.idempotencyKey,
    posts[1].options.body.idempotencyKey,
  );
  assert.equal(
    posts[0].options.body.monitorCount,
    posts[1].options.body.monitorCount,
  );
  assert.equal(posts[0].options.body.priceVersion, 1);
  assert.equal(posts[1].options.body.priceVersion, 1);
  assert.equal(
    harness.dom.window.sessionStorage.getItem(
      "heartbeat-billing-request:yookassa-test-owner",
    ),
    null,
    "a confirmed order clears its retry intent",
  );
  assert.equal(posts[0].options.signal.aborted, true);
  assert.equal(posts[1].options.signal.aborted, false);
  assert.equal(
    calls.filter((call) => call.path.startsWith("/billing/orders/")).length,
    1,
    "accepted POST should be followed by one authoritative order lookup",
  );
  assert.equal(calls.filter((call) => call.path === "/entitlement").length, 1);
  assert.equal(
    harness.host.querySelector('a[href="https://yookassa.ru/checkout/abc"]')
      ?.textContent,
    "Перейти к оплате в ЮKassa",
  );
  assert.ok(timers.every((item) => item.delay < 30 * 60 * 1000));
  firstCreate.resolve({ order: { ...pendingOrder, id: "late-stale-order" } });
  await settle();
  assert.equal(harness.host.textContent.includes("late-stale-order"), false);
  await harness.unmount();
});

test("YooKassa create preserves quote and idempotency key after a 5xx, while stale quote needs a fresh click", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const calls = [];
  let billingReads = 0;
  let posts = 0;
  const order = {
    id: "a8b9d215-3d8a-44f0-a2a0-0fc3a8d2c8b4",
    provider: "yookassa",
    status: "pending",
    reconciliationStatus: "pending",
    monitorCount: 3,
    amountMinor: 7200,
    currency: "RUB",
    confirmationUrl: "https://yookassa.ru/checkout/pricing",
    createdAt: "2026-10-20T00:00:00Z",
    expiresAt: null,
    completedAt: null,
    activeUntil: null,
  };
  class ApiError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing") {
      billingReads += 1;
      const current =
        billingReads === 1
          ? { pricePerMonitorMinor: 2900, priceVersion: 1 }
          : { pricePerMonitorMinor: 2400, priceVersion: 2 };
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        ...current,
        orders: [],
      };
    }
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === "/billing/orders" && options.method === "POST") {
      posts += 1;
      if (posts === 1) throw new ApiError("temporary_unavailable", 503);
      if (posts === 2) throw new ApiError("price_changed", 409);
      return { order };
    }
    if (path === `/billing/orders/${order.id}`) return { order };
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  let idSequence = 0;
  const page = compile(
    "../src/_pages/billing/index.tsx",
    {
      "@/entities/session": {
        useSession: () => ({
          profile: { account: { id: "pricing-stale-owner" } },
        }),
      },
      "@/shared/api": {
        api: billingPricingApi(api),
        ApiError,
        errorMessage: (error) => String(error?.message ?? error),
        paymentConfirmationUrl: shared.paymentConfirmationUrl,
      },
      "@/entities/monitor": {},
      "@/shared/ui": {
        Icon: () => null,
        dateTime: (value) => value ?? "—",
        Loading: ({ text }) =>
          React.createElement("div", { role: "status" }, text),
        Notice: ({ children }) =>
          React.createElement("div", { role: "alert" }, children),
      },
    },
    {
      crypto: {
        randomUUID: () =>
          `44444444-4444-4444-8444-${String(++idSequence).padStart(12, "0")}`,
      },
    },
  );
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  const count = harness.host.querySelector('input[name="monitorCount"]');
  Object.getOwnPropertyDescriptor(
    harness.dom.window.HTMLInputElement.prototype,
    "value",
  ).set.call(count, "3");
  count.dispatchEvent(new harness.dom.window.Event("input", { bubbles: true }));
  count.dispatchEvent(
    new harness.dom.window.Event("change", { bubbles: true }),
  );
  const submit = async () => {
    const form = harness.host.querySelector(".billing-checkout form");
    assert.ok(form);
    await act(async () => {
      form.dispatchEvent(
        new harness.dom.window.Event("submit", {
          bubbles: true,
          cancelable: true,
        }),
      );
      await Promise.resolve();
    });
    await settle();
  };

  await submit();
  assert.equal(posts, 1);
  const uncertainIntent = JSON.parse(
    harness.dom.window.sessionStorage.getItem(
      "heartbeat-billing-request:pricing-stale-owner",
    ),
  );
  assert.equal(uncertainIntent.priceVersion, 1);
  assert.equal(uncertainIntent.pricePerMonitorMinor, 2900);
  const retry = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Повторить запрос заказа"),
  );
  assert.ok(retry, "5xx keeps the explicit same-intent retry available");
  await act(async () => retry.click());
  await settle();
  assert.equal(posts, 2);
  const postCalls = calls.filter(
    (call) => call.path === "/billing/orders" && call.options.method === "POST",
  );
  assert.equal(
    postCalls[0].options.body.idempotencyKey,
    postCalls[1].options.body.idempotencyKey,
  );
  assert.equal(postCalls[0].options.body.priceVersion, 1);
  assert.equal(postCalls[1].options.body.priceVersion, 1);

  const form = harness.host.querySelector(".billing-checkout form");
  assert.ok(form);
  await act(async () => {
    form.dispatchEvent(
      new harness.dom.window.Event("submit", {
        bubbles: true,
        cancelable: true,
      }),
    );
    await Promise.resolve();
  });
  await settle();
  assert.equal(
    posts,
    3,
    "the definitive stale-quote response is not retried automatically",
  );
  assert.match(harness.host.textContent, /24,00/);
  const allPosts = calls.filter(
    (call) => call.path === "/billing/orders" && call.options.method === "POST",
  );
  assert.notEqual(
    allPosts[1].options.body.idempotencyKey,
    allPosts[2].options.body.idempotencyKey,
  );
  assert.equal(allPosts[2].options.body.priceVersion, 2);

  await harness.unmount();
});

test("YooKassa return only looks up the owner order and hides payment for manual review", async () => {
  const orderId = "8b9d2155-3d8a-44f0-a2a0-0fc3a8d2c8b4";
  const harness = createHarness(
    `http://localhost/app/billing?order=${orderId}`,
  );
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 2900,
        orders: [],
      };
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === `/billing/orders/${orderId}`)
      return {
        order: {
          id: orderId,
          provider: "yookassa",
          status: "pending",
          reconciliationStatus: "manual_review",
          monitorCount: 1,
          amountMinor: 2900,
          currency: "RUB",
          confirmationUrl: "https://yookassa.ru/checkout/abc",
          createdAt: "2026-01-01T00:00:00Z",
          expiresAt: null,
          completedAt: null,
          activeUntil: null,
        },
      };
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "yookassa-test-owner" } },
      }),
    },
    "@/shared/api": {
      api: billingPricingApi(api),
      errorMessage: (error) => String(error?.message ?? error),
      paymentConfirmationUrl: shared.paymentConfirmationUrl,
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.equal(
    calls.filter((call) => call.path === "/billing/orders").length,
    0,
  );
  assert.equal(
    calls.filter((call) => call.path === `/billing/orders/${orderId}`).length,
    1,
  );
  assert.equal(
    harness.host.querySelector('a[href^="https://yookassa.ru/"]'),
    null,
  );
  assert.match(harness.host.textContent, /Нужна проверка результата/);
  await harness.unmount();
});

test("YooKassa return hydrates pending count and amount from the owner order", async () => {
  const orderId = "8b9d2155-3d8a-44f0-a2a0-0fc3a8d2c8b4";
  const harness = createHarness(
    `http://localhost/app/billing?order=${orderId}`,
  );
  const calls = [];
  const order = {
    id: orderId,
    provider: "yookassa",
    status: "pending",
    reconciliationStatus: "pending",
    monitorCount: 3,
    amountMinor: 8700,
    currency: "RUB",
    confirmationUrl: "https://yookassa.ru/checkout/abc",
    createdAt: "2026-01-01T00:00:00Z",
    expiresAt: null,
    completedAt: null,
    activeUntil: null,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 3100,
        orders: [],
      };
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === `/billing/orders/${orderId}`) return { order };
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "yookassa-test-owner" } },
      }),
    },
    "@/shared/api": {
      api: billingPricingApi(api, 3100),
      errorMessage: (error) => String(error?.message ?? error),
      paymentConfirmationUrl: shared.paymentConfirmationUrl,
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.equal(
    harness.host.querySelector('input[name="monitorCount"]').value,
    "3",
  );
  assert.match(harness.host.textContent, /87,00/);
  assert.ok(
    harness.host.querySelector('a[href="https://yookassa.ru/checkout/abc"]'),
  );
  assert.equal(
    calls.filter((call) => call.path === "/billing/orders").length,
    0,
  );
  await harness.unmount();
});

test("an unavailable current quote does not hide an existing pending payment order", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const calls = [];
  const pendingOrder = {
    id: "a15b6dc0-3b1e-4df3-9e98-1142fd775c62",
    provider: "yookassa",
    status: "pending",
    reconciliationStatus: "pending",
    monitorCount: 3,
    amountMinor: 8700,
    currency: "RUB",
    confirmationUrl: "https://yookassa.ru/checkout/old-order",
    createdAt: "2026-01-01T00:00:00Z",
    expiresAt: null,
    completedAt: null,
    activeUntil: null,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 0,
        priceVersion: 0,
        orders: [pendingOrder],
      };
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === `/billing/orders/${pendingOrder.id}`)
      return { order: pendingOrder };
    if (path === "/billing/pricing")
      throw new Error("pricing temporarily unavailable");
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "pricing-unavailable-owner" } },
      }),
    },
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
      paymentConfirmationUrl: shared.paymentConfirmationUrl,
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.match(harness.host.textContent, /Цена временно недоступна/);
  assert.match(harness.host.textContent, /87,00/);
  const paymentLink = harness.host.querySelector(
    'a[href="https://yookassa.ru/checkout/old-order"]',
  );
  assert.ok(
    paymentLink,
    "the previously issued provider URL remains available",
  );
  assert.equal(
    harness.host.querySelector('input[name="monitorCount"]').disabled,
    true,
  );
  assert.equal(
    calls.filter((call) => call.path === "/billing/orders").length,
    0,
  );
  await harness.unmount();
});

test("an older pricing response cannot roll back the billing overview quote", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const oldQuote = deferred();
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 2400,
        priceVersion: 2,
        orders: [],
      };
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === "/billing/pricing") return oldQuote.promise;
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "pricing-race-owner" } },
      }),
    },
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
      paymentConfirmationUrl: shared.paymentConfirmationUrl,
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.match(harness.host.textContent, /24,00/);
  await act(async () =>
    oldQuote.resolve({
      pricing: {
        pricePerMonitorMinor: 2900,
        currency: "RUB",
        period: "month",
        version: 1,
        updatedAt: "2026-10-01T00:00:00Z",
      },
    }),
  );
  await settle();
  assert.match(harness.host.textContent, /24,00/);
  assert.doesNotMatch(harness.host.textContent, /29,00/);

  await harness.unmount();
});

test("a delayed pricing failure does not erase a newer overview quote", async () => {
  const harness = createHarness("http://localhost/app/billing");
  const priceRead = deferred();
  void priceRead.promise.catch(() => {});
  const api = async (path) => {
    if (path === "/billing")
      return {
        provider: "yookassa",
        canSimulate: false,
        canPay: true,
        test: true,
        pricePerMonitorMinor: 2400,
        priceVersion: 2,
        orders: [],
      };
    if (path === "/entitlement")
      return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
    if (path === "/billing/pricing") return priceRead.promise;
    throw new Error(`Unexpected billing API call: ${path}`);
  };
  const shared = compile("../src/shared/api/index.ts", {});
  const page = compile("../src/_pages/billing/index.tsx", {
    "@/entities/session": {
      useSession: () => ({
        profile: { account: { id: "pricing-late-failure-owner" } },
      }),
    },
    "@/shared/api": {
      api,
      errorMessage: (error) => String(error?.message ?? error),
      paymentConfirmationUrl: shared.paymentConfirmationUrl,
    },
    "@/entities/monitor": {},
    "@/shared/ui": {
      Icon: () => null,
      dateTime: (value) => value ?? "—",
      Loading: ({ text }) =>
        React.createElement("div", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  await harness.render(React.createElement(page.BillingPage));
  await settle();
  assert.match(harness.host.textContent, /24,00/);
  await act(async () => priceRead.reject(new Error("late pricing failure")));
  await settle();
  assert.match(harness.host.textContent, /24,00/);
  assert.doesNotMatch(harness.host.textContent, /Цена временно недоступна/);
  await harness.unmount();
});

test("definitive YooKassa create rejections clear retry intent and refresh the current order", async () => {
  for (const status of [400, 403, 409]) {
    const harness = createHarness("http://localhost/app/billing");
    const calls = [];
    const pendingOrder = {
      id: "8b9d2155-3d8a-44f0-a2a0-0fc3a8d2c8b4",
      provider: "yookassa",
      status: "pending",
      reconciliationStatus: "pending",
      monitorCount: 1,
      amountMinor: 2900,
      currency: "RUB",
      confirmationUrl: "https://yookassa.ru/checkout/abc",
      createdAt: "2026-01-01T00:00:00Z",
      expiresAt: null,
      completedAt: null,
      activeUntil: null,
    };
    class ApiError extends Error {
      constructor(code) {
        super(code);
        this.status = status;
      }
    }
    let billingReads = 0;
    let postCalls = 0;
    const api = async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/billing") {
        billingReads += 1;
        return {
          provider: "yookassa",
          canSimulate: false,
          canPay: true,
          test: true,
          pricePerMonitorMinor: 2900,
          orders: status === 409 && billingReads > 1 ? [pendingOrder] : [],
        };
      }
      if (path === "/entitlement")
        return { entitlement: { quota: 0, activeUntil: null, source: "none" } };
      if (path === "/billing/orders" && options.method === "POST") {
        postCalls += 1;
        if (postCalls === 1) throw new ApiError(`rejected_${status}`);
        return { order: pendingOrder };
      }
      if (path === `/billing/orders/${pendingOrder.id}`)
        return { order: pendingOrder };
      throw new Error(`Unexpected billing API call: ${path}`);
    };
    const shared = compile("../src/shared/api/index.ts", {});
    let idSequence = 0;
    const page = compile(
      "../src/_pages/billing/index.tsx",
      {
        "@/entities/session": {
          useSession: () => ({
            profile: { account: { id: "yookassa-test-owner" } },
          }),
        },
        "@/shared/api": {
          api: billingPricingApi(api),
          ApiError,
          errorMessage: (error) => String(error?.message ?? error),
          paymentConfirmationUrl: shared.paymentConfirmationUrl,
        },
        "@/entities/monitor": {},
        "@/shared/ui": {
          Icon: () => null,
          dateTime: (value) => value ?? "—",
          Loading: ({ text }) =>
            React.createElement("div", { role: "status" }, text),
          Notice: ({ children }) =>
            React.createElement("div", { role: "alert" }, children),
        },
      },
      {
        crypto: {
          randomUUID: () =>
            `33333333-3333-4333-8333-${String(++idSequence).padStart(12, "0")}`,
        },
      },
    );
    await harness.render(React.createElement(page.BillingPage));
    await settle();
    const submit = async () => {
      const form = harness.host.querySelector(".billing-checkout form");
      assert.ok(form);
      await act(async () => {
        form.dispatchEvent(
          new harness.dom.window.Event("submit", {
            bubbles: true,
            cancelable: true,
          }),
        );
        await Promise.resolve();
      });
      await settle();
    };
    await submit();
    assert.equal(
      harness.dom.window.sessionStorage.getItem(
        "heartbeat-billing-request:yookassa-test-owner",
      ),
      null,
      `${status} must clear the persisted idempotency intent`,
    );
    assert.ok(
      calls.some((call) => call.path === "/billing" && call.options.signal),
      `${status} should refresh the authoritative overview`,
    );
    if (status === 409) {
      assert.ok(
        calls.some(
          (call) => call.path === `/billing/orders/${pendingOrder.id}`,
        ),
        "409 recovery should look up the existing pending order",
      );
      assert.equal(
        postCalls,
        1,
        "409 recovery must not submit another payment",
      );
      assert.ok(
        harness.host.querySelector(
          'a[href="https://yookassa.ru/checkout/abc"]',
        ),
      );
    } else {
      const retryButton = [...harness.host.querySelectorAll("button")].find(
        (button) => button.textContent.includes("Оформить заказ"),
      );
      assert.ok(
        retryButton,
        `${status} should unlock the form for a fresh request`,
      );
      assert.equal(
        harness.host.querySelector('input[name="monitorCount"]').disabled,
        false,
      );
      if (status === 400) {
        const firstKey = calls.find((call) => call.path === "/billing/orders")
          .options.body.idempotencyKey;
        await submit();
        const keys = calls
          .filter(
            (call) =>
              call.path === "/billing/orders" && call.options.method === "POST",
          )
          .map((call) => call.options.body.idempotencyKey);
        assert.equal(keys.length, 2);
        assert.notEqual(keys[0], keys[1]);
        assert.notEqual(keys[1], firstKey);
      }
    }
    await harness.unmount();
  }
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

test("a cancelled OAuth link returns to settings and consumes its one-time error", async () => {
  const harness = createHarness(
    "http://localhost/app/settings?oauthError=oauth_cancelled",
  );
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/auth/oauth/providers") return { providers: [] };
    if (path === "/me/identities") return { identities: [] };
    throw new Error(`Unexpected API call: ${path}`);
  };
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
  assert.ok(
    toastCalls.some(
      (call) =>
        call.kind === "error" && /OAuth callback error/.test(call.message),
    ),
  );
  assert.equal(harness.dom.window.location.pathname, "/app/settings");
  assert.equal(harness.dom.window.location.search, "");
  assert.equal(
    calls.some((call) => call.options.method === "POST"),
    false,
    "a cancelled callback does not retry or create a new link attempt",
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
      safeAppReturnTo: loadSafeAppReturnTo(),
      boundedRequest: loadBoundedRequest(),
    },
    "@/shared/ui": {
      Icon: () => null,
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

test("registration challenge sends distinct captcha actions, normalizes code, and creates a session once", async () => {
  const harness = createHarness(
    "http://localhost/register?returnTo=%2Fapp%2Fsettings",
  );
  const calls = [];
  const actions = [];
  const challenge = {
    id: "10000000-0000-4000-8000-000000000001",
    email: "new-owner@example.test",
    expiresAt: "2026-10-20T12:00:00.000Z",
    resendAvailableAt: "2026-10-20T10:00:00.000Z",
    serverNow: "2026-10-20T10:01:00.000Z",
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/auth/registration" && !options.method)
      return { challenge: null };
    if (path === "/auth/registration" && options.method === "POST")
      return { challenge };
    if (path === `/auth/registration/${challenge.id}/resend`)
      return { challenge };
    if (path === `/auth/registration/${challenge.id}/confirm`)
      return { account: { id: "account-1" }, csrfToken: "synthetic-csrf" };
    throw new Error(`Unexpected registration API request: ${path}`);
  };
  const router = {
    replacements: [],
    replace(path) {
      this.replacements.push(path);
    },
    refresh() {},
  };
  const RegistrationForm = registrationComponent(async (path, options) => {
    if (options?.body?.turnstileToken)
      actions.push(options.body.turnstileToken.replace("synthetic-", ""));
    return api(path, options);
  }, router);

  try {
    await harness.render(React.createElement(RegistrationForm));
    await settle();
    assert.ok(harness.host.querySelector('input[name="name"]'));
    await setReactInputValue(
      harness.host.querySelector('input[name="name"]'),
      "New Owner",
    );
    await setReactInputValue(
      harness.host.querySelector('input[name="email"]'),
      "new-owner@example.test",
    );
    await setReactInputValue(
      harness.host.querySelector('input[name="password"]'),
      "long synthetic passphrase",
    );
    await act(async () =>
      harness.host.querySelector('[data-captcha-action="register"]').click(),
    );
    await submitReactForm(harness.host.querySelector("form"));

    const start = calls.find(
      (call) =>
        call.path === "/auth/registration" && call.options.method === "POST",
    );
    assert.ok(start);
    assert.deepEqual(JSON.parse(JSON.stringify(start.options.body)), {
      name: "New Owner",
      email: "new-owner@example.test",
      password: "long synthetic passphrase",
      turnstileToken: "synthetic-register",
    });
    assert.ok(harness.host.querySelector('input[name="code"]'));
    assert.equal(harness.host.querySelector('input[name="password"]'), null);
    assert.match(harness.host.textContent, /Проверьте почту/);
    assert.equal(window.localStorage.length, 0);
    assert.equal(window.sessionStorage.length, 0);

    const resendButton = [...harness.host.querySelectorAll("button")].find(
      (button) => button.textContent.includes("Отправить повторно"),
    );
    assert.ok(resendButton);
    await act(async () => resendButton.click());
    await act(async () =>
      harness.host
        .querySelector('[data-captcha-action="registration_resend"]')
        .click(),
    );
    await submitReactForm(harness.host.querySelector("form"));
    assert.ok(calls.some((call) => call.path.endsWith("/resend")));

    await setReactInputValue(
      harness.host.querySelector('input[name="code"]'),
      "123 456",
    );
    await act(async () =>
      harness.host
        .querySelector('[data-captcha-action="registration_confirm"]')
        .click(),
    );
    await submitReactForm(harness.host.querySelector("form"));
    const confirm = calls.find((call) => call.path.endsWith("/confirm"));
    assert.deepEqual(JSON.parse(JSON.stringify(confirm.options.body)), {
      code: "123456",
      turnstileToken: "synthetic-registration_confirm",
    });
    assert.deepEqual(actions, [
      "register",
      "registration_resend",
      "registration_confirm",
    ]);
    assert.deepEqual(router.replacements, ["/app/settings"]);
  } finally {
    await harness.unmount();
  }
});

test("registration completion falls back to /app for an unsafe returnTo", async () => {
  const harness = createHarness(
    "http://localhost/register?returnTo=javascript%3Aalert(1)",
  );
  const calls = [];
  const challenge = {
    id: "10000000-0000-4000-8000-000000000002",
    email: "owner@example.test",
    expiresAt: "2026-10-20T12:00:00.000Z",
    resendAvailableAt: "2026-10-20T10:00:00.000Z",
    serverNow: "2026-10-20T10:01:00.000Z",
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/auth/registration" && !options.method)
      return { challenge: null };
    if (path === "/auth/registration" && options.method === "POST")
      return { challenge };
    if (path === `/auth/registration/${challenge.id}/confirm`)
      return {
        account: { id: "account-unsafe-return" },
        csrfToken: "synthetic-csrf",
      };
    throw new Error(`Unexpected registration API request: ${path}`);
  };
  const routes = [];
  const RegistrationForm = registrationComponent(api, {
    replace: (path) => routes.push(path),
    refresh() {},
  });
  try {
    await harness.render(React.createElement(RegistrationForm));
    await settle();
    await setReactInputValue(
      harness.host.querySelector('input[name="name"]'),
      "New Owner",
    );
    await setReactInputValue(
      harness.host.querySelector('input[name="email"]'),
      "owner@example.test",
    );
    await setReactInputValue(
      harness.host.querySelector('input[name="password"]'),
      "long synthetic passphrase",
    );
    await act(async () =>
      harness.host.querySelector('[data-captcha-action="register"]').click(),
    );
    await submitReactForm(harness.host.querySelector("form"));
    await setReactInputValue(
      harness.host.querySelector('input[name="code"]'),
      "123456",
    );
    await act(async () =>
      harness.host
        .querySelector('[data-captcha-action="registration_confirm"]')
        .click(),
    );
    await submitReactForm(harness.host.querySelector("form"));
    assert.ok(calls.some((call) => call.path.endsWith("/confirm")));
    assert.deepEqual(routes, ["/app"]);
  } finally {
    await harness.unmount();
  }
});

function consoleShellFixture(api, routes = {}, suppliedContext = null) {
  class ConsoleApiError extends Error {
    constructor(status, code) {
      super(code);
      this.status = status;
      this.code = code;
    }
  }
  const sessionContext = suppliedContext ?? React.createContext(null);
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
      useRouter: () => routes,
    },
    "@/entities/session": { SessionContext: sessionContext },
    "@/features/theme-toggle": { ThemeToggle: () => null },
    "@/shared/api": {
      api,
      ApiError: ConsoleApiError,
      boundedRequest: loadBoundedRequest(),
      errorMessage: (error) => String(error?.code ?? error?.message ?? error),
      safeAppReturnTo: loadSafeAppReturnTo(),
    },
    "@/shared/ui": {
      Icon: () => null,
      Brand: () => React.createElement("span", null, "Heartbeat Checker"),
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  });
  return { ...component, ApiError: ConsoleApiError, sessionContext };
}

const consoleTestProfile = (id, name) => ({
  account: {
    id,
    name,
    role: "user",
    status: "active",
    createdAt: "2026-01-01T00:00:00Z",
  },
  email: `${id}@example.test`,
  hasPassword: true,
});

test("ConsoleShell hides a BFCache snapshot until the fresh session check succeeds", async () => {
  const harness = createHarness("http://localhost/app");
  const initial = consoleTestProfile("account-bfcache", "Cached session");
  const refreshed = consoleTestProfile("account-bfcache", "Fresh session");
  const restored = deferred();
  const calls = [];
  const routes = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path !== "/me") throw new Error(`Unexpected API call: ${path}`);
    return calls.filter((call) => call.path === "/me").length === 1
      ? initial
      : restored.promise;
  };
  const component = consoleShellFixture(api, {
    replace: (path) => routes.push(path),
  });
  await harness.render(
    React.createElement(component.ConsoleShell, null, "Page content"),
  );
  await settle();
  assert.match(harness.host.textContent, /Cached session/);
  assert.equal(calls.length, 1);

  await dispatchPageShow(false);
  assert.equal(calls.length, 1, "non-persisted pageshow does not refresh");
  await dispatchPageShow(true);
  assert.equal(calls.length, 2);
  assert.match(harness.host.textContent, /Открываем кабинет/);
  assert.doesNotMatch(harness.host.textContent, /Cached session/);
  restored.resolve(refreshed);
  await settle();
  assert.match(harness.host.textContent, /Fresh session/);
  assert.deepEqual(routes, []);
  await harness.unmount();
});

test("ConsoleShell replaces a stale authenticated destination after BFCache 401", async () => {
  const pushPair = "123e4567-e89b-12d3-a456-426614174000";
  const returnTo = `/app/settings?pushPair=${pushPair}`;
  const harness = createHarness(`http://localhost${returnTo}`);
  const calls = [];
  const routes = [];
  const { ApiError, ConsoleShell } = consoleShellFixture(
    async (path, options = {}) => {
      calls.push({ path, options });
      if (calls.filter((call) => call.path === "/me").length === 1)
        return consoleTestProfile("account-expired", "Expired cached profile");
      throw new ApiError(401, "unauthorized");
    },
    { replace: (path) => routes.push(path) },
  );
  await harness.render(React.createElement(ConsoleShell, null, "Page content"));
  await settle();
  assert.match(harness.host.textContent, /Expired cached profile/);
  await dispatchPageShow(true);
  await settle();
  assert.deepEqual(routes, [`/login?returnTo=${encodeURIComponent(returnTo)}`]);
  assert.doesNotMatch(harness.host.textContent, /Expired cached profile/);
  await harness.unmount();
});

test("ConsoleShell logout fences an in-flight session refresh from restoring the profile", async () => {
  const harness = createHarness("http://localhost/app");
  const profile = consoleTestProfile("account-logout", "Signed-in profile");
  const staleRequest = deferred();
  const logoutRequest = deferred();
  const calls = [];
  const routes = [];
  const api = (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/auth/logout") return logoutRequest.promise;
    if (path !== "/me") throw new Error(`Unexpected API call: ${path}`);
    return calls.filter((call) => call.path === "/me").length === 1
      ? Promise.resolve(profile)
      : staleRequest.promise;
  };
  const { ConsoleShell } = consoleShellFixture(api, {
    replace: (path) => routes.push(path),
  });
  await harness.render(React.createElement(ConsoleShell, null, "Page content"));
  await settle();
  assert.match(harness.host.textContent, /Signed-in profile/);

  await act(async () => window.dispatchEvent(new window.Event("focus")));
  await settle();
  const staleSignal = calls.findLast((call) => call.path === "/me").options
    .signal;
  assert.equal(staleSignal.aborted, false);
  const logout = harness.host.querySelector("button.logout");
  assert.ok(logout);
  await act(async () => logout.click());
  assert.equal(staleSignal.aborted, true);
  assert.equal(
    calls.find((call) => call.path === "/auth/logout").options.method,
    "POST",
  );
  logoutRequest.resolve({});
  await settle();
  assert.deepEqual(routes, ["/login"]);

  staleRequest.resolve(profile);
  await settle();
  assert.doesNotMatch(harness.host.textContent, /Signed-in profile/);
  assert.deepEqual(routes, ["/login"]);
  await harness.unmount();
});

test("ConsoleShell session transitions ignore persisted pageshow until transition ends", async () => {
  const harness = createHarness("http://localhost/app");
  const calls = [];
  const profile = consoleTestProfile(
    "account-transition",
    "Transition profile",
  );
  let session;
  function SessionProbe() {
    session = React.useContext(context);
    return null;
  }
  const context = React.createContext(null);
  const { ConsoleShell } = consoleShellFixture(
    async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/me") return profile;
      return {};
    },
    {},
    context,
  );
  await harness.render(
    React.createElement(
      ConsoleShell,
      null,
      React.createElement(SessionProbe),
      "Page content",
    ),
  );
  await settle();
  assert.ok(session);
  assert.match(harness.host.textContent, /Transition profile/);

  await act(async () => session.beginSessionTransition());
  await dispatchPageShow(true);
  assert.equal(calls.filter((call) => call.path === "/me").length, 1);
  assert.match(harness.host.textContent, /Transition profile/);
  await act(async () => session.endSessionTransition());
  await dispatchPageShow(true);
  await settle();
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  assert.match(harness.host.textContent, /Transition profile/);
  await harness.unmount();
});

test("ConsoleShell times out a hanging profile check and retry can recover", async () => {
  const harness = createHarness("http://localhost/app");
  const scheduled = [];
  window.setTimeout = (callback, delay) => {
    const timer = { callback, delay, cleared: false };
    scheduled.push(timer);
    return timer;
  };
  window.clearTimeout = (timer) => {
    if (timer) timer.cleared = true;
  };
  const pending = deferred();
  const calls = [];
  const profile = consoleTestProfile("account-retry", "Recovered session");
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path !== "/me") throw new Error(`Unexpected API call: ${path}`);
    return calls.filter((call) => call.path === "/me").length === 1
      ? pending.promise
      : profile;
  };
  const { ConsoleShell } = consoleShellFixture(api);
  await harness.render(React.createElement(ConsoleShell, null, "Page content"));
  assert.equal(scheduled[0].delay, 10_000);
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
  assert.equal(calls.filter((call) => call.path === "/me").length, 2);
  assert.match(harness.host.textContent, /Recovered session/);
  pending.resolve(profile);
  await settle();
  assert.match(harness.host.textContent, /Recovered session/);
  await harness.unmount();
});
