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
