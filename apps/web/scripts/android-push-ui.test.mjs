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

function harness(url = "http://localhost/app/settings") {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url });
  dom.window.setInterval = () => 1;
  dom.window.clearInterval = () => {};
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
    verificationCode: "042681",
    status: "pending",
    expiresAt: new Date(Date.now() + 300_000).toISOString(),
    ...overrides,
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
) {
  const aliases = {
    "next/link": ({ href, children }) =>
      React.createElement("a", { href }, children),
    "@/entities/session": { useSession: () => ({ profile: getProfile() }) },
    "@/entities/push": { pushStatusLabels: labels },
    "@/shared/api": apiTools(api),
    "@/shared/android": {
      androidPushIntent: () => "#intent",
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

function loadRoutes(api, onSaved = () => {}) {
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

test("pairing code is shown before confirmation and viewing sends no mutation", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") return pairing();
    if (path.endsWith("/confirm")) return {};
    throw new Error(`Unexpected request: ${path}`);
  };
  const Settings = loadSettings(api);
  await view.render(React.createElement(Settings));
  assert.match(view.host.textContent, /042681/);
  const confirm = [...view.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Подтвердить подключение"),
  );
  assert.ok(confirm);
  assert.equal(
    confirm.disabled,
    true,
    "the account owner must explicitly acknowledge the displayed code",
  );
  assert.equal(
    calls.some(({ options }) => options.method),
    false,
    "opening a pairing link is read-only",
  );

  const checkbox = view.host.querySelector('input[type="checkbox"]');
  await act(async () => {
    checkbox.click();
  });
  await act(async () => confirm.click());
  assert.deepEqual(
    calls
      .filter(({ options }) => options.method)
      .map(({ path, options }) => [path, options.method]),
    [["/push/pairings/pair-1/confirm", "POST"]],
  );
});

test("expired pairing never enables confirmation", async (t) => {
  await t.test("expired", async () => {
    const view = harness("http://localhost/app/settings?pushPair=pair-1");
    const calls = [];
    const api = async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/push/installations")
        return { enabled: true, installations: [] };
      if (path === "/push/pairings/pair-1") {
        return pairing("pair-1", {
          expiresAt: new Date(Date.now() - 60_000).toISOString(),
        });
      }
      throw new Error(`Unexpected request: ${path}`);
    };
    const Settings = loadSettings(api);
    await view.render(React.createElement(Settings));
    assert.match(view.host.textContent, /Срок подтверждения истёк/);
    assert.equal(view.host.querySelector('input[type="checkbox"]'), null);
    assert.equal(
      calls.some(({ options }) => options.method),
      false,
    );
    await view.unmount();
  });
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
    calls.some(({ options }) => options.method),
    false,
  );
});

test("ambiguous pairing confirmation requires a manual read before any retry", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
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
  await act(async () =>
    view.host.querySelector('input[type="checkbox"]').click(),
  );
  await act(async () => {
    [...view.host.querySelectorAll("button")]
      .find((button) => button.textContent.includes("Подтвердить подключение"))
      .click();
  });
  assert.match(view.host.textContent, /Обновите состояние перед повтором/);
  assert.equal(
    calls.filter(({ options }) => options.method === "POST").length,
    1,
  );
  await act(async () => {
    [...view.host.querySelectorAll("button")]
      .find((button) => button.textContent.includes("Обновить состояние"))
      .click();
  });
  assert.equal(
    calls.filter(({ options }) => options.method === "POST").length,
    1,
  );
  assert.equal(
    calls.filter(({ path }) => path === "/push/pairings/pair-1").length,
    2,
  );
});

test("a monitor route stays off by default and a 409 preserves the draft without retry", async (t) => {
  const view = harness("http://localhost/app/monitors/monitor-1");
  t.after(() => view.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/push/installations")
      return { enabled: true, installations: [installation()] };
    if (path === "/monitors/monitor-1/push-routes") return { routes: [] };
    if (options.method === "PUT") throw new ApiError(409, "version_conflict");
    throw new Error(`Unexpected request: ${path}`);
  };
  const Routes = loadRoutes(api);
  await view.render(React.createElement(Routes, { monitorId: "monitor-1" }));
  const checkbox = view.host.querySelector('input[type="checkbox"]');
  assert.equal(checkbox.checked, false);
  assert.equal(view.host.querySelector("form button").disabled, true);
  assert.equal(
    calls.some(({ options }) => options.method),
    false,
    "loading monitor settings must not enable a route",
  );

  await act(async () => checkbox.click());
  await act(async () => view.host.querySelector("form button").click());
  assert.equal(
    checkbox.checked,
    true,
    "the user's pending selection stays visible after a conflict",
  );
  assert.match(view.host.textContent, /Настройки уже изменены/);
  assert.equal(
    calls.filter(({ options }) => options.method === "PUT").length,
    1,
  );
  assert.ok(
    [...view.host.querySelectorAll("button")].some((button) =>
      button.textContent.includes("Обновить настройки Android push"),
    ),
  );
});

test("account change and unmount discard a late pairing read", async (t) => {
  const view = harness("http://localhost/app/settings?pushPair=pair-1");
  t.after(() => view.unmount());
  const firstPairing = deferred();
  let account = "account-1";
  const pairingReads = [];
  const api = async (path) => {
    if (path === "/push/installations")
      return { enabled: true, installations: [] };
    if (path === "/push/pairings/pair-1") {
      pairingReads.push(account);
      return account === "account-1"
        ? firstPairing.promise
        : pairing("pair-1", { deviceLabel: "New account phone" });
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
  assert.deepEqual(pairingReads, ["account-1", "account-2"]);
  firstPairing.resolve(pairing("pair-1", { deviceLabel: "Old account phone" }));
  await act(async () => Promise.resolve());
  await act(async () => Promise.resolve());
  assert.match(view.host.textContent, /New account phone/);
  assert.doesNotMatch(view.host.textContent, /Old account phone/);
});
