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
const labels = {
  pending: "Ожидает настройки в приложении",
  ready: "Настроен для уведомлений",
  permission_denied: "Уведомления запрещены на телефоне",
  session_expired: "Срок подключения истёк",
  session_revoked: "Подключение отозвано",
  invalid_token: "Нужно обновить подключение",
  provider_unavailable: "Сервис push временно недоступен",
};

class ApiError extends Error {
  constructor(status, code = "request_failed") {
    super(code);
    this.status = status;
    this.code = code;
  }
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

function harness(url = "http://localhost/app/settings", initialStorage = {}) {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url });
  for (const [key, value] of Object.entries(initialStorage))
    dom.window.sessionStorage.setItem(key, value);
  dom.window.setInterval = () => 1;
  dom.window.clearInterval = () => {};
  const assigned = [];
  const appWindow = {
    location: {
      search: dom.window.location.search,
      assign: (value) => assigned.push(value),
    },
    sessionStorage: dom.window.sessionStorage,
    setInterval: dom.window.setInterval.bind(dom.window),
    clearInterval: dom.window.clearInterval.bind(dom.window),
  };
  globalThis.window = appWindow;
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
    assigned,
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

function load(relativePath, aliases) {
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
      if (id === "@/shared/notifications")
        return { notify: { success() {}, error() {} } };
      return requireWeb(id);
    },
    console,
    Date,
    URL,
    URLSearchParams,
    AbortController,
    DOMException: globalThis.DOMException,
    setTimeout,
    clearTimeout,
    window: globalThis.window,
    document: globalThis.document,
    FormData: globalThis.FormData,
    navigator: globalThis.navigator,
    self: globalThis.window,
  });
  return fixtureModule.exports;
}

function apiTools(api) {
  return {
    api,
    ApiError,
    boundedRequest: (_controller, request) => request(_controller.signal),
    errorMessage: (error) => String(error?.message ?? "Ошибка"),
  };
}

function pairing(id = "pair-1", overrides = {}) {
  return {
    id,
    deviceLabel: "Pixel 9",
    status: "pending",
    expiresAt: new Date(Date.now() + 300_000).toISOString(),
    ...overrides,
  };
}

function confirmation(id = "pair-1", expiresAt = pairing(id).expiresAt) {
  return {
    pairingId: id,
    completionCode: "A".repeat(43),
    clientState: `${"A".repeat(42)}Q`,
    expiresAt,
  };
}

function installation(overrides = {}) {
  return {
    id: "phone-1",
    provider: "rustore",
    deviceLabel: "Pixel 9",
    status: "ready",
    bindingGeneration: 1,
    registrationVersion: 1,
    leaseExpiresAt: null,
    lastPairingId: null,
    lastSeenAt: new Date().toISOString(),
    ...overrides,
  };
}

function loadSettings(
  api,
  getProfile = () => ({
    account: { id: "account-1", name: "Команда 1" },
    email: "owner@example.test",
  }),
  androidPushIntent = () => "intent://complete",
) {
  const aliases = {
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
    "@/entities/session": { useSession: () => ({ profile: getProfile() }) },
    "@/entities/push": { pushStatusLabels: labels },
    "@/shared/api": apiTools(api),
    "@/shared/android": {
      androidPushIntent,
      pushPairingId: (params) => params.get("pushPair"),
    },
    "@/shared/ui": {
      dateTime: (value) => String(value),
      Icon: () => null,
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
  };
  return load("../src/features/android-push/index.tsx", aliases)
    .AndroidPushSettings;
}

function loadRoutes(api) {
  const aliases = {
    "@/entities/session": {
      useSession: () => ({ profile: { account: { id: "account-1" } } }),
    },
    "@/entities/push": { pushStatusLabels: labels },
    "@/shared/api": apiTools(api),
    "@/shared/ui": {
      Icon: () => null,
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
    },
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
  };
  return load("../src/features/monitor-notifications/push.tsx", aliases)
    .AndroidPushRoutes;
}

test("pairing confirmation is browser-only, single-issue, and offers the same app return link", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const calls = [];
  const intents = [];
  const pairResponse = pairing();
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") return pairResponse;
    if (path.endsWith("/confirm"))
      return confirmation("pair-1", pairResponse.expiresAt);
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api, undefined, (action, payload) => {
    intents.push({ action, payload });
    return "intent://complete";
  });
  await view.render(React.createElement(Settings));
  assert.match(view.host.textContent, /Продолжите в приложении/);
  assert.match(view.host.textContent, /Вернуться в приложение/);
  assert.doesNotMatch(view.host.textContent, /042681|Подтвердить подключение/);
  assert.equal(view.host.querySelector('input[type="checkbox"]'), null);
  assert.deepEqual(
    calls.map(({ path, options }) => [path, options.method ?? "GET"]),
    [
      ["/push/installations", "GET"],
      ["/push/pairings/pair-1", "GET"],
      ["/push/pairings/pair-1/confirm", "POST"],
    ],
  );
  assert.equal(Object.keys(calls.at(-1).options.body).length, 0);
  assert.equal(
    intents.length,
    2,
    "automatic and manual return use the same completion",
  );
  assert.deepEqual(view.assigned, ["intent://complete"]);
  for (const intent of intents) {
    assert.equal(intent.action, "complete");
    assert.deepEqual(
      intent.payload,
      confirmation("pair-1", pairResponse.expiresAt),
    );
  }
  assert.equal(
    calls.some(
      ({ path, options }) =>
        path.includes("completionCode") ||
        options.url?.includes("completionCode"),
    ),
    false,
    "completion proof never enters an HTTP URL",
  );
});

test("expired pairing never issues a completion code", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1")
      return pairing("pair-1", {
        expiresAt: new Date(Date.now() - 60_000).toISOString(),
      });
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api);
  await view.render(React.createElement(Settings));
  assert.match(view.host.textContent, /Срок подключения истёк/);
  assert.equal(
    calls.some(({ options }) => options.method === "POST"),
    false,
  );
});

test("foreign pairing 404 remains read-only", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") throw new ApiError(404, "not_found");
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api);
  await view.render(React.createElement(Settings));
  assert.match(
    view.host.textContent,
    /Подключение не найдено или срок ссылки истёк/,
  );
  assert.equal(
    calls.some(({ options }) => options.method === "POST"),
    false,
  );
});

test("ambiguous confirmation is never retried after remount", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") return pairing();
    if (path.endsWith("/confirm")) throw new Error("connection lost");
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api);
  await view.render(React.createElement(Settings));
  assert.match(
    view.host.textContent,
    /подтверждение не повторяется автоматически/,
  );
  assert.equal(
    calls.filter(({ options }) => options.method === "POST").length,
    1,
  );
  const storage = view.host.ownerDocument.defaultView.sessionStorage;
  const savedMarker = Object.fromEntries(
    Array.from({ length: storage.length }, (_, index) => {
      const key = storage.key(index);
      return [key, storage.getItem(key)];
    }),
  );
  await view.unmount();

  const remounted = harness(
    "http://localhost/app/settings?pushPair=pair-1",
    savedMarker,
  );
  t.after(() => remounted.unmount());
  const RemountedSettings = loadSettings(api);
  await remounted.render(React.createElement(RemountedSettings));
  assert.match(remounted.host.textContent, /Подтверждение уже запрошено/);
  assert.equal(
    calls.filter(({ options }) => options.method === "POST").length,
    1,
  );
  assert.equal(
    calls.filter(({ path }) => path === "/push/pairings/pair-1").length,
    2,
  );
});

test("monitor push stays off by default and preserves the draft after a CAS conflict", async (t) => {
  const view = harness("http://localhost/app/monitors/monitor-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [installation()] };
    if (path === "/monitors/monitor-1/push") {
      if (options.method === "PUT") throw new ApiError(409, "version_conflict");
      return { enabled: false, version: 0 };
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const Routes = loadRoutes(api);
  await view.render(React.createElement(Routes, { monitorId: "monitor-1" }));
  const checkbox = view.host.querySelector('input[type="checkbox"]');
  assert.equal(checkbox.checked, false);
  assert.equal(view.host.querySelector("form button").disabled, false);
  assert.equal(
    calls.some(({ options }) => options.method),
    false,
  );
  await act(async () => checkbox.click());
  await act(async () => view.host.querySelector("form button").click());
  assert.equal(
    checkbox.checked,
    true,
    "the user's pending selection stays visible after conflict",
  );
  assert.match(view.host.textContent, /Настройки уже изменены/);
  const puts = calls.filter(({ options }) => options.method === "PUT");
  assert.equal(puts.length, 1, "a 409 does not trigger an automatic retry");
  assert.equal(puts[0].options.body.enabled, true);
  assert.equal(puts[0].options.body.version, 0);
  assert.ok(
    [...view.host.querySelectorAll("button")].some((button) =>
      button.textContent.includes("Обновить настройки Push"),
    ),
  );
});

test("explicit off can be saved before the first phone is connected", async (t) => {
  const view = harness("http://localhost/app/monitors/monitor-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/monitors/monitor-1/push") {
      if (options.method === "PUT") return { enabled: false, version: 1 };
      return { enabled: false, version: 0 };
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const Routes = loadRoutes(api);
  await view.render(React.createElement(Routes, { monitorId: "monitor-1" }));
  assert.equal(
    view.host.querySelector('input[type="checkbox"]').checked,
    false,
  );
  await act(async () => view.host.querySelector("form button").click());
  const puts = calls.filter(({ options }) => options.method === "PUT");
  assert.equal(puts.length, 1);
  assert.equal(puts[0].options.body.enabled, false);
  assert.equal(puts[0].options.body.version, 0);
  assert.equal(view.host.querySelector("form button").disabled, true);
});

test("account change and unmount discard a late pairing response", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const firstPairing = deferred();
  let account = "account-1";
  let lastPairing;
  const reads = [];
  const confirmations = [];
  const api = async (path) => {
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") {
      reads.push(account);
      if (account === "account-1") return firstPairing.promise;
      lastPairing = pairing("pair-1", { deviceLabel: "New account phone" });
      return lastPairing;
    }
    if (path.endsWith("/confirm")) {
      confirmations.push(account);
      return confirmation("pair-1", lastPairing.expiresAt);
    }
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api, () => ({
    account: { id: account, name: account },
    email: `${account}@example.test`,
  }));
  await view.render(React.createElement(Settings));
  account = "account-2";
  await view.render(React.createElement(Settings));
  assert.deepEqual(reads, ["account-1", "account-2"]);
  firstPairing.resolve(pairing("pair-1", { deviceLabel: "Old account phone" }));
  await act(async () => Promise.resolve());
  await act(async () => Promise.resolve());
  assert.match(view.host.textContent, /New account phone/);
  assert.doesNotMatch(view.host.textContent, /Old account phone/);
  assert.deepEqual(
    confirmations,
    ["account-2"],
    "the stale account response cannot confirm",
  );
});
