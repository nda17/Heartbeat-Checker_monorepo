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
const monitorHelpers = loadComponent("../src/entities/monitor/index.tsx", {});
const safeAppReturnTo = loadComponent(
  "../src/shared/api/index.ts",
  {},
).safeAppReturnTo;

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
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
    setInterval,
    clearInterval,
    requestAnimationFrame: globalThis.requestAnimationFrame,
    cancelAnimationFrame: globalThis.cancelAnimationFrame,
    window: globalThis.window,
    document: globalThis.document,
    FormData: globalThis.FormData,
    ...runtime,
  });
  return fixtureModule.exports;
}

function createHarness() {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "http://localhost/app",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: dom.window.navigator,
  });
  globalThis.HTMLElement = dom.window.HTMLElement;
  dom.window.HTMLElement.prototype.scrollIntoView ??= () => {};
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.FormData = dom.window.FormData;
  globalThis.requestAnimationFrame = (callback) => setTimeout(callback, 0);
  globalThis.cancelAnimationFrame = (id) => clearTimeout(id);
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  return {
    dom,
    host,
    async render(element) {
      await act(async () => root.render(element));
      await flush();
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      for (const key of [
        "window",
        "document",
        "navigator",
        "HTMLElement",
        "Event",
        "MouseEvent",
        "FormData",
        "requestAnimationFrame",
        "cancelAnimationFrame",
        "IS_REACT_ACT_ENVIRONMENT",
      ])
        delete globalThis[key];
    },
  };
}

function page(
  names,
  { page = 1, pageSize = 25, total = names.length, totalPages = 1 } = {},
) {
  return {
    monitors: names.map((name, index) =>
      monitor(`monitor-${name}`, name, index),
    ),
    pagination: { page, pageSize, total, totalPages },
    summary: {
      total,
      up: 1,
      down: 0,
      unknown: 0,
      paused: 0,
      subscriptionRequired: 0,
    },
  };
}

function monitor(id, name, index = 0) {
  return {
    id,
    checkType: "http",
    name,
    url: `https://${name.toLowerCase().replaceAll(" ", "-")}.example.test/health`,
    method: "GET",
    expectedStatus: 200,
    bodyContains: "",
    intervalMinutes: 5,
    failureThreshold: 2,
    enabled: true,
    version: 1,
    state: "UP",
    lastCheckedAt: new Date(Date.now() - index * 1000).toISOString(),
    lastHttpStatus: 200,
    lastLatencyMs: 20,
    nextCheckAt: new Date(Date.now() + 60_000).toISOString(),
    notificationChannels: [],
  };
}

function loadPage(api, query = "") {
  const session = { profile: { account: { id: "account-test" } } };
  const router = { replace() {}, push() {} };
  let currentQuery = query;
  const aliases = {
    "next/navigation": {
      useRouter: () => router,
      useSearchParams: () => new URLSearchParams(currentQuery),
    },
    "next/link": {
      default: ({ href, children, ...props }) =>
        React.createElement("a", { href, ...props }, children),
    },
    "@/features/monitor-form": { MonitorForm: () => null },
    "@/features/monitor-notifications": { MonitorNotifications: () => null },
    "@/entities/session": { useSession: () => session },
    "@/entities/monitor": {
      ...monitorHelpers,
      Status: ({ state }) => React.createElement("span", null, state),
    },
    "@/shared/api": {
      api,
      ApiError,
      errorMessage: (error) => error?.message ?? "Не удалось загрузить данные.",
      safeAppReturnTo,
    },
    "@/shared/dialog": {
      Dialog: ({ title, children }) =>
        React.createElement(
          "section",
          { role: "dialog", "aria-label": title },
          children,
        ),
    },
    "@/shared/android": {
      clearMonitorDraft() {},
      draftChanged: "draftChanged",
      readMonitorDraft: () => null,
    },
    "@/shared/ui": {
      Icon: ({ name }) => React.createElement("i", { "data-icon": name }),
      MessengerIcon: ({ brand }) =>
        React.createElement("i", { "data-brand": brand }),
      Loading: ({ text }) =>
        React.createElement("span", { role: "status" }, text),
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
      Spinner: () => React.createElement("span", null, "…"),
      dateTime: (value) => String(value ?? "—"),
    },
  };
  return {
    MonitorsPage: loadComponent("../src/_pages/monitors/index.tsx", aliases)
      .MonitorsPage,
    setQuery(value) {
      currentQuery = value;
    },
  };
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function click(harness, text) {
  const button = [...harness.host.querySelectorAll("button")].find((item) =>
    item.textContent.includes(text),
  );
  assert.ok(button, `missing button: ${text}`);
  await act(async () => button.click());
  await flush();
}

async function fillSearch(harness, value) {
  const input = harness.host.querySelector(
    'input[aria-label="Поиск мониторов"]',
  );
  assert.ok(input, "monitor search control should be available");
  const props =
    input[Object.keys(input).find((key) => key.startsWith("__reactProps"))];
  await act(async () => props.onChange({ target: { value } }));
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 350));
    await Promise.resolve();
    await Promise.resolve();
  });
}

function entitlement() {
  return {
    entitlement: {
      quota: 25,
      monitorCount: 2,
      source: "admin_grant",
      activeUntil: "2099-01-01T00:00:00Z",
    },
  };
}

test("monitor list sends remote search, sort and page controls and renders active channel badges", async (t) => {
  const harness = createHarness();
  t.after(() => harness.unmount());
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path.startsWith("/monitors?")) {
      const params = new URL(path, "http://localhost").searchParams;
      if (params.get("q") === "needle")
        return {
          ...page(["Server match despite nonmatching label"]),
          monitors: [
            {
              ...monitor(
                "remote-match",
                "Server match despite nonmatching label",
              ),
              notificationChannels: ["telegram", "email", "webhook"],
            },
          ],
        };
      if (params.get("page") === "2")
        return {
          ...page(["Second page"], { page: 2, total: 40, totalPages: 2 }),
          monitors: [
            { ...monitor("page-2", "Second page"), notificationChannels: [] },
          ],
        };
      return {
        ...page(["First page"], { total: 40, totalPages: 2 }),
        monitors: [
          { ...monitor("page-1", "First page"), notificationChannels: ["max"] },
        ],
      };
    }
    if (path === "/entitlement") return entitlement();
    throw new Error(`unexpected API call ${path}`);
  };
  const { MonitorsPage } = loadPage(api);
  await harness.render(React.createElement(MonitorsPage));
  assert.match(harness.host.textContent, /First page/);
  assert.ok(
    harness.host.querySelector('[aria-label="MAX: уведомления включены"]'),
  );
  assert.ok(
    harness.host.querySelector('[aria-label="Уведомления выключены"]') === null,
  );

  await click(harness, "Вперёд");
  assert.ok(calls.some(({ path }) => path.includes("page=2")));
  assert.match(harness.host.textContent, /Second page/);

  await click(harness, "По имени");
  assert.ok(
    calls.some(
      ({ path }) => path.includes("sort=name_asc") && path.includes("page=1"),
    ),
  );
  await fillSearch(harness, "needle");
  const searchRequest = calls.findLast(
    ({ path }) => path.startsWith("/monitors?") && path.includes("q=needle"),
  );
  assert.ok(searchRequest, "search text should be sent to the server");
  assert.match(
    harness.host.textContent,
    /Server match despite nonmatching label/,
  );
  assert.ok(
    harness.host.querySelector('[aria-label="Telegram: уведомления включены"]'),
  );
  assert.ok(
    harness.host.querySelector('[aria-label="Email: уведомления включены"]'),
  );
  assert.ok(
    harness.host.querySelector('[aria-label="Webhook: уведомления включены"]'),
  );
});

test("a superseded monitor-list response cannot replace the newer selection", async (t) => {
  const harness = createHarness();
  t.after(() => harness.unmount());
  let resolveOld;
  const calls = [];
  const api = (path, options = {}) => {
    calls.push({ path, options });
    if (path.startsWith("/monitors?")) {
      if (path.includes("sort=created_desc"))
        return new Promise((resolve) => (resolveOld = resolve));
      return Promise.resolve(page(["Fresh selection"]));
    }
    if (path === "/entitlement") return Promise.resolve(entitlement());
    throw new Error(`unexpected API call ${path}`);
  };
  const { MonitorsPage } = loadPage(api);
  await harness.render(React.createElement(MonitorsPage));
  assert.equal(typeof resolveOld, "function");
  await click(harness, "По имени");
  assert.match(harness.host.textContent, /Fresh selection/);
  const oldRequest = calls.find(({ path }) =>
    path.includes("sort=created_desc"),
  );
  assert.equal(oldRequest.options.signal.aborted, true);
  await act(async () => resolveOld(page(["Stale response"])));
  await flush();
  assert.match(harness.host.textContent, /Fresh selection/);
  assert.doesNotMatch(harness.host.textContent, /Stale response/);
});

test("a deep-linked monitor outside the loaded page is fetched directly by ID", async (t) => {
  const harness = createHarness();
  t.after(() => harness.unmount());
  const calls = [];
  const target = monitor(
    "20000000-0000-4000-8000-000000000099",
    "Monitor outside this page",
  );
  const incidentID = "30000000-0000-4000-8000-000000000099";
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path.startsWith("/monitors?")) return page(["Current page monitor"]);
    if (path === "/entitlement") return entitlement();
    if (path === `/monitors/${target.id}`) return { monitor: target };
    if (path === `/monitors/${target.id}/results?limit=50`)
      return { results: [] };
    if (path === `/monitors/${target.id}/incidents`) return { incidents: [] };
    if (path === `/monitors/${target.id}/incidents/${incidentID}`)
      return {
        incident: {
          id: incidentID,
          number: 99,
          openedAt: "2026-01-01",
          confirmedAt: "2026-01-01",
        },
      };
    throw new Error(`unexpected API call ${path}`);
  };
  const { MonitorsPage } = loadPage(
    api,
    `monitor=${target.id}&incident=${incidentID}`,
  );
  await harness.render(React.createElement(MonitorsPage));
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 10));
    await Promise.resolve();
    await Promise.resolve();
  });
  assert.ok(calls.some(({ path }) => path === `/monitors/${target.id}`));
  assert.ok(
    calls.some(
      ({ path }) => path === `/monitors/${target.id}/results?limit=50`,
    ),
  );
  assert.ok(
    [...harness.host.querySelectorAll('[role="dialog"]')].some(
      (dialog) => dialog.getAttribute("aria-label") === target.name,
    ),
  );
});

test("removing a deep link cancels its pending detail read and restores list actions", async (t) => {
  const harness = createHarness();
  t.after(() => harness.unmount());
  const monitorID = "20000000-0000-4000-8000-000000000098";
  const incidentID = "30000000-0000-4000-8000-000000000098";
  const current = monitor("current-monitor", "Current page monitor");
  let resolveTarget;
  let detailSignal;
  const api = (path, options = {}) => {
    if (path.startsWith("/monitors?"))
      return Promise.resolve(page([current.name]));
    if (path === "/entitlement") return Promise.resolve(entitlement());
    if (path === `/monitors/${monitorID}`) {
      detailSignal = options.signal;
      return new Promise((resolve) => (resolveTarget = resolve));
    }
    if (path === `/monitors/${monitorID}/results?limit=50`)
      return Promise.resolve({ results: [] });
    if (path === `/monitors/${monitorID}/incidents`)
      return Promise.resolve({ incidents: [] });
    if (path === `/monitors/${monitorID}/incidents/${incidentID}`)
      return Promise.resolve({
        incident: {
          id: incidentID,
          number: 98,
          openedAt: "2026-01-01",
          confirmedAt: "2026-01-01",
        },
      });
    throw new Error(`unexpected API call ${path}`);
  };
  const loaded = loadPage(api, `monitor=${monitorID}&incident=${incidentID}`);
  await harness.render(React.createElement(loaded.MonitorsPage));
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 10));
    await Promise.resolve();
  });
  const settings = () =>
    [...harness.host.querySelectorAll("button")].find(
      (button) =>
        button.getAttribute("aria-label") === `Настройки ${current.name}`,
    );
  assert.equal(typeof resolveTarget, "function");
  assert.equal(
    settings().disabled,
    true,
    "pending deep link owns the busy state",
  );

  loaded.setQuery("");
  await harness.render(React.createElement(loaded.MonitorsPage));
  assert.equal(detailSignal.aborted, true);
  assert.equal(
    settings().disabled,
    false,
    "removing the link restores list controls",
  );
  await act(async () =>
    resolveTarget({ monitor: monitor(monitorID, "Old linked monitor") }),
  );
  await flush();
  assert.equal(settings().disabled, false);
  assert.equal(harness.host.querySelector('[role="dialog"]'), null);
  assert.doesNotMatch(harness.host.textContent, /Old linked monitor/);
});

test("a committed monitor mutation refreshes the latest server-side selection", async (t) => {
  const harness = createHarness();
  t.after(() => harness.unmount());
  let finishPatch;
  const listCalls = [];
  const api = (path, options = {}) => {
    if (path.startsWith("/monitors?")) {
      listCalls.push(path);
      const params = new URL(path, "http://localhost").searchParams;
      const name =
        params.get("sort") === "name_asc"
          ? "Sorted after mutation"
          : "Before mutation";
      const result = page([name]);
      result.monitors[0].id = "target-monitor";
      return Promise.resolve(result);
    }
    if (path === "/entitlement") return Promise.resolve(entitlement());
    if (path === "/monitors/target-monitor" && options.method === "PATCH")
      return new Promise((resolve) => (finishPatch = resolve));
    throw new Error(`unexpected API call ${path}`);
  };
  const { MonitorsPage } = loadPage(api);
  await harness.render(React.createElement(MonitorsPage));
  await click(harness, "По имени");
  assert.match(harness.host.textContent, /Sorted after mutation/);
  const pause = [...harness.host.querySelectorAll("button")].find(
    (button) =>
      button.getAttribute("aria-label") ===
      "Приостановить Sorted after mutation",
  );
  assert.ok(pause);
  await act(async () => pause.click());
  assert.equal(typeof finishPatch, "function");

  await click(harness, "По дате");
  const latestSelection = listCalls.at(-1);
  assert.ok(latestSelection.includes("sort=created_desc"));
  await act(async () => finishPatch({}));
  await flush();

  assert.ok(listCalls.at(-1).includes("sort=created_desc"));
  assert.match(harness.host.textContent, /Before mutation/);
});
