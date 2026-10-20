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

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function createHarness() {
  toastCalls.length = 0;
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "http://localhost/app/settings",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: dom.window.navigator,
  });
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.FormData = dom.window.FormData;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  return {
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
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    },
  };
}

function loadAccountDeletion({ api, session, router }) {
  const source = fs.readFileSync(
    new URL("../src/features/account-deletion/index.tsx", import.meta.url),
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
  class ApiError extends Error {
    constructor(status, code) {
      super(code);
      this.status = status;
      this.code = code;
    }
  }
  const aliases = {
    "next/link": ({ href, className, children }) =>
      React.createElement("a", { href, className }, children),
    "next/navigation": { useRouter: () => router },
    "@/entities/session": { useSession: () => session.current },
    "@/shared/api": {
      api,
      ApiError,
      errorMessage: (error) => error?.message ?? "Не удалось выполнить запрос.",
    },
    "@/shared/dialog": {
      Dialog: ({ title, children, onClose }) =>
        React.createElement(
          "section",
          { role: "dialog", "aria-label": title },
          React.createElement(
            "button",
            { type: "button", onClick: onClose },
            "Закрыть",
          ),
          children,
        ),
    },
    "@/shared/ui": {
      Icon: () => null,
      Notice: ({ children }) =>
        React.createElement("div", { role: "alert" }, children),
      PasswordInput: (props) =>
        React.createElement("input", { ...props, type: "password" }),
      Spinner: () => React.createElement("span", null, "…"),
    },
  };
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports,
    require(id) {
      if (Object.hasOwn(aliases, id)) return aliases[id];
      if (id === "@/shared/notifications") return toastMock;
      return requireWeb(id);
    },
    console,
    Date,
    TextEncoder,
    URL,
    URLSearchParams,
    FormData: globalThis.FormData,
    window: globalThis.window,
    document: globalThis.document,
  });
  return { AccountDeletion: fixtureModule.exports.AccountDeletion, ApiError };
}

function sessionState(
  id,
  email,
  transitions,
  role = "user",
  hasPassword = true,
) {
  return {
    profile: { account: { id, role }, email, hasPassword },
    beginSessionTransition() {
      transitions.value += 1;
    },
    endSessionTransition() {
      transitions.value -= 1;
    },
  };
}

async function openAndConfirm(harness) {
  await act(async () => {
    harness.host.querySelector("button:not([disabled])").click();
  });
  const form = harness.host.querySelector("[role=dialog] form");
  assert.ok(form, "confirmation dialog is open");
  const password = form.elements.namedItem("currentPassword");
  Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    "value",
  ).set.call(password, "correct horse battery staple");
  password.dispatchEvent(new window.Event("input", { bubbles: true }));
  const checkbox = form.elements.namedItem("confirm");
  checkbox.checked = true;
  checkbox.dispatchEvent(new window.Event("change", { bubbles: true }));
  return form;
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

test("account deletion latches account id and makes one DELETE even on double submit", async () => {
  const harness = createHarness();
  const request = deferred();
  const calls = [];
  const destinations = [];
  const router = { replace: (path) => destinations.push(path) };
  const transitions = { value: 0 };
  const session = {
    current: sessionState("account-a", "a@example.test", transitions),
  };
  const { AccountDeletion } = loadAccountDeletion({
    api: async (path, options) => {
      calls.push({ path, options });
      return request.promise;
    },
    session,
    router,
  });
  await harness.render(React.createElement(AccountDeletion));
  const form = await openAndConfirm(harness);
  session.current = sessionState("account-b", "b@example.test", transitions);
  await harness.render(React.createElement(AccountDeletion));
  await submit(form, true);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, "/me");
  assert.equal(calls[0].options.method, "DELETE");
  assert.equal(
    calls[0].options.body.currentPassword,
    "correct horse battery staple",
  );
  assert.equal(calls[0].options.body.expectedAccountId, "account-a");
  assert.equal(transitions.value, 1);
  request.resolve(undefined);
  await act(async () => request.promise);
  assert.deepEqual(destinations, ["/login?account=deleted"]);
  await harness.unmount();
});

test("an ambiguous deletion response leaves a terminal result and cannot retry DELETE", async () => {
  const harness = createHarness();
  const calls = [];
  const destinations = [];
  const transitions = { value: 0 };
  const session = {
    current: sessionState("account-a", "a@example.test", transitions),
  };
  const { AccountDeletion, ApiError } = loadAccountDeletion({
    api: async (path, options) => {
      calls.push({ path, options });
      throw new ApiError(0, "network_error");
    },
    session,
    router: { replace: (path) => destinations.push(path) },
  });
  await harness.render(React.createElement(AccountDeletion));
  const form = await openAndConfirm(harness);
  await submit(form);
  assert.equal(calls.length, 1);
  assert.deepEqual(destinations, ["/login?account=unknown"]);
  assert.match(harness.host.textContent, /Не повторяйте запрос/);
  assert.equal(transitions.value, 1);
  await harness.unmount();
  assert.equal(transitions.value, 0);
});

test("wrong password reports an inline error without redirecting", async () => {
  const harness = createHarness();
  const calls = [];
  const destinations = [];
  const transitions = { value: 0 };
  const session = {
    current: sessionState("account-a", "a@example.test", transitions),
  };
  const { AccountDeletion, ApiError } = loadAccountDeletion({
    api: async (path, options) => {
      calls.push({ path, options });
      throw new ApiError(401, "invalid_credentials");
    },
    session,
    router: { replace: (path) => destinations.push(path) },
  });
  await harness.render(React.createElement(AccountDeletion));
  const form = await openAndConfirm(harness);
  await submit(form);
  assert.equal(calls.length, 1);
  assert.deepEqual(destinations, []);
  assert.match(harness.host.textContent, /Неверный текущий пароль/);
  assert.equal(form.elements.namedItem("currentPassword").value, "");
  assert.equal(transitions.value, 0);
  await harness.unmount();
});
