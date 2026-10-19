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
