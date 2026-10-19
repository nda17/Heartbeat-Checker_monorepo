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
const pricingEntity = loadComponent("../src/entities/billing/index.ts", {});

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
    URL,
    URLSearchParams,
    AbortController,
    DOMException,
    window: globalThis.window,
    document: globalThis.document,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    ...runtime,
  });
  return fixtureModule.exports;
}

function createHarness(url = "http://localhost/") {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
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
    async settle() {
      await act(async () => Promise.resolve());
      await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      delete globalThis.window;
      delete globalThis.document;
      delete globalThis.HTMLElement;
      delete globalThis.Event;
      delete globalThis.FormData;
      delete globalThis.DOMException;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
    },
  };
}

const pricing = (pricePerMonitorMinor, version) => ({
  pricePerMonitorMinor,
  currency: "RUB",
  period: "month",
  version,
  updatedAt: `2026-10-${String(version).padStart(2, "0")}T10:00:00.000Z`,
});

test("SSR public pricing forwards only the edge-overwritten single client IP", async (t) => {
  for (const clientIP of ["192.0.2.41", "2001:db8::41"]) {
    await t.test(clientIP, async () => {
      const calls = [];
      const reader = publicPricingReader(
        async () => ({
          get(name) {
            assert.equal(name, "X-Heartbeat-Client-IP");
            return clientIP;
          },
        }),
        async (url, options) => {
          calls.push({ url, options });
          return {
            ok: true,
            json: async () => ({ pricing: pricing(2400, 4) }),
          };
        },
      );
      assert.deepEqual(await reader(), pricing(2400, 4));
      assert.equal(calls.length, 1);
      assert.equal(
        calls[0].url,
        "http://go-api.internal/api/v1/billing/pricing",
      );
      assert.deepEqual(
        Object.fromEntries(Object.entries(calls[0].options.headers)),
        {
          "X-Heartbeat-Client-IP": clientIP,
        },
      );
      assert.equal(calls[0].options.cache, "no-store");
    });
  }
});

test("SSR pricing fails closed for missing, malformed, duplicate, and unspecified client IPs", async (t) => {
  for (const clientIP of [
    null,
    "",
    "192.0.2.1, 198.51.100.2",
    "192.0.2.1,192.0.2.1",
    "for=192.0.2.1",
    "[2001:db8::1]",
    "fe80::1%en0",
    " 192.0.2.1",
    "192.0.2.1 ",
    "0.0.0.0",
    "::",
  ]) {
    await t.test(String(clientIP), async () => {
      let fetches = 0;
      const reader = publicPricingReader(
        async () => ({ get: () => clientIP }),
        async () => {
          fetches += 1;
          return {
            ok: true,
            json: async () => ({ pricing: pricing(2400, 4) }),
          };
        },
      );
      assert.equal(await reader(), null);
      assert.equal(fetches, 0, "invalid identity must not reach the API");
    });
  }
});

test("SSR pricing ignores cookies and all unrelated forwarding headers", async () => {
  let fetchOptions;
  const reader = publicPricingReader(
    async () => ({
      get(name) {
        assert.equal(name, "X-Heartbeat-Client-IP");
        return "198.51.100.77";
      },
      // These values are deliberately exposed by the incoming request object.
      // The reader may consult and forward only the dedicated edge header.
      entries() {
        return [
          ["cookie", "hc_session=synthetic"],
          ["authorization", "Bearer synthetic"],
          ["x-forwarded-for", "203.0.113.8"],
          ["forwarded", "for=203.0.113.9"],
        ];
      },
    }),
    async (_url, options) => {
      fetchOptions = options;
      return { ok: true, json: async () => ({ pricing: pricing(2400, 4) }) };
    },
  );
  assert.deepEqual(await reader(), pricing(2400, 4));
  assert.deepEqual(Object.fromEntries(Object.entries(fetchOptions.headers)), {
    "X-Heartbeat-Client-IP": "198.51.100.77",
  });
  assert.equal("credentials" in fetchOptions, false);
});

test("SSR pricing returns null when Next headers or the fixed API read fails", async (t) => {
  await t.test("headers() throws outside a request context", async () => {
    let fetches = 0;
    const reader = publicPricingReader(
      async () => {
        throw new Error("request context unavailable");
      },
      async () => {
        fetches += 1;
        return { ok: true, json: async () => ({ pricing: pricing(2400, 4) }) };
      },
    );
    assert.equal(await reader(), null);
    assert.equal(fetches, 0);
  });
  await t.test("API fetch rejects", async () => {
    const reader = publicPricingReader(
      async () => ({ get: () => "192.0.2.41" }),
      async () => {
        throw new Error("API unavailable");
      },
    );
    assert.equal(await reader(), null);
  });
});

function uiMocks() {
  return {
    Arrow: () => React.createElement("span", null, "→"),
    Icon: () => null,
    Loading: ({ text }) => React.createElement("div", { role: "status" }, text),
    Notice: ({ children, success }) =>
      React.createElement(
        "div",
        { role: "alert", "data-success": success ? "true" : undefined },
        children,
      ),
    dateTime: (value) => value ?? "—",
  };
}

function publicPricingComponent(api) {
  return loadComponent("../src/features/public-pricing/index.tsx", {
    "next/link": ({ href, children, ...props }) =>
      React.createElement("a", { href, ...props }, children),
    "@/entities/billing": pricingEntity,
    "@/shared/api": { api },
    "@/shared/ui": uiMocks(),
  }).PublicPricing;
}

function adminPricingComponent(api) {
  class ApiError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }
  return loadComponent("../src/features/admin-pricing/index.tsx", {
    "@/entities/billing": pricingEntity,
    "@/shared/api": {
      api,
      ApiError,
      errorMessage: (error) => String(error?.message ?? error),
    },
    "@/shared/ui": uiMocks(),
  }).AdminPricing;
}

function publicPricingReader(headers, fetchImpl) {
  return loadComponent(
    "../src/entities/billing/server.ts",
    {
      "server-only": {},
      "next/headers": { headers },
      "./index": pricingEntity,
    },
    {
      process: { env: { GO_API_ORIGIN: "http://go-api.internal" } },
      fetch: fetchImpl,
    },
  ).readPublicPricing;
}

async function fill(input, value) {
  input.value = value;
  const propsKey = Object.keys(input).find((key) =>
    key.startsWith("__reactProps$"),
  );
  assert.ok(propsKey, "React input props are attached");
  await act(async () =>
    input[propsKey].onChange({ target: input, currentTarget: input }),
  );
}

async function submit(form) {
  await act(async () => {
    form.dispatchEvent(
      new window.Event("submit", { bubbles: true, cancelable: true }),
    );
    await Promise.resolve();
  });
}

test("public pricing refreshes after focus and pageshow and never keeps stale amounts on failure", async () => {
  const harness = createHarness();
  const calls = [];
  let read = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path !== "/billing/pricing")
      throw new Error(`Unexpected API call: ${path}`);
    read += 1;
    if (read === 1) return { pricing: pricing(2400, 2) };
    if (read === 2) return { pricing: pricing(7200, 3) };
    throw new Error("price service unavailable");
  };
  const PublicPricing = publicPricingComponent(api);
  await harness.render(
    React.createElement(PublicPricing, { initialPricing: pricing(2900, 1) }),
  );
  await harness.settle();
  assert.match(harness.host.textContent, /24\s*₽/);
  assert.doesNotMatch(harness.host.textContent, /29\s*₽|87\s*₽/);

  await act(async () => window.dispatchEvent(new window.Event("focus")));
  await harness.settle();
  assert.match(harness.host.textContent, /72\s*₽/);
  assert.match(harness.host.textContent, /216\s*₽/);

  await act(async () => window.dispatchEvent(new window.Event("pageshow")));
  await harness.settle();
  assert.match(harness.host.textContent, /Цена временно недоступна/);
  assert.doesNotMatch(harness.host.textContent, /216\s*₽|72\s*₽|29\s*₽/);
  assert.equal(
    calls.filter((call) => call.path === "/billing/pricing").length,
    3,
  );
  assert.ok(calls.every((call) => call.options.signal instanceof AbortSignal));
  await harness.unmount();
});

test("terms pricing uses the live quote for examples instead of a baked-in amount", async () => {
  const harness = createHarness("http://localhost/terms");
  const api = async (path) => {
    if (path === "/billing/pricing") return { pricing: pricing(2400, 2) };
    throw new Error(`Unexpected API call: ${path}`);
  };
  const PublicPricing = publicPricingComponent(api);
  await harness.render(
    React.createElement(PublicPricing, {
      variant: "terms",
      initialPricing: pricing(2400, 2),
    }),
  );
  await harness.settle();
  assert.match(harness.host.textContent, /24\s*₽/);
  assert.match(harness.host.textContent, /72\s*₽/);
  assert.match(harness.host.textContent, /240\s*₽/);
  assert.doesNotMatch(harness.host.textContent, /29\s*₽|87\s*₽|290\s*₽/);
  await harness.unmount();
});

test("admin pricing sends the exact integer amount and CAS version, then reconciles an uncertain write without replay", async () => {
  const harness = createHarness("http://localhost/app/admin");
  const calls = [];
  let writes = 0;
  class ApiError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/admin/billing/pricing" && !options.method)
      return {
        pricing: pricing(
          calls.filter((call) => call.path === path && !call.options.method)
            .length === 1
            ? 2900
            : 2400,
          calls.filter((call) => call.path === path && !call.options.method)
            .length === 1
            ? 1
            : 2,
        ),
      };
    if (path === "/admin/billing/pricing" && options.method === "PUT") {
      writes += 1;
      if (writes === 1) throw new ApiError("temporary_unavailable", 503);
      return { pricing: pricing(2400, 2) };
    }
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api);
  await harness.render(
    React.createElement(AdminPricing, {
      canRead: true,
      canWrite: true,
      blocked: false,
      sessionRevision: 1,
      onBusyChange() {},
    }),
  );
  await harness.settle();
  const price = harness.host.querySelector('input[name="price"]');
  const reason = harness.host.querySelector('input[name="reason"]');
  await fill(price, "24,00");
  await fill(reason, "снижение тарифа по решению оператора");
  await submit(harness.host.querySelector("form"));
  await harness.settle();
  const puts = calls.filter(
    (call) =>
      call.path === "/admin/billing/pricing" && call.options.method === "PUT",
  );
  assert.equal(
    puts.length,
    1,
    "uncertain admin writes are never replayed automatically",
  );
  assert.deepEqual(JSON.parse(JSON.stringify(puts[0].options.body)), {
    pricePerMonitorMinor: 2400,
    version: 1,
    reason: "снижение тарифа по решению оператора",
  });
  assert.equal(
    calls.filter(
      (call) => call.path === "/admin/billing/pricing" && !call.options.method,
    ).length,
    2,
    "the uncertain result is reconciled by GET",
  );
  assert.match(harness.host.textContent, /24\s*₽/);
  assert.doesNotMatch(harness.host.textContent, /29\s*₽/);
  assert.equal(writes, 1);
  await harness.unmount();
});
