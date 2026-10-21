import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";
import { createRequire } from "node:module";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";
import { JSDOM } from "jsdom";

const transitionKey = "heartbeat-session-transition";
const transitionEvent = "heartbeat-session-transition";
const downloadPath = "/api/v1/admin/android-dev/apk";
const requireWeb = createRequire(new URL("../package.json", import.meta.url));

function deferred() {
  let resolve;
  const promise = new Promise((done) => (resolve = done));
  return { promise, resolve };
}

function response(status, body = {}) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

function validMetadata(overrides = {}) {
  return {
    release: {
      packageName: "ru.heartbeatchecker.app.dev",
      versionName: "0.28.3-dev",
      sizeBytes: 12_345_678,
      sha256: "a".repeat(64),
      ...overrides,
    },
    downloadPath,
  };
}

function compileComponent(window, document, fetch) {
  const source = fs.readFileSync(
    new URL("../src/features/android-dev-download/index.tsx", import.meta.url),
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
      if (id === "react") return React;
      if (id === "react/jsx-runtime") return requireWeb("react/jsx-runtime");
      if (id === "@/entities/session")
        return {
          sessionTransitionEvent: transitionEvent,
          sessionTransitionStorageKey: transitionKey,
        };
      throw new Error(`Unexpected import: ${id}`);
    },
    window,
    document,
    fetch,
    AbortController,
    Date,
  });
  return fixtureModule.exports.AndroidDevDownload;
}

function harness(fetchImpl) {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "https://heartbeat-checker.ru/android",
  });
  Object.defineProperty(dom.window.document, "visibilityState", {
    configurable: true,
    value: "visible",
  });
  dom.window.setInterval = () => 1;
  dom.window.clearInterval = () => {};
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = dom.window.document.createElement("div");
  dom.window.document.body.append(host);
  const root = createRoot(host);
  const Component = compileComponent(
    dom.window,
    dom.window.document,
    fetchImpl,
  );
  return {
    dom,
    host,
    async mount() {
      await act(async () => root.render(React.createElement(Component)));
    },
    async settle() {
      await act(async () => Promise.resolve());
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      delete globalThis.window;
      delete globalThis.document;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    },
    get link() {
      return host.querySelector('a[href="/api/v1/admin/android-dev/apk"]');
    },
  };
}

test("dev APK control is absent during SSR and until an authorized metadata response", async () => {
  const source = fs.readFileSync(
    new URL("../src/features/android-dev-download/index.tsx", import.meta.url),
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
      if (id === "react") return React;
      if (id === "react/jsx-runtime") return requireWeb("react/jsx-runtime");
      if (id === "@/entities/session")
        return {
          sessionTransitionEvent: transitionEvent,
          sessionTransitionStorageKey: transitionKey,
        };
      throw new Error(`Unexpected import: ${id}`);
    },
    AbortController,
  });
  assert.doesNotMatch(
    renderToStaticMarkup(
      React.createElement(fixtureModule.exports.AndroidDevDownload),
    ),
    /Скачать dev APK/,
  );

  const pending = deferred();
  const h = harness(() => pending.promise);
  try {
    await h.mount();
    assert.equal(h.link, null);
    pending.resolve(response(200, validMetadata()));
    await h.settle();
    assert.ok(h.link);
    assert.equal(h.link.textContent, "Скачать dev APK");
  } finally {
    await h.unmount();
  }
});

test("401, 403, missing metadata, network failure, and malformed metadata keep the link hidden", async (t) => {
  const cases = [
    ["unauthenticated", async () => response(401)],
    ["forbidden", async () => response(403)],
    ["artifact missing", async () => response(404)],
    [
      "network error",
      async () => {
        throw new Error("offline");
      },
    ],
    [
      "wrong package",
      async () =>
        response(
          200,
          validMetadata({ packageName: "ru.heartbeatchecker.app" }),
        ),
    ],
    ["bad digest", async () => response(200, validMetadata({ sha256: "bad" }))],
  ];
  for (const [name, fetchImpl] of cases) {
    await t.test(name, async () => {
      const h = harness(fetchImpl);
      try {
        await h.mount();
        await h.settle();
        assert.equal(h.link, null);
      } finally {
        await h.unmount();
      }
    });
  }
});

test("stale successful metadata cannot reveal the link after auth failure or logout transition", async (t) => {
  for (const transition of ["401", "logout"]) {
    await t.test(transition, async () => {
      const stale = deferred();
      const calls = [];
      const h = harness((url, options) => {
        const call = { url, options };
        calls.push(call);
        return calls.length === 1
          ? stale.promise
          : Promise.resolve(response(401));
      });
      try {
        await h.mount();
        assert.equal(calls.length, 1);
        if (transition === "401") {
          h.dom.window.dispatchEvent(new h.dom.window.Event("focus"));
          await h.settle();
          assert.equal(calls.length, 2);
        } else {
          h.dom.window.localStorage.setItem(
            transitionKey,
            `begin:${Date.now()}:1`,
          );
          h.dom.window.dispatchEvent(
            new h.dom.window.CustomEvent(transitionEvent, {
              detail: h.dom.window.localStorage.getItem(transitionKey),
            }),
          );
        }
        await h.settle();
        stale.resolve(response(200, validMetadata()));
        await h.settle();
        assert.equal(h.link, null);
      } finally {
        await h.unmount();
      }
    });
  }
});

test("hides while the document is hidden and rechecks on visibility; storage transition clears cross-tab", async () => {
  const results = [
    response(200, validMetadata()),
    response(200, validMetadata()),
  ];
  const h = harness(async () => results.shift() ?? response(403));
  try {
    await h.mount();
    await h.settle();
    assert.ok(h.link);
    await act(async () => {
      Object.defineProperty(h.dom.window.document, "visibilityState", {
        configurable: true,
        value: "hidden",
      });
      h.dom.window.document.dispatchEvent(
        new h.dom.window.Event("visibilitychange"),
      );
      await Promise.resolve();
    });
    assert.equal(h.link, null);

    await act(async () => {
      Object.defineProperty(h.dom.window.document, "visibilityState", {
        configurable: true,
        value: "visible",
      });
      h.dom.window.document.dispatchEvent(
        new h.dom.window.Event("visibilitychange"),
      );
      await Promise.resolve();
    });
    assert.ok(h.link);

    await act(async () => {
      h.dom.window.dispatchEvent(
        new h.dom.window.StorageEvent("storage", {
          key: transitionKey,
          newValue: `begin:${Date.now()}:2`,
        }),
      );
      await Promise.resolve();
    });
    assert.equal(h.link, null);
  } finally {
    await h.unmount();
  }
});
