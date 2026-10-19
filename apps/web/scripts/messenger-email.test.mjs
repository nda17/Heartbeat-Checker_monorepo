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

class ApiError extends Error {
  constructor(status, code) {
    super(code);
    this.status = status;
    this.code = code;
  }
}

function loadComponent(relativePath, aliases, runtime = {}) {
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
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: fixtureModule.exports,
    require(id) {
      if (Object.hasOwn(aliases, id)) return aliases[id];
      return requireWeb(id);
    },
    console,
    Date,
    URL,
    URLSearchParams,
    AbortController,
    DOMException,
    setTimeout,
    clearTimeout,
    window: globalThis.window,
    document: globalThis.document,
    FormData: globalThis.FormData,
    ...runtime,
  });
  return fixtureModule.exports;
}

function createHarness(url = "http://localhost/") {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: dom.window.navigator,
  });
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.FormData = dom.window.FormData;
  globalThis.DOMException = dom.window.DOMException;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  return {
    host,
    async render(element) {
      await act(async () => root.render(element));
      await act(async () => Promise.resolve());
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
      delete globalThis.MouseEvent;
      delete globalThis.FormData;
      delete globalThis.DOMException;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    },
  };
}

function uiMocks() {
  return {
    Icon: () => null,
    Loading: () => React.createElement("span", { role: "status" }, "Загрузка"),
    Notice: ({ children, success }) =>
      React.createElement(
        "div",
        { role: "alert", "data-success": success ? "true" : undefined },
        children,
      ),
    PasswordInput: (props) =>
      React.createElement("input", { ...props, type: "password" }),
    dateTime: (value) => String(value ?? ""),
  };
}

function apiAliases(api) {
  return {
    "@/shared/api": {
      api,
      ApiError,
      errorMessage: (error) => String(error?.message ?? "Ошибка"),
    },
    "@/shared/ui": uiMocks(),
    "@/shared/dialog": {
      Dialog: ({ children, title }) =>
        React.createElement(
          "section",
          { role: "dialog", "aria-label": title },
          children,
        ),
    },
  };
}

function loadWebhookNotifications(api) {
  return loadComponent(
    "../src/features/monitor-notifications/webhook.tsx",
    apiAliases(api),
    { navigator: globalThis.navigator },
  ).WebhookNotifications;
}

function loadMessengerLinks(api, browserNow = Date.now()) {
  const DateWithBrowserClock = class extends Date {
    static now() {
      return browserNow;
    }
  };
  const aliases = {
    ...apiAliases(api),
    "@/entities/messenger": loadComponent(
      "../src/entities/messenger/index.ts",
      {},
    ),
    "@/shared/dialog": {
      Dialog: ({ title, children, onClose, busy }) =>
        React.createElement(
          "section",
          { role: "dialog", "aria-label": title },
          React.createElement(
            "button",
            { disabled: busy, onClick: onClose },
            "Закрыть окно",
          ),
          children,
        ),
    },
  };
  return loadComponent("../src/features/messenger-links/index.tsx", aliases, {
    Date: DateWithBrowserClock,
    navigator: globalThis.navigator,
  }).MessengerLinks;
}

function messengerLink(id, kind, status, errorCode, expiresAt) {
  return {
    id,
    provider: "telegram",
    kind,
    status,
    errorCode,
    expiresAt,
    candidate:
      status === "candidate"
        ? {
            label: "Operations",
            chatId: "-10077",
            userId: "321",
            threadId: kind === "topic" ? "44" : null,
          }
        : null,
    destinationId: null,
  };
}

async function clickMessengerButton(harness, label) {
  const button = [...harness.host.querySelectorAll("button")].find((item) =>
    item.textContent.includes(label),
  );
  assert.ok(button, `missing messenger button: ${label}`);
  await act(async () =>
    button.dispatchEvent(new MouseEvent("click", { bubbles: true })),
  );
  await settle();
}

function webhookApi(api) {
  const aliases = {
    ...apiAliases(api),
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
    "@/entities/monitor": {},
    "@/entities/messenger": {
      destinationKindName: {
        private: "личный чат",
        group: "группа",
        topic: "топик группы",
        email: "email",
      },
      providerName: { telegram: "Telegram", max: "MAX", email: "Email" },
    },
    "@/shared/dialog": {
      Dialog: ({ title, children }) =>
        React.createElement(
          "section",
          { role: "dialog", "aria-label": title },
          children,
        ),
    },
    "./webhook": { WebhookNotifications: loadWebhookNotifications(api) },
  };
  return loadComponent(
    "../src/features/monitor-notifications/index.tsx",
    aliases,
  ).MonitorNotifications;
}

async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

async function fillWebhookURL(input, value) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(input, value);
    const key = Object.keys(input).find((name) =>
      name.startsWith("__reactProps"),
    );
    input[key].onChange({ target: { value } });
  });
}

async function submitWebhook(input) {
  const form = input.closest("form");
  const propsKey = Object.keys(form).find((key) =>
    key.startsWith("__reactProps"),
  );
  assert.equal(typeof form[propsKey]?.onSubmit, "function");
  await act(async () => {
    form[propsKey].onSubmit({ preventDefault() {} });
  });
  await settle();
}

async function clickButton(harness, label) {
  const button = [...harness.host.querySelectorAll("button")].find((item) =>
    item.textContent.includes(label),
  );
  assert.ok(button, `missing button ${label}`);
  await act(async () => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await settle();
}

test("email connection uses only the account address and sends an empty request", async () => {
  const harness = createHarness();
  const calls = [];
  const destination = {
    id: "email-destination",
    provider: "email",
    kind: "email",
    label: "owner@example.test",
    email: "owner@example.test",
    chatId: "",
    userId: "",
    threadId: null,
    ready: true,
    version: 1,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/messenger-destinations" && !options.method) {
      return { enabled: false, emailEnabled: true, destinations: [] };
    }
    if (path === "/messenger-destinations/email") return { destination };
    throw new Error(`Unexpected request: ${path}`);
  };
  const aliases = {
    ...apiAliases(api),
    "@/entities/messenger": loadComponent(
      "../src/entities/messenger/index.ts",
      {},
    ),
    "@/shared/dialog": {
      Dialog: ({ children }) => React.createElement("section", null, children),
    },
  };
  const { MessengerLinks } = loadComponent(
    "../src/features/messenger-links/index.tsx",
    aliases,
  );

  try {
    await harness.render(
      React.createElement(MessengerLinks, { email: "owner@example.test" }),
    );
    assert.match(harness.host.textContent, /owner@example\.test/);
    assert.match(harness.host.textContent, /Подключить email аккаунта/);
    assert.equal(harness.host.querySelector('input[type="email"]'), null);
    assert.equal(
      harness.host.querySelectorAll(".messenger-options button").length,
      0,
    );

    const button = [...harness.host.querySelectorAll("button")].find((item) =>
      item.textContent.includes("Подключить email аккаунта"),
    );
    assert.ok(button);
    await act(async () => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    const connect = calls.find(
      (call) => call.path === "/messenger-destinations/email",
    );
    assert.equal(JSON.stringify(connect?.options.body), "{}");
    assert.equal(connect?.options.method, "POST");
    assert.equal(connect?.options.authenticated, true);
    assert.match(harness.host.textContent, /Подтверждённый email/);
    assert.match(harness.host.textContent, /Отключить email/);
  } finally {
    await harness.unmount();
  }
});

test("email monitor route is off by default and saves the selected destination", async () => {
  const harness = createHarness();
  const calls = [];
  const destination = {
    id: "email-destination",
    provider: "email",
    kind: "email",
    label: "owner@example.test",
    email: "owner@example.test",
    chatId: "",
    userId: "",
    threadId: null,
    ready: true,
    version: 1,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/messenger-destinations") {
      return {
        enabled: false,
        emailEnabled: true,
        destinations: [destination],
      };
    }
    if (path === "/monitors/monitor-1/messenger-routes") {
      return { enabled: false, emailEnabled: true, routes: [] };
    }
    if (path.endsWith("/messenger-routes/email")) {
      return {
        route: {
          provider: "email",
          destinationId: "email-destination",
          enabled: true,
          reminderMinutes: 0,
          version: 1,
        },
      };
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const aliases = {
    ...apiAliases(api),
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
    "@/entities/monitor": {},
    "@/entities/messenger": loadComponent(
      "../src/entities/messenger/index.ts",
      {},
    ),
    "./webhook": { WebhookNotifications: () => null },
  };
  const { MonitorNotifications } = loadComponent(
    "../src/features/monitor-notifications/index.tsx",
    aliases,
  );

  try {
    await harness.render(
      React.createElement(MonitorNotifications, {
        monitor: { id: "monitor-1", name: "Checkout" },
        onClose() {},
      }),
    );
    const emailHeading = [...harness.host.querySelectorAll("h3")].find(
      (heading) => heading.textContent === "Email",
    );
    assert.ok(emailHeading);
    const form = emailHeading.closest("form");
    const checkbox = form.querySelector('input[name="enabled"]');
    assert.equal(checkbox.checked, false, "a new email route must be opt-in");
    assert.equal(
      form.querySelector('select[name="destinationId"]').value,
      destination.id,
    );

    checkbox.checked = true;
    const submitted = new Event("submit", { bubbles: true, cancelable: true });
    await act(async () => {
      form.dispatchEvent(submitted);
      await Promise.resolve();
    });

    const save = calls.find((call) =>
      call.path.endsWith("/messenger-routes/email"),
    );
    assert.equal(save?.options.method, "PUT");
    assert.deepEqual(JSON.parse(JSON.stringify(save?.options.body)), {
      destinationId: destination.id,
      enabled: true,
      reminderMinutes: 0,
      version: 0,
    });
    assert.match(harness.host.textContent, /Настройки уведомлений сохранены/);
  } finally {
    await harness.unmount();
  }
});

test("settings clears the verification fragment and requires explicit confirmation", async () => {
  const token = "a".repeat(43);
  const harness = createHarness(
    `http://localhost/app/settings#verify-email=${token}`,
  );
  const calls = [];
  let transitions = 0;
  let releaseRefresh;
  const pendingRefresh = new Promise((resolve) => {
    releaseRefresh = resolve;
  });
  const timers = new Map();
  let timerID = 0;
  window.setTimeout = (callback) => {
    const id = ++timerID;
    timers.set(id, callback);
    return id;
  };
  window.clearTimeout = (id) => timers.delete(id);
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/me/sessions") return { sessions: [] };
    if (path === "/me/email-verification/confirm") return {};
    throw new Error(`Unexpected request: ${path}`);
  };
  const profile = {
    account: { id: "owner", name: "Owner", role: "user" },
    email: "owner@example.test",
    emailVerified: false,
    hasPassword: true,
  };
  const aliases = {
    ...apiAliases(api),
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
    "next/navigation": { useRouter: () => ({ replace() {} }) },
    "@/entities/session": {
      useSession: () => ({
        profile,
        refresh: () => pendingRefresh,
        beginSessionTransition() {
          transitions += 1;
        },
        endSessionTransition() {},
      }),
    },
    "@/features/messenger-links": { MessengerLinks: () => null },
    "@/features/oauth-identities": { OAuthIdentities: () => null },
    "@/features/account-deletion": { AccountDeletion: () => null },
  };
  const { SettingsPage } = loadComponent(
    "../src/_pages/settings/index.tsx",
    aliases,
  );

  try {
    await harness.render(React.createElement(SettingsPage));
    assert.equal(
      window.location.hash,
      "",
      "the private token must leave the URL",
    );
    assert.match(harness.host.textContent, /Подтвердить email/);
    assert.match(
      harness.host.textContent,
      /Отправить письмо для подтверждения email/,
    );
    assert.equal(
      calls.some((call) => call.path === "/me/email-verification/confirm"),
      false,
      "opening the fragment must not confirm automatically",
    );

    const confirm = [...harness.host.querySelectorAll("button")].find(
      (button) => button.textContent === "Подтвердить email",
    );
    await act(async () => {
      confirm.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const request = calls.find(
      (call) => call.path === "/me/email-verification/confirm",
    );
    assert.deepEqual(JSON.parse(JSON.stringify(request?.options.body)), {
      token,
    });
    assert.equal(request?.options.authenticated, true);
    assert.equal(
      transitions,
      0,
      "email confirmation must not rotate the session",
    );
    assert.match(harness.host.textContent, /Email подтверждён/);
    assert.equal(
      timers.size,
      0,
      "a successful confirmation clears its timeout",
    );
    assert.equal(
      [...harness.host.querySelectorAll("button")].some(
        (button) => button.textContent === "Подтвердить email",
      ),
      false,
      "the confirmed token action should disappear after success",
    );
    for (const callback of timers.values()) callback();
    await act(async () => Promise.resolve());
    assert.doesNotMatch(
      harness.host.textContent,
      /слишком много времени|состояние email перед повтором/i,
      "a slow profile refresh must not turn a confirmed POST into an ambiguous result",
    );
    releaseRefresh();
    await act(async () => pendingRefresh);
  } finally {
    await harness.unmount();
  }
});

test("webhook settings stay available when messenger settings fail to load", async () => {
  const harness = createHarness();
  const calls = [];
  const api = async (path) => {
    calls.push(path);
    if (
      path === "/messenger-destinations" ||
      path.endsWith("/messenger-routes")
    )
      throw new ApiError(503, "temporarily_unavailable");
    if (path.endsWith("/webhook/deliveries?limit=50"))
      return { deliveries: [] };
    if (path.endsWith("/webhook")) throw new ApiError(404, "not_found");
    throw new Error(`Unexpected request: ${path}`);
  };
  const MonitorNotifications = webhookApi(api);

  try {
    await harness.render(
      React.createElement(MonitorNotifications, {
        monitor: { id: "monitor-1", name: "Checkout" },
        onClose() {},
      }),
    );
    await settle();
    assert.match(harness.host.textContent, /temporarily_unavailable/);
    assert.ok(harness.host.querySelector('section[aria-label="Webhook"]'));
    assert.match(harness.host.textContent, /История webhook/);
    assert.ok(calls.some((path) => path === "/monitors/monitor-1/webhook"));
    assert.ok(
      calls.some(
        (path) => path === "/monitors/monitor-1/webhook/deliveries?limit=50",
      ),
    );
  } finally {
    await harness.unmount();
  }
});

test("webhook create reveals a one-time secret, keeps a committed success through history failure, and enables only on request", async () => {
  const harness = createHarness();
  const calls = [];
  let historyReads = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/monitors/monitor-1/webhook" && !options.method)
      throw new ApiError(404, "not_found");
    if (path.endsWith("/webhook/deliveries?limit=50")) {
      historyReads += 1;
      if (historyReads === 1) return { deliveries: [] };
      throw new ApiError(503, "temporarily_unavailable");
    }
    if (path === "/monitors/monitor-1/webhook" && options.method === "PUT") {
      const input = JSON.parse(JSON.stringify(options.body));
      if (input.version === 0)
        return {
          webhook: {
            id: "webhook-1",
            monitorId: "monitor-1",
            url: input.url,
            enabled: false,
            version: 1,
            createdAt: "2026-01-01T00:00:00Z",
            updatedAt: "2026-01-01T00:00:00Z",
          },
          secret: "one-time-signing-secret",
        };
      return {
        webhook: {
          id: "webhook-1",
          monitorId: "monitor-1",
          url: "https://receiver.example.test/hooks",
          enabled: input.enabled,
          version: 2,
          createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z",
        },
      };
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const WebhookNotifications = loadWebhookNotifications(api);

  try {
    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-1" }),
    );
    await settle();
    const input = harness.host.querySelector('input[name="webhookUrl"]');
    assert.ok(input, harness.host.textContent);
    await fillWebhookURL(input, "https://receiver.example.test/hooks");
    assert.equal(input.value, "https://receiver.example.test/hooks");
    await submitWebhook(input);

    const create = calls.find((call) => call.options.method === "PUT");
    assert.ok(
      create,
      JSON.stringify(
        calls.map(({ path, options }) => ({
          path,
          method: options.method,
          body: options.body,
        })),
      ),
    );
    assert.deepEqual(JSON.parse(JSON.stringify(create.options.body)), {
      version: 0,
      url: "https://receiver.example.test/hooks",
      enabled: false,
    });
    assert.match(harness.host.textContent, /one-time-signing-secret/);
    assert.match(harness.host.textContent, /Webhook сохранён и выключен/);
    assert.match(harness.host.textContent, /temporarily_unavailable/);
    assert.equal(
      calls.filter((call) => call.options.method === "PUT").length,
      1,
      "a failed history refresh must not replay the committed write",
    );

    await clickButton(harness, "Включить webhook");
    const writes = calls.filter((call) => call.options.method === "PUT");
    assert.equal(writes.length, 2);
    assert.equal(writes[1].options.body.enabled, true);
    assert.equal(writes[1].options.body.id, "webhook-1");
    assert.equal(writes[1].options.body.version, 1);
  } finally {
    await harness.unmount();
  }
});

test("an ambiguous webhook create reconciles with GET without replaying PUT or exposing a lost secret", async () => {
  const harness = createHarness();
  const calls = [];
  let configReads = 0;
  const serverWebhook = {
    id: "webhook-created-on-server",
    monitorId: "monitor-1",
    url: "https://receiver.example.test/hooks",
    enabled: false,
    version: 1,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/monitors/monitor-1/webhook" && !options.method) {
      configReads += 1;
      if (configReads === 1) throw new ApiError(404, "not_found");
      return { webhook: serverWebhook };
    }
    if (path.endsWith("/webhook/deliveries?limit=50"))
      return { deliveries: [] };
    if (path === "/monitors/monitor-1/webhook" && options.method === "PUT") {
      throw new ApiError(0, "network_error");
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const WebhookNotifications = loadWebhookNotifications(api);

  try {
    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-1" }),
    );
    await settle();
    const input = harness.host.querySelector('input[name="webhookUrl"]');
    await fillWebhookURL(input, serverWebhook.url);
    await submitWebhook(input);

    assert.equal(
      calls.filter((call) => call.options.method === "PUT").length,
      1,
    );
    assert.equal(
      configReads,
      2,
      "the uncertain write must be reconciled with GET",
    );
    assert.match(harness.host.textContent, /ответ с новым секретом потерян/);
    assert.doesNotMatch(harness.host.textContent, /lost-signing-secret/);
    assert.doesNotMatch(harness.host.textContent, /Скопировать секрет/);
    assert.equal(
      harness.host.querySelector('input[name="webhookUrl"]').value,
      serverWebhook.url,
    );
  } finally {
    await harness.unmount();
  }
});

test("a clipboard write started for an old secret cannot mark a rotated secret copied", async () => {
  const harness = createHarness();
  const calls = [];
  let resolveClipboard;
  let copiedValue = "";
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText(value) {
        copiedValue = value;
        return new Promise((resolve) => {
          resolveClipboard = resolve;
        });
      },
    },
  });
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/monitors/monitor-1/webhook" && !options.method)
      throw new ApiError(404, "not_found");
    if (path.endsWith("/webhook/deliveries?limit=50"))
      return { deliveries: [] };
    if (path === "/monitors/monitor-1/webhook" && options.method === "PUT") {
      const input = options.body;
      const rotating = input.rotateSecret === true;
      return {
        webhook: {
          id: "webhook-1",
          monitorId: "monitor-1",
          url: "https://receiver.example.test/hooks",
          enabled: false,
          version: rotating ? 2 : 1,
          createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z",
        },
        secret: rotating ? "secret-B" : "secret-A",
      };
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const WebhookNotifications = loadWebhookNotifications(api);

  try {
    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-1" }),
    );
    await settle();
    const input = harness.host.querySelector('input[name="webhookUrl"]');
    await fillWebhookURL(input, "https://receiver.example.test/hooks");
    await submitWebhook(input);
    assert.match(harness.host.textContent, /secret-A/);

    await clickButton(harness, "Скопировать секрет");
    assert.equal(copiedValue, "secret-A");
    await clickButton(harness, "Обновить секрет");
    await clickButton(harness, "Подтвердить обновление секрета");
    assert.match(harness.host.textContent, /secret-B/);
    assert.doesNotMatch(harness.host.textContent, /secret-A/);

    await act(async () => resolveClipboard());
    await settle();
    assert.match(
      [...harness.host.querySelectorAll("button")].find((button) =>
        button.textContent.includes("секрет"),
      )?.textContent ?? "",
      /Скопировать секрет/,
    );
    assert.doesNotMatch(harness.host.textContent, /Секрет скопирован/);
    assert.equal(
      calls.filter((call) => call.options.method === "PUT").length,
      2,
    );
  } finally {
    await harness.unmount();
  }
});

test("stale webhook writes reload the current endpoint without retaining a secret", async () => {
  const harness = createHarness();
  const calls = [];
  let configReads = 0;
  const current = {
    id: "webhook-new-id",
    monitorId: "monitor-1",
    url: "https://receiver.example.test/current",
    enabled: false,
    version: 1,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/monitors/monitor-1/webhook" && !options.method) {
      configReads += 1;
      return {
        webhook:
          configReads === 1
            ? {
                ...current,
                id: "webhook-old-id",
                url: "https://receiver.example.test/old",
              }
            : current,
      };
    }
    if (path.endsWith("/webhook/deliveries?limit=50"))
      return { deliveries: [] };
    if (path === "/monitors/monitor-1/webhook" && options.method === "PUT")
      throw new ApiError(409, "version_conflict");
    throw new Error(`Unexpected request: ${path}`);
  };
  const WebhookNotifications = loadWebhookNotifications(api);

  try {
    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-1" }),
    );
    await settle();
    await clickButton(harness, "Обновить секрет");
    await clickButton(harness, "Подтвердить обновление секрета");

    const write = calls.find((call) => call.options.method === "PUT");
    assert.equal(write.options.body.id, "webhook-old-id");
    assert.equal(write.options.body.version, 1);
    assert.equal(
      calls.filter((call) => call.options.method === "PUT").length,
      1,
    );
    assert.equal(
      harness.host.querySelector('input[name="webhookUrl"]').value,
      current.url,
    );
    assert.doesNotMatch(
      harness.host.textContent,
      /Секрет подписи — показывается один раз/,
    );
    assert.equal(configReads, 2);
  } finally {
    await harness.unmount();
  }
});

test("changing monitors discards a pending webhook secret response", async () => {
  const harness = createHarness();
  const calls = [];
  let finishWrite;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path.endsWith("/webhook/deliveries?limit=50"))
      return { deliveries: [] };
    if (path.endsWith("/webhook") && !options.method)
      throw new ApiError(404, "not_found");
    if (path.endsWith("/webhook") && options.method === "PUT")
      return new Promise((resolve) => {
        finishWrite = resolve;
      });
    throw new Error(`Unexpected request: ${path}`);
  };
  const WebhookNotifications = loadWebhookNotifications(api);

  try {
    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-1" }),
    );
    await settle();
    const input = harness.host.querySelector('input[name="webhookUrl"]');
    await fillWebhookURL(input, "https://receiver.example.test/old-monitor");
    await submitWebhook(input);
    assert.equal(typeof finishWrite, "function");

    await harness.render(
      React.createElement(WebhookNotifications, { monitorId: "monitor-2" }),
    );
    await settle();
    finishWrite({
      webhook: {
        id: "webhook-old",
        monitorId: "monitor-1",
        url: "https://receiver.example.test/old-monitor",
        enabled: false,
        version: 1,
        createdAt: "2026-01-01T00:00:00Z",
        updatedAt: "2026-01-01T00:00:00Z",
      },
      secret: "late-secret-for-old-monitor",
    });
    await settle();

    assert.ok(
      calls.some((call) => call.path === "/monitors/monitor-2/webhook"),
    );
    assert.doesNotMatch(
      harness.host.textContent,
      /late-secret-for-old-monitor/,
    );
    assert.doesNotMatch(harness.host.textContent, /old-monitor/);
  } finally {
    await harness.unmount();
  }
});

test("Telegram group and topic permission failures stay explicit before TTL and restart once with the same kind", async (t) => {
  for (const { kind, option } of [
    { kind: "group", option: "Telegram · группа" },
    { kind: "topic", option: "Telegram · топик группы" },
  ]) {
    await t.test(kind, async () => {
      const harness = createHarness();
      const writes = [];
      let linkNumber = 0;
      const futureExpiry = new Date(Date.now() + 10 * 60_000).toISOString();
      const api = async (path, options = {}) => {
        if (path === "/messenger-destinations")
          return { enabled: true, emailEnabled: false, destinations: [] };
        if (path === "/messenger-links" && options.method === "POST") {
          writes.push(options.body);
          linkNumber += 1;
          return {
            link:
              linkNumber === 1
                ? messengerLink(
                    `link-${linkNumber}`,
                    kind,
                    "failed",
                    "telegram_bot_admin_required",
                    futureExpiry,
                  )
                : messengerLink(
                    `link-${linkNumber}`,
                    kind,
                    "pending",
                    undefined,
                    futureExpiry,
                  ),
            startUrl: null,
            command: "/connect@heartbeat_test_bot token",
          };
        }
        throw new Error(`Unexpected request: ${path}`);
      };
      const MessengerLinks = loadMessengerLinks(
        api,
        Date.now() + 2 * 24 * 60 * 60_000,
      );
      try {
        await harness.render(React.createElement(MessengerLinks));
        await settle();
        await clickMessengerButton(harness, option);
        assert.match(
          harness.host.textContent,
          /Назначьте Informer администратором группы/,
        );
        assert.doesNotMatch(
          harness.host.textContent,
          /Срок действия ссылки истёк/,
        );
        assert.ok(
          [...harness.host.querySelectorAll("button")].some((button) =>
            button.textContent.includes("Начать заново"),
          ),
          "a permanent permission failure should offer an explicit restart before its link TTL",
        );

        const restart = [...harness.host.querySelectorAll("button")].find(
          (button) => button.textContent.includes("Начать заново"),
        );
        await act(async () => {
          restart.dispatchEvent(new MouseEvent("click", { bubbles: true }));
          restart.dispatchEvent(new MouseEvent("click", { bubbles: true }));
          await Promise.resolve();
        });
        await settle();
        assert.equal(
          JSON.stringify(writes),
          JSON.stringify([
            { provider: "telegram", kind },
            { provider: "telegram", kind },
          ]),
        );
        assert.match(harness.host.textContent, /Ожидаем сообщение боту/);
      } finally {
        await harness.unmount();
      }
    });
  }
});

test("server link status wins over browser clock skew and legacy expiry does not claim a known cause", async (t) => {
  await t.test(
    "a server candidate can be confirmed when the browser clock is ahead",
    async () => {
      const harness = createHarness();
      const confirmCalls = [];
      const expiresAt = new Date(Date.now() + 60 * 60_000).toISOString();
      const api = async (path, options = {}) => {
        if (path === "/messenger-destinations")
          return { enabled: true, emailEnabled: false, destinations: [] };
        if (path === "/messenger-links" && options.method === "POST")
          return {
            link: messengerLink(
              "candidate-1",
              "group",
              "candidate",
              undefined,
              expiresAt,
            ),
            startUrl: null,
            command: "/connect@heartbeat_test_bot token",
          };
        if (path === "/messenger-links/candidate-1/confirm") {
          confirmCalls.push(options.method);
          return {
            destination: {
              id: "destination-1",
              provider: "telegram",
              kind: "group",
              label: "Operations",
              chatId: "-10077",
              userId: "321",
              threadId: null,
              ready: true,
              version: 1,
            },
          };
        }
        throw new Error(`Unexpected request: ${path}`);
      };
      const MessengerLinks = loadMessengerLinks(
        api,
        Date.now() + 2 * 24 * 60 * 60_000,
      );
      try {
        await harness.render(React.createElement(MessengerLinks));
        await settle();
        await clickMessengerButton(harness, "Telegram · группа");
        assert.ok(
          [...harness.host.querySelectorAll("button")].some((button) =>
            button.textContent.includes("Подтвердить этот чат"),
          ),
        );
        await clickMessengerButton(harness, "Подтвердить этот чат");
        assert.deepEqual(confirmCalls, ["POST"]);
        assert.match(harness.host.textContent, /Чат подтверждён/);
        assert.doesNotMatch(
          harness.host.textContent,
          /Срок действия ссылки истёк/,
        );
      } finally {
        await harness.unmount();
      }
    },
  );

  await t.test(
    "a server-expired link remains expired when the browser clock is behind",
    async () => {
      const harness = createHarness();
      const api = async (path) => {
        if (path === "/messenger-destinations")
          return { enabled: true, emailEnabled: false, destinations: [] };
        if (path === "/messenger-links" && path)
          return {
            link: messengerLink(
              "expired-1",
              "topic",
              "expired",
              "link_expired",
              new Date(Date.now() - 60_000).toISOString(),
            ),
            startUrl: null,
            command: "/connect@heartbeat_test_bot token",
          };
        throw new Error(`Unexpected request: ${path}`);
      };
      const MessengerLinks = loadMessengerLinks(
        api,
        Date.now() - 2 * 24 * 60 * 60_000,
      );
      try {
        await harness.render(React.createElement(MessengerLinks));
        await settle();
        await clickMessengerButton(harness, "Telegram · топик группы");
        assert.match(harness.host.textContent, /Срок действия ссылки истёк/);
        assert.ok(
          [...harness.host.querySelectorAll("button")].some((button) =>
            button.textContent.includes("Начать заново"),
          ),
        );
        assert.equal(
          [...harness.host.querySelectorAll("button")].some((button) =>
            button.textContent.includes("Подтвердить этот чат"),
          ),
          false,
        );
      } finally {
        await harness.unmount();
      }
    },
  );

  await t.test("a legacy expired row stays a generic failure", async () => {
    const harness = createHarness();
    const api = async (path) => {
      if (path === "/messenger-destinations")
        return { enabled: true, emailEnabled: false, destinations: [] };
      if (path === "/messenger-links" && path)
        return {
          link: messengerLink(
            "legacy-expired",
            "group",
            "expired",
            undefined,
            new Date(Date.now() - 60_000).toISOString(),
          ),
          startUrl: null,
          command: "/connect@heartbeat_test_bot token",
        };
      throw new Error(`Unexpected request: ${path}`);
    };
    const MessengerLinks = loadMessengerLinks(
      api,
      Date.now() - 2 * 24 * 60 * 60_000,
    );
    try {
      await harness.render(React.createElement(MessengerLinks));
      await settle();
      await clickMessengerButton(harness, "Telegram · группа");
      assert.match(harness.host.textContent, /Эта попытка привязки недоступна/);
      assert.doesNotMatch(
        harness.host.textContent,
        /Срок действия ссылки истёк/,
      );
    } finally {
      await harness.unmount();
    }
  });
});

test("closing a link invalidates its pending poll before a new group link starts", async () => {
  const harness = createHarness();
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  });
  let starts = 0;
  let resolveOldPoll;
  let markPollStarted;
  const pollStarted = new Promise((resolve) => {
    markPollStarted = resolve;
  });
  const oldPoll = new Promise((resolve) => {
    resolveOldPoll = resolve;
  });
  const api = async (path, options = {}) => {
    if (path === "/messenger-destinations")
      return { enabled: true, emailEnabled: false, destinations: [] };
    if (path === "/messenger-links" && options.method === "POST") {
      starts += 1;
      return {
        link: messengerLink(
          `link-${starts}`,
          "group",
          "pending",
          undefined,
          new Date(Date.now() + 10 * 60_000).toISOString(),
        ),
        startUrl: null,
        command: `/connect@heartbeat_test_bot token-${starts}`,
      };
    }
    if (path === "/messenger-links/link-1") {
      markPollStarted();
      return oldPoll;
    }
    if (path === "/messenger-links/link-2")
      return {
        link: messengerLink(
          "link-2",
          "group",
          "pending",
          undefined,
          new Date(Date.now() + 10 * 60_000).toISOString(),
        ),
      };
    throw new Error(`Unexpected request: ${path}`);
  };
  const MessengerLinks = loadMessengerLinks(api);
  try {
    await harness.render(React.createElement(MessengerLinks));
    await settle();
    await clickMessengerButton(harness, "Telegram · группа");
    await Promise.race([
      pollStarted,
      new Promise((_, reject) =>
        setTimeout(() => reject(new Error("poll did not start")), 3500),
      ),
    ]);
    await clickMessengerButton(harness, "Закрыть окно");
    await clickMessengerButton(harness, "Telegram · группа");
    assert.match(harness.host.textContent, /token-2/);
    resolveOldPoll({
      link: messengerLink(
        "link-1",
        "group",
        "candidate",
        undefined,
        new Date(Date.now() + 10 * 60_000).toISOString(),
      ),
    });
    await settle();
    assert.match(harness.host.textContent, /token-2/);
    assert.doesNotMatch(harness.host.textContent, /Operations/);
    assert.match(harness.host.textContent, /Ожидаем сообщение боту/);
  } finally {
    await harness.unmount();
  }
});

test("a candidate poll started before confirmation cannot replace the confirmed result", async () => {
  const harness = createHarness();
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: "visible",
  });
  const oldPoll = deferred();
  let pollStartedResolve;
  let didStartPoll = false;
  const pollStarted = new Promise((resolve) => {
    pollStartedResolve = resolve;
  });
  const destination = {
    id: "destination-confirmed",
    provider: "telegram",
    kind: "topic",
    label: "Operations · deploys",
    chatId: "-10077",
    userId: "321",
    threadId: "44",
    ready: true,
    version: 1,
  };
  const api = async (path, options = {}) => {
    if (path === "/messenger-destinations")
      return { enabled: true, emailEnabled: false, destinations: [] };
    if (path === "/messenger-links" && options.method === "POST")
      return {
        link: messengerLink(
          "candidate-topic",
          "topic",
          "candidate",
          undefined,
          new Date(Date.now() + 10 * 60_000).toISOString(),
        ),
        startUrl: null,
        command: "/connect@heartbeat_test_bot topic-token",
      };
    if (path === "/messenger-links/candidate-topic") {
      didStartPoll = true;
      pollStartedResolve();
      return oldPoll.promise;
    }
    if (
      path === "/messenger-links/candidate-topic/confirm" &&
      options.method === "POST"
    )
      return { destination };
    throw new Error(`Unexpected request: ${path}`);
  };
  const MessengerLinks = loadMessengerLinks(api);
  try {
    await harness.render(React.createElement(MessengerLinks));
    await settle();
    await clickMessengerButton(harness, "Telegram · топик группы");
    await Promise.race([
      pollStarted,
      new Promise((resolve) => setTimeout(resolve, 3500)),
    ]);
    assert.equal(
      didStartPoll,
      true,
      "candidate polling should be active before confirmation",
    );
    await clickMessengerButton(harness, "Подтвердить этот чат");
    assert.match(harness.host.textContent, /Чат подтверждён/);
    oldPoll.resolve({
      link: messengerLink(
        "candidate-topic",
        "topic",
        "expired",
        "link_expired",
        new Date(Date.now() - 60_000).toISOString(),
      ),
    });
    await settle();
    assert.equal(harness.host.querySelector('[role="dialog"]'), null);
    assert.match(harness.host.textContent, /Чат подтверждён/);
    assert.doesNotMatch(harness.host.textContent, /Срок действия ссылки истёк/);
  } finally {
    await harness.unmount();
  }
});

test("an ambiguous confirmation is reconciled by GET without replaying its POST", async () => {
  const harness = createHarness();
  const confirmationWrites = [];
  const destination = {
    id: "destination-reconciled",
    provider: "telegram",
    kind: "group",
    label: "Operations",
    chatId: "-10077",
    userId: "321",
    threadId: null,
    ready: true,
    version: 1,
  };
  const api = async (path, options = {}) => {
    if (path === "/messenger-destinations")
      return { enabled: true, emailEnabled: false, destinations: [] };
    if (path === "/messenger-links" && options.method === "POST")
      return {
        link: messengerLink(
          "confirm-timeout",
          "group",
          "candidate",
          undefined,
          new Date(Date.now() + 10 * 60_000).toISOString(),
        ),
        startUrl: null,
        command: "/connect@heartbeat_test_bot token",
      };
    if (
      path === "/messenger-links/confirm-timeout/confirm" &&
      options.method === "POST"
    ) {
      confirmationWrites.push("POST");
      throw new Error("connection ended before response");
    }
    if (path === "/messenger-links/confirm-timeout")
      return {
        link: {
          ...messengerLink(
            "confirm-timeout",
            "group",
            "confirmed",
            undefined,
            new Date(Date.now() + 10 * 60_000).toISOString(),
          ),
          destinationId: destination.id,
        },
      };
    throw new Error(`Unexpected request: ${path}`);
  };
  const MessengerLinks = loadMessengerLinks(api);
  try {
    await harness.render(React.createElement(MessengerLinks));
    await settle();
    await clickMessengerButton(harness, "Telegram · группа");
    await clickMessengerButton(harness, "Подтвердить этот чат");
    assert.deepEqual(confirmationWrites, ["POST"]);
    assert.match(
      harness.host.textContent,
      /Чат подтверждён\. Теперь его можно выбрать для монитора/,
    );
    assert.equal(
      harness.host.textContent.includes("Подтвердить этот чат"),
      false,
    );
    assert.equal(
      confirmationWrites.length,
      1,
      "reconciliation must not replay the confirmation write",
    );
  } finally {
    await harness.unmount();
  }
});

test("disconnect is not undone by a destinations read started after confirmation", async () => {
  const harness = createHarness();
  let destinationReads = 0;
  const delayedRefresh = deferred();
  const destination = {
    id: "destination-race",
    provider: "telegram",
    kind: "group",
    label: "Operations",
    chatId: "-10077",
    userId: "321",
    threadId: null,
    ready: true,
    version: 1,
  };
  const api = async (path, options = {}) => {
    if (path === "/messenger-destinations") {
      destinationReads += 1;
      if (destinationReads === 2) return delayedRefresh.promise;
      return { enabled: true, emailEnabled: false, destinations: [] };
    }
    if (path === "/messenger-links" && options.method === "POST")
      return {
        link: messengerLink(
          "destination-race-link",
          "group",
          "candidate",
          undefined,
          new Date(Date.now() + 10 * 60_000).toISOString(),
        ),
        startUrl: null,
        command: "/connect@heartbeat_test_bot token",
      };
    if (
      path === "/messenger-links/destination-race-link/confirm" &&
      options.method === "POST"
    )
      return { destination };
    if (
      path === "/messenger-destinations/destination-race" &&
      options.method === "DELETE"
    )
      return undefined;
    throw new Error(`Unexpected request: ${path}`);
  };
  const MessengerLinks = loadMessengerLinks(api);
  try {
    await harness.render(React.createElement(MessengerLinks));
    await settle();
    await clickMessengerButton(harness, "Telegram · группа");
    await clickMessengerButton(harness, "Подтвердить этот чат");
    assert.match(harness.host.textContent, /Operations/);
    assert.equal(
      destinationReads,
      2,
      "confirmation should start its follow-up read",
    );

    await clickMessengerButton(harness, "Отключить");
    const dialog = harness.host.querySelector('[role="dialog"]');
    const confirmDisconnect = [...dialog.querySelectorAll("button")].find(
      (button) => button.textContent.includes("Отключить"),
    );
    assert.ok(confirmDisconnect);
    await act(async () =>
      confirmDisconnect.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      ),
    );
    await settle();
    assert.equal(
      destinationReads,
      3,
      "disconnect should load the current destination list",
    );
    assert.match(harness.host.textContent, /Пока нет подтверждённых чатов/);

    delayedRefresh.resolve({
      enabled: true,
      emailEnabled: false,
      destinations: [destination],
    });
    await settle();
    assert.match(harness.host.textContent, /Пока нет подтверждённых чатов/);
    assert.doesNotMatch(harness.host.textContent, /Operations/);
  } finally {
    await harness.unmount();
  }
});
