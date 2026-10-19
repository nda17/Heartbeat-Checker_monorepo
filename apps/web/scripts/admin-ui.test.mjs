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
const qrcode = requireWeb("qrcode.react");

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
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    DOMException,
    window: globalThis.window,
    document: globalThis.document,
    FormData: globalThis.FormData,
    ...runtime,
  });
  return fixtureModule.exports;
}

function uiMocks(React) {
  return {
    Loading: () => React.createElement("span", { role: "status" }, "Загрузка"),
    Notice: ({ children, success }) =>
      React.createElement(
        "div",
        { role: "alert", "data-success": success ? "true" : undefined },
        children,
      ),
    Spinner: () => React.createElement("span", null, "…"),
    PasswordInput: (props) =>
      React.createElement("input", { ...props, type: "password" }),
    dateTime: (value) => String(value ?? ""),
  };
}

function createHarness() {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "http://localhost/",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: dom.window.navigator,
  });
  globalThis.HTMLElement = dom.window.HTMLElement;
  dom.window.HTMLElement.prototype.attachEvent ??= function attachEvent() {};
  dom.window.HTMLElement.prototype.detachEvent ??= function detachEvent() {};
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.FormData = dom.window.FormData;
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
      delete globalThis.MouseEvent;
      delete globalThis.FormData;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    },
  };
}

function apiStub(handlers, calls) {
  return async (path, options = {}) => {
    calls.push({ path, ...options });
    const handler = handlers[path] ?? handlers[path.split("?")[0]];
    if (!handler) throw new Error(`Unexpected API call: ${path}`);
    return typeof handler === "function" ? handler(options) : handler;
  };
}

function fill(form, name, value) {
  const field = form.elements.namedItem(name);
  assert.ok(field, `missing form field ${name}`);
  const prototype =
    field instanceof window.HTMLTextAreaElement
      ? window.HTMLTextAreaElement.prototype
      : window.HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value").set.call(field, value);
  field.dispatchEvent(new window.Event("input", { bubbles: true }));
  field.dispatchEvent(new window.Event("change", { bubbles: true }));
}

async function submit(form, twice = false) {
  await act(async () => {
    form.dispatchEvent(
      new window.Event("submit", { bubbles: true, cancelable: true }),
    );
    if (twice)
      form.dispatchEvent(
        new window.Event("submit", { bubbles: true, cancelable: true }),
      );
  });
}

function sharedAliases(api, extra = {}) {
  return {
    "@/shared/api": {
      api,
      ApiError: class ApiError extends Error {
        constructor(message, status, code) {
          super(message);
          this.status = status;
          this.code = code;
        }
      },
      errorMessage: (error) => error?.message ?? "Не удалось выполнить запрос.",
    },
    "@/shared/ui": uiMocks(React),
    ...extra,
  };
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

function adminPageHarness({
  account,
  monitors = [],
  apiHandler,
  replacements = [],
}) {
  const harness = createHarness();
  const calls = [];
  const mfa = {
    enabled: true,
    verifiedUntil: "2099-01-15T00:00:00Z",
    stepUpUntil: "2099-01-16T00:00:00Z",
    recoveryCodesRemaining: 6,
  };
  const api = async (path, options = {}) => {
    calls.push({ path, ...options });
    if (path === "/auth/mfa") return mfa;
    if (apiHandler) {
      const handled = await apiHandler(path, options, calls);
      if (handled !== undefined) return handled;
    }
    if (path.startsWith("/admin/accounts?"))
      return { items: [account], nextCursor: null };
    if (path === `/admin/accounts/${account.id}`)
      return { account, entitlement: null, sessionsCount: 1 };
    if (path === `/admin/accounts/${account.id}/monitors`)
      return { monitors, nextCursor: null };
    if (path.startsWith("/admin/audit?"))
      return { items: [], nextCursor: null };
    throw new Error(`Unexpected API call: ${path}`);
  };
  const session = {
    profile: {
      account: {
        id: "admin-actor",
        name: "Admin",
        role: "admin",
        status: "active",
        createdAt: "2026-01-01T00:00:00Z",
      },
      email: "admin@example.test",
      hasPassword: true,
    },
    refresh: async () => {},
    beginSessionTransition() {},
    endSessionTransition() {},
  };
  const router = { replace: (path) => replacements.push(path) };
  const shared = sharedAliases(api);
  const sessionModule = { useSession: () => session };
  const { AdminMfa } = loadComponent("../src/features/admin-mfa/index.tsx", {
    ...shared,
    "qrcode.react": qrcode,
  });
  const { MonitorForm } = loadComponent(
    "../src/features/monitor-form/index.tsx",
    {
      ...shared,
      "@/entities/session": sessionModule,
      "@/entities/monitor": { Status: () => null },
      "@/shared/dialog": {
        Dialog: ({ title, children }) =>
          React.createElement(
            "section",
            { role: "dialog", "aria-label": title },
            children,
          ),
      },
    },
  );
  const { AdminPage } = loadComponent("../src/_pages/admin/index.tsx", {
    ...shared,
    "next/navigation": { useRouter: () => router },
    "@/entities/session": sessionModule,
    "@/entities/monitor": { Status: () => null },
    "@/features/admin-mfa": { AdminMfa },
    "@/features/monitor-form": { MonitorForm },
  });
  return {
    calls,
    harness,
    AdminPage,
    replacements,
    router,
    async render() {
      try {
        await harness.render(React.createElement(AdminPage));
      } catch (error) {
        if (error instanceof AggregateError)
          throw new Error(
            error.errors.map((cause) => cause.stack ?? cause).join("\n"),
          );
        throw error;
      }
    },
  };
}

async function selectAdminAccount(harness, accountName) {
  const accountButton = [
    ...harness.host.querySelectorAll(".admin-account-list button"),
  ].find((button) => button.textContent.includes(accountName));
  assert.ok(
    accountButton,
    `account ${accountName} should be available; DOM: ${harness.host.innerHTML}`,
  );
  await act(async () => accountButton.click());
}

test("MFA enrollment renders a local SVG QR from the server otpauth URI", async (t) => {
  const calls = [];
  const expiresAt = new Date(Date.now() + 10 * 60 * 1000).toISOString();
  const otpauthUri =
    "otpauth://totp/Heartbeat%20Checker:admin%40example.test?secret=TESTSECRET&issuer=Heartbeat%20Checker";
  const qrProps = [];
  const api = apiStub(
    {
      "/auth/mfa": {
        enabled: false,
        verifiedUntil: null,
        stepUpUntil: null,
        recoveryCodesRemaining: 0,
      },
      "/auth/mfa/enrollment": {
        enrollmentId: "enrollment-1",
        secret: "TESTSECRET",
        otpauthUri,
        expiresAt,
      },
    },
    calls,
  );
  const harness = createHarness();
  t.after(() => harness.unmount());
  const { AdminMfa } = loadComponent("../src/features/admin-mfa/index.tsx", {
    ...sharedAliases(api),
    "qrcode.react": {
      QRCodeSVG: (props) => {
        qrProps.push(props);
        return React.createElement(qrcode.QRCodeSVG, props);
      },
    },
  });

  await harness.render(
    React.createElement(AdminMfa, {
      onState() {},
      onSessionVerified: async () => {},
      onBusyChange() {},
    }),
  );
  const form = harness.host.querySelector("form");
  fill(form, "password", "correct horse battery staple");
  await submit(form);

  assert.equal(
    calls.filter((call) => call.path === "/auth/mfa/enrollment").length,
    1,
  );
  assert.equal(
    qrProps.length,
    1,
    "enrollment should render the local QR component",
  );
  assert.equal(
    qrProps[0].value,
    otpauthUri,
    "QR must encode the server-issued otpauth URI unchanged",
  );
  assert.ok(
    harness.host.querySelector(".admin-enrollment svg"),
    "QR should be an inline SVG, not a remote image",
  );
  assert.equal(harness.host.querySelector(".admin-enrollment img"), null);
  assert.equal(
    harness.host.querySelector(".admin-enrollment a")?.getAttribute("href"),
    otpauthUri,
  );
});

test("MFA rotation ignores repeated confirmation and adopts the server's new access window", async (t) => {
  const calls = [];
  const enrollmentExpiresAt = new Date(
    Date.now() + 10 * 60 * 1000,
  ).toISOString();
  const fresh = {
    enabled: true,
    verifiedUntil: "2099-02-01T00:00:00Z",
    stepUpUntil: "2099-01-20T00:00:00Z",
    recoveryCodesRemaining: 8,
  };
  let statusReads = 0;
  const api = apiStub(
    {
      "/auth/mfa": () =>
        ++statusReads === 1
          ? {
              enabled: true,
              verifiedUntil: "2099-01-15T00:00:00Z",
              stepUpUntil: "2099-01-16T00:00:00Z",
              recoveryCodesRemaining: 3,
            }
          : fresh,
      "/auth/mfa/enrollment": {
        enrollmentId: "rotation-1",
        secret: "ROTATEDSECRET",
        otpauthUri:
          "otpauth://totp/Heartbeat%20Checker:admin%40example.test?secret=ROTATEDSECRET",
        expiresAt: enrollmentExpiresAt,
      },
      "/auth/mfa/enrollment/confirm": {
        recoveryCodes: ["new-code-1", "new-code-2"],
        verifiedUntil: fresh.verifiedUntil,
        stepUpUntil: fresh.stepUpUntil,
      },
    },
    calls,
  );
  const states = [];
  let sessionRefreshes = 0;
  const busyChanges = [];
  const harness = createHarness();
  t.after(() => harness.unmount());
  const { AdminMfa } = loadComponent(
    "../src/features/admin-mfa/index.tsx",
    sharedAliases(api),
  );

  await harness.render(
    React.createElement(AdminMfa, {
      onState: (state) => states.push(state),
      onSessionVerified: async () => {
        sessionRefreshes += 1;
      },
      onBusyChange: (busy) => busyChanges.push(busy),
    }),
  );
  const settings = [...harness.host.querySelectorAll("summary")].find((item) =>
    item.textContent.includes("Настройки защиты"),
  );
  assert.ok(settings, "enabled MFA should show its settings disclosure");
  await act(async () => settings.click());
  const enrollmentForm = harness.host.querySelector(
    ".admin-security-forms form",
  );
  fill(enrollmentForm, "password", "current-password");
  await submit(enrollmentForm, true);
  assert.equal(
    calls.filter((call) => call.path === "/auth/mfa/enrollment").length,
    1,
  );

  const confirmationForm = [...harness.host.querySelectorAll("form")].find(
    (form) => form.elements.namedItem("code"),
  );
  assert.ok(
    confirmationForm,
    "replacement enrollment should ask for a new authenticator code",
  );
  fill(confirmationForm, "code", "123456");
  await submit(confirmationForm, true);

  assert.equal(
    calls.filter((call) => call.path === "/auth/mfa/enrollment/confirm").length,
    1,
  );
  assert.equal(
    sessionRefreshes,
    1,
    "successful factor rotation should refresh the current session once",
  );
  assert.deepEqual(
    states.at(-1),
    fresh,
    "the refreshed MFA state should replace the old access window",
  );
  assert.deepEqual(busyChanges, [true, false, true, false]);
  assert.ok(
    harness.host.textContent.includes("new-code-1"),
    "new recovery codes should remain available for one-time saving",
  );
});

test("a focus refresh that returns 401 during MFA rotation cannot sign the session out", async (t) => {
  const pendingProfile = deferred();
  const profile = {
    account: {
      id: "admin-1",
      name: "Admin",
      role: "admin",
      status: "active",
      createdAt: "2026-01-01T00:00:00Z",
    },
    email: "admin@example.test",
    hasPassword: true,
  };
  const calls = [];
  const replacements = [];
  const router = { replace: (path) => replacements.push(path) };
  class TestApiError extends Error {
    constructor(message, status, code) {
      super(message);
      this.status = status;
      this.code = code;
    }
  }
  const api = async (path) => {
    calls.push(path);
    if (path !== "/me") throw new Error(`Unexpected API call: ${path}`);
    if (calls.length === 1) return profile;
    if (calls.length === 2) return pendingProfile.promise;
    return profile;
  };
  const sessionModule = loadComponent("../src/entities/session/index.tsx", {});
  const harness = createHarness();
  t.after(() => harness.unmount());
  const { ConsoleShell } = loadComponent(
    "../src/widgets/console-shell/index.tsx",
    {
      "next/link": {
        __esModule: true,
        default: ({ href, children, ...props }) =>
          React.createElement("a", { href, ...props }, children),
      },
      "next/navigation": {
        usePathname: () => "/app/admin",
        useRouter: () => router,
      },
      "@/entities/session": sessionModule,
      "@/features/theme-toggle": { ThemeToggle: () => null },
      "@/shared/api": {
        api,
        ApiError: TestApiError,
        errorMessage: (error) => error?.message ?? "API error",
        safeAppReturnTo: (path) => path,
      },
      "@/shared/android": {
        captureMonitorDraft: () => null,
        clearMonitorDraft() {},
        readMonitorDraft() {},
      },
      "@/shared/ui": { ...uiMocks(React), Brand: () => null },
    },
  );
  const SessionControls = () => {
    const session = sessionModule.useSession();
    return React.createElement(
      React.Fragment,
      null,
      React.createElement(
        "button",
        { onClick: session.beginSessionTransition },
        "Начать MFA-переход",
      ),
      React.createElement(
        "button",
        { onClick: session.endSessionTransition },
        "Завершить MFA-переход",
      ),
    );
  };

  await harness.render(
    React.createElement(
      ConsoleShell,
      null,
      React.createElement(SessionControls),
    ),
  );
  assert.ok(
    harness.host.textContent.includes("Начать MFA-переход"),
    "session shell should finish its initial profile load",
  );
  await act(async () => window.dispatchEvent(new window.Event("focus")));
  assert.equal(
    calls.filter((path) => path === "/me").length,
    2,
    "focus should start the second profile request",
  );
  const begin = [...harness.host.querySelectorAll("button")].find((button) =>
    button.textContent.includes("Начать MFA-переход"),
  );
  assert.ok(begin);
  await act(async () => begin.click());
  pendingProfile.reject(
    new TestApiError("session expired", 401, "unauthorized"),
  );
  await act(async () => {
    await pendingProfile.promise.catch(() => undefined);
  });
  assert.deepEqual(
    replacements,
    [],
    "superseded focus 401 must not navigate to login",
  );

  const buttons = [...harness.host.querySelectorAll("button")];
  const finish = buttons.find((button) =>
    button.textContent.includes("Завершить MFA-переход"),
  );
  assert.ok(finish);
  await act(async () => finish.click());
  await act(async () => window.dispatchEvent(new window.Event("focus")));
  await act(async () => Promise.resolve());
  assert.equal(
    calls.filter((path) => path === "/me").length,
    3,
    "focus should refresh the profile after the MFA transition ends",
  );
  assert.deepEqual(replacements, []);
});

test("MonitorForm sends one write and does not replay a committed write when refresh fails", async (t) => {
  const calls = [];
  const write = deferred();
  const api = apiStub(
    {
      "/admin/accounts/target-1/monitors": () => write.promise,
    },
    calls,
  );
  let refreshes = 0;
  const closes = [];
  const harness = createHarness();
  t.after(() => harness.unmount());
  const { MonitorForm } = loadComponent(
    "../src/features/monitor-form/index.tsx",
    {
      ...sharedAliases(api),
      "@/entities/session": {
        useSession: () => ({
          refresh: async () => {
            refreshes += 1;
          },
        }),
      },
      "@/entities/monitor": { Status: () => null },
      "@/shared/dialog": {
        Dialog: ({ title, children }) =>
          React.createElement(
            "section",
            { role: "dialog", "aria-label": title },
            children,
          ),
      },
    },
  );

  await harness.render(
    React.createElement(MonitorForm, {
      basePath: "/admin/accounts/target-1/monitors",
      requireReason: true,
      onClose: () => closes.push("closed"),
      onSaved: async () => {
        throw new Error("account refresh failed");
      },
    }),
  );
  const form = harness.host.querySelector("form");
  fill(form, "name", "Production site");
  fill(form, "url", "https://status.example/health");
  fill(form, "reason", "Add production health monitor");
  await submit(form, true);
  assert.equal(
    calls.filter((call) => call.method === "POST").length,
    1,
    "duplicate submit events must not issue concurrent writes",
  );

  await act(async () => write.resolve({ id: "monitor-1" }));
  await act(async () => Promise.resolve());
  assert.equal(
    refreshes,
    0,
    "a failed owner refresh should not trigger unrelated session refresh",
  );
  assert.deepEqual(
    closes,
    [],
    "the form should preserve an explicit saved-state result when refresh fails",
  );
  const committedMessage =
    harness.host.querySelector('[role="alert"]')?.textContent ?? "";
  assert.match(
    committedMessage,
    /сохран|измен|обновлен/i,
    "the UI should tell the operator the write committed even if refresh failed",
  );

  await submit(form);
  assert.equal(
    calls.filter((call) => call.method === "POST").length,
    1,
    "a committed write must not be replayable after refresh failure",
  );
});

test("MonitorsPage preserves a committed monitor when its refresh fails", async (t) => {
  const calls = [];
  let listReads = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, ...options });
    if (path === "/monitors" && !options.method) {
      listReads += 1;
      if (listReads === 2) throw new Error("list refresh failed");
      return { monitors: [] };
    }
    if (path === "/entitlement")
      return {
        entitlement: {
          quota: 1,
          monitorCount: 0,
          source: "admin_grant",
          activeUntil: "2099-01-01T00:00:00Z",
        },
      };
    if (path === "/monitors" && options.method === "POST") return {};
    throw new Error(`Unexpected API call: ${path}`);
  };
  const harness = createHarness();
  t.after(() => harness.unmount());
  const session = {
    profile: { account: { id: "account-1" } },
    refresh: async () => {},
  };
  const shared = sharedAliases(api, {
    "@/shared/api": {
      ...sharedAliases(api)["@/shared/api"],
      safeAppReturnTo: () => null,
    },
  });
  const { MonitorForm } = loadComponent(
    "../src/features/monitor-form/index.tsx",
    {
      ...shared,
      "@/entities/session": { useSession: () => session },
      "@/entities/monitor": { Status: () => null },
      "@/shared/dialog": {
        Dialog: ({ title, children }) =>
          React.createElement(
            "section",
            { role: "dialog", "aria-label": title },
            children,
          ),
      },
    },
  );
  const router = { replace() {} };
  const { MonitorsPage } = loadComponent("../src/_pages/monitors/index.tsx", {
    ...shared,
    "next/navigation": {
      useRouter: () => router,
      useSearchParams: () => new URLSearchParams(),
    },
    "next/link": {
      default: ({ href, children, ...props }) =>
        React.createElement("a", { href, ...props }, children),
    },
    "@/entities/session": { useSession: () => session },
    "@/entities/monitor": {
      Status: () => null,
      resultErrorMessage: () => "Ошибка проверки",
    },
    "@/features/monitor-form": { MonitorForm },
    "@/features/monitor-notifications": { MonitorNotifications: () => null },
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
  });

  await harness.render(React.createElement(MonitorsPage));
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  const addButton = [...harness.host.querySelectorAll("button")].find(
    (button) => button.textContent.includes("Добавить первый монитор"),
  );
  assert.ok(addButton, "the empty state should offer monitor creation");
  await act(async () => addButton.click());
  const form = harness.host.querySelector("form");
  assert.ok(form);
  fill(form, "name", "Production site");
  fill(form, "url", "https://status.example/health");
  await submit(form, true);
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

  assert.equal(calls.filter((call) => call.method === "POST").length, 1);
  assert.ok(harness.host.textContent.includes("Монитор сохранён."));
  assert.ok(
    harness.host.textContent.includes("Данные не обновились"),
    "the dialog should explain that the saved monitor list could not refresh",
  );
  assert.equal(
    harness.host.querySelector("form"),
    null,
    "the committed form should not remain available for resubmission",
  );
  assert.equal(calls.filter((call) => call.method === "POST").length, 1);
});

test("API errors hide proxy error bodies and preserve structured server codes", async () => {
  const responses = [
    {
      ok: false,
      status: 500,
      async json() {
        throw new SyntaxError("not JSON");
      },
    },
    {
      ok: false,
      status: 502,
      async json() {
        return null;
      },
    },
    {
      ok: false,
      status: 503,
      async json() {
        return { error: "request_timeout" };
      },
    },
  ];
  const { api, ApiError } = loadComponent(
    "../src/shared/api/index.ts",
    {},
    { fetch: async () => responses.shift() },
  );
  for (const expectedCode of [
    "temporarily_unavailable",
    "temporarily_unavailable",
    "request_timeout",
  ]) {
    await assert.rejects(api("/monitors"), (error) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.status >= 500, true);
      assert.equal(error.code, expectedCode);
      assert.doesNotMatch(error.message, /Internal Server Error|not JSON/);
      return true;
    });
  }
});

test("invalid entitlement dates are shown inline without issuing a grant", async (t) => {
  const account = {
    id: "target-1",
    name: "Target account",
    email: "target@example.test",
    role: "user",
    status: "active",
    version: 1,
    createdAt: "2026-01-01T00:00:00Z",
  };
  const fixture = adminPageHarness({ account });
  const { calls, harness } = fixture;
  t.after(() => harness.unmount());
  await fixture.render();
  await selectAdminAccount(harness, account.name);
  const quotaTab = [
    ...harness.host.querySelectorAll(".admin-detail-nav button"),
  ].find((button) => button.textContent === "Квота");
  assert.ok(quotaTab);
  await act(async () => quotaTab.click());
  const form = [...harness.host.querySelectorAll("form")].find((item) =>
    item.elements.namedItem("validUntil"),
  );
  assert.ok(form);
  fill(form, "validUntil", "");
  fill(form, "reason", "test invalid date");
  await submit(form);

  assert.match(
    harness.host.querySelector('[role="alert"]')?.textContent ?? "",
    /корректную дату.*будущем/i,
  );
  assert.equal(
    calls.some((call) => call.method === "PUT"),
    false,
    "invalid date must not reach the entitlement API",
  );
});

test("deleting an admin monitor keeps the operator in the admin page", async (t) => {
  const account = {
    id: "target-1",
    name: "Target account",
    email: "target@example.test",
    role: "user",
    status: "active",
    version: 1,
    createdAt: "2026-01-01T00:00:00Z",
  };
  const monitors = [
    {
      id: "monitor-1",
      name: "Status check",
      url: "https://status.example/health",
      state: "UP",
      version: 1,
      method: "GET",
      expectedStatus: 200,
      intervalMinutes: 5,
      failureThreshold: 2,
      bodyContains: "",
      enabled: true,
    },
  ];
  const fixture = adminPageHarness({
    account,
    monitors,
    apiHandler: async (path, options) => {
      if (
        path === `/admin/accounts/${account.id}/monitors/monitor-1` &&
        options.method === "DELETE"
      ) {
        monitors.splice(0);
        return {};
      }
      return undefined;
    },
  });
  const { calls, harness, replacements } = fixture;
  t.after(() => harness.unmount());
  const testWindow = window;
  const originalConfirm = testWindow.confirm;
  testWindow.confirm = () => true;
  t.after(() => {
    testWindow.confirm = originalConfirm;
  });
  await fixture.render();
  await selectAdminAccount(harness, account.name);
  const monitorsTab = [
    ...harness.host.querySelectorAll(".admin-detail-nav button"),
  ].find((button) => button.textContent === "Мониторы");
  assert.ok(monitorsTab);
  await act(async () => monitorsTab.click());
  const reasonForm = harness.host.querySelector(".admin-monitor-actions form");
  assert.ok(reasonForm);
  fill(reasonForm, "reason", "Remove obsolete monitor");
  await submit(reasonForm);
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

  assert.equal(calls.filter((call) => call.method === "DELETE").length, 1);
  assert.deepEqual(
    replacements,
    [],
    "a successful monitor deletion must not navigate away from admin",
  );
  assert.ok(harness.host.textContent.includes("Монитор удалён."));
});
