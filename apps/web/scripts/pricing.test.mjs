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
      if (id === "@/shared/notifications") return toastMock;
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
  toastCalls.length = 0;
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

function adminPricingComponent(
  api,
  ApiError = class extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  },
) {
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

function adminPricingProps(overrides = {}) {
  return {
    canRead: true,
    canWrite: true,
    blocked: false,
    sessionRevision: 1,
    onBusyChange() {},
    ...overrides,
  };
}

async function renderAdminPricing(harness, Component, overrides = {}) {
  await harness.render(
    React.createElement(Component, adminPricingProps(overrides)),
  );
  await harness.settle();
}

function adminPricingControls(harness) {
  return {
    form: harness.host.querySelector("form"),
    price: harness.host.querySelector('input[name="price"]'),
    reason: harness.host.querySelector('input[name="reason"]'),
    save: [...harness.host.querySelectorAll("button")].find((button) =>
      button.textContent.includes("Сохранить цену"),
    ),
    refresh: [...harness.host.querySelectorAll("button")].find((button) =>
      button.textContent.includes("Обновить текущую цену"),
    ),
  };
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

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
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
      return { pricing: pricing(2500, 3) };
    }
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api, ApiError);
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
  assert.match(harness.host.textContent, /не повторяется автоматически/i);
  assert.equal(
    toastCalls.length,
    0,
    "an uncertain 503 stays as persistent reconciliation guidance rather than success/error toast",
  );
  assert.equal(writes, 1);
  await fill(harness.host.querySelector('input[name="price"]'), "25.00");
  await fill(
    harness.host.querySelector('input[name="reason"]'),
    "ручное повторное изменение",
  );
  await submit(harness.host.querySelector("form"));
  const secondPut = calls.find(
    (call) =>
      call.path === "/admin/billing/pricing" &&
      call.options.method === "PUT" &&
      call.options.body.pricePerMonitorMinor === 2500,
  );
  assert.ok(secondPut, "only a fresh user action may issue the next PUT");
  assert.equal(
    secondPut.options.body.version,
    2,
    "unknown-result GET adopts the reconciled CAS version",
  );
  await harness.unmount();
});

test("admin pricing disables editing during its first read and treats equivalent decimal formats as a no-op", async (t) => {
  const harness = createHarness("http://localhost/app/admin");
  t.after(() => harness.unmount());
  const initialRead = deferred();
  const calls = [];
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/admin/billing/pricing" && !options.method)
      return initialRead.promise;
    if (path === "/admin/billing/pricing" && options.method === "PUT")
      return { pricing: pricing(2900, 2) };
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api);
  await harness.render(React.createElement(AdminPricing, adminPricingProps()));
  let controls = adminPricingControls(harness);
  assert.equal(
    controls.price.disabled,
    true,
    "price stays read-only before initial pricing arrives",
  );
  assert.equal(
    controls.reason.disabled,
    true,
    "reason stays read-only before initial pricing arrives",
  );
  assert.equal(
    controls.save.disabled,
    true,
    "save remains disabled while the initial GET is pending",
  );

  await act(async () => initialRead.resolve({ pricing: pricing(2900, 1) }));
  await harness.settle();
  controls = adminPricingControls(harness);
  assert.equal(controls.price.disabled, false);
  for (const equivalent of ["29", "29,00", "29.00"]) {
    await fill(controls.price, equivalent);
    controls = adminPricingControls(harness);
    assert.equal(
      controls.save.disabled,
      true,
      `${equivalent} represents the unchanged 2,900-kopek price`,
    );
    assert.match(harness.host.textContent, /Цена не изменилась/);
    await submit(controls.form);
  }
  assert.equal(
    calls.filter(
      (call) =>
        call.path === "/admin/billing/pricing" && call.options.method === "PUT",
    ).length,
    0,
    "unchanged numeric price cannot generate a PUT even if the form is submitted directly",
  );
});

test("admin pricing preserves dirty value and its original CAS version across a blocked session cycle", async (t) => {
  const harness = createHarness("http://localhost/app/admin");
  t.after(() => harness.unmount());
  const calls = [];
  let reads = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/admin/billing/pricing" && !options.method) {
      reads += 1;
      return { pricing: reads === 1 ? pricing(2900, 1) : pricing(3100, 2) };
    }
    if (path === "/admin/billing/pricing" && options.method === "PUT")
      return { pricing: pricing(2600, 3) };
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api);
  await renderAdminPricing(harness, AdminPricing);
  let controls = adminPricingControls(harness);
  await fill(controls.price, "26,00");
  await fill(controls.reason, "синтетическая причина изменения цены");

  await renderAdminPricing(harness, AdminPricing, {
    blocked: true,
    sessionRevision: 2,
  });
  controls = adminPricingControls(harness);
  assert.equal(
    controls.price.disabled,
    true,
    "step-up lock disables price editing",
  );
  assert.equal(controls.save.disabled, true, "step-up lock disables saving");
  await renderAdminPricing(harness, AdminPricing, {
    blocked: false,
    sessionRevision: 3,
  });
  controls = adminPricingControls(harness);
  assert.equal(reads, 2, "a new authorized session performs a fresh GET");
  assert.equal(
    controls.price.value,
    "26,00",
    "background read preserves the dirty value",
  );
  assert.equal(controls.reason.value, "синтетическая причина изменения цены");

  await submit(controls.form);
  const put = calls.find(
    (call) =>
      call.path === "/admin/billing/pricing" && call.options.method === "PUT",
  );
  assert.ok(put);
  assert.deepEqual(
    JSON.parse(JSON.stringify(put.options.body)),
    {
      pricePerMonitorMinor: 2600,
      version: 1,
      reason: "синтетическая причина изменения цены",
    },
    "background GET must not replace the CAS snapshot associated with the dirty value",
  );
  assert.equal(toastCalls.filter((call) => call.kind === "success").length, 1);
});

test("admin pricing serializes double submit, accepts one update, and explicit refresh adopts a new baseline", async (t) => {
  const harness = createHarness("http://localhost/app/admin");
  t.after(() => harness.unmount());
  const calls = [];
  const pendingPut = deferred();
  let reads = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/admin/billing/pricing" && !options.method) {
      reads += 1;
      return { pricing: reads === 1 ? pricing(2900, 1) : pricing(3100, 3) };
    }
    if (path === "/admin/billing/pricing" && options.method === "PUT")
      return pendingPut.promise;
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api);
  await renderAdminPricing(harness, AdminPricing);
  let controls = adminPricingControls(harness);
  await fill(controls.price, "26.00");
  await fill(controls.reason, "плановое изменение");
  await act(async () => {
    controls.save.click();
    controls.save.click();
  });
  assert.equal(
    calls.filter(
      (call) =>
        call.path === "/admin/billing/pricing" && call.options.method === "PUT",
    ).length,
    1,
    "double submit while the write is pending must share one PUT",
  );
  await act(async () => pendingPut.resolve({ pricing: pricing(2600, 2) }));
  await harness.settle();
  assert.equal(toastCalls.filter((call) => call.kind === "success").length, 1);
  controls = adminPricingControls(harness);
  assert.equal(controls.price.value, "26.00");
  assert.equal(
    controls.reason.value,
    "",
    "accepted PUT clears the prior reason",
  );

  await fill(controls.price, "27.00");
  await fill(controls.reason, "неподтверждённый черновик");
  await act(async () => controls.refresh.click());
  await harness.settle();
  controls = adminPricingControls(harness);
  assert.equal(reads, 2);
  assert.equal(
    controls.price.value,
    "31.00",
    "explicit refresh replaces the dirty value with the distinct current server price",
  );
  assert.equal(
    controls.save.disabled,
    true,
    "the refreshed price is now a no-op",
  );
  await fill(controls.price, "32");
  await fill(controls.reason, "следующее изменение");
  await submit(controls.form);
  const puts = calls.filter(
    (call) =>
      call.path === "/admin/billing/pricing" && call.options.method === "PUT",
  );
  assert.equal(puts.length, 2);
  assert.deepEqual(
    JSON.parse(JSON.stringify(puts[1].options.body)),
    {
      pricePerMonitorMinor: 3200,
      version: 3,
      reason: "следующее изменение",
    },
    "explicit refresh resets both the displayed price and CAS baseline",
  );
});

test("a known pricing conflict preserves the draft and CAS baseline until explicit refresh", async (t) => {
  const harness = createHarness("http://localhost/app/admin");
  t.after(() => harness.unmount());
  class ApiError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }
  const calls = [];
  let reads = 0;
  let writes = 0;
  const api = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/admin/billing/pricing" && !options.method) {
      reads += 1;
      return { pricing: reads === 1 ? pricing(2900, 1) : pricing(3100, 2) };
    }
    if (path === "/admin/billing/pricing" && options.method === "PUT") {
      writes += 1;
      if (writes < 3) throw new ApiError("pricing_conflict", 409);
      return { pricing: pricing(3200, 3) };
    }
    throw new Error(`Unexpected API call: ${path}`);
  };
  const AdminPricing = adminPricingComponent(api, ApiError);
  await renderAdminPricing(harness, AdminPricing);
  let controls = adminPricingControls(harness);
  await fill(controls.price, "26.00");
  await fill(controls.reason, "операторский черновик");

  await submit(controls.form);
  controls = adminPricingControls(harness);
  assert.equal(reads, 2, "a known rejection may be reconciled with a safe GET");
  assert.equal(controls.price.value, "26.00", "409 keeps the typed amount");
  assert.equal(controls.reason.value, "операторский черновик");
  assert.ok(
    toastCalls.some(
      (call) => call.kind === "error" && call.message === "pricing_conflict",
    ),
    "a known conflict is announced as an error toast while preserving the draft",
  );

  await submit(controls.form);
  const puts = () =>
    calls.filter(
      (call) =>
        call.path === "/admin/billing/pricing" && call.options.method === "PUT",
    );
  assert.equal(
    puts().length,
    2,
    "a second explicit click is allowed, without automatic replay",
  );
  assert.deepEqual(
    puts().map((put) => put.options.body.version),
    [1, 1],
    "both rejected attempts retain the original CAS baseline until refreshed",
  );
  assert.equal(controls.price.value, "26.00");

  await act(async () => controls.refresh.click());
  await harness.settle();
  controls = adminPricingControls(harness);
  assert.equal(reads, 4);
  assert.equal(
    controls.price.value,
    "31.00",
    "explicit refresh adopts the server value",
  );
  assert.equal(
    controls.save.disabled,
    true,
    "the refreshed value is a no-op against version 2",
  );
  await fill(controls.price, "32.00");
  await fill(controls.reason, "после явного обновления");
  await submit(controls.form);
  assert.equal(puts().length, 3);
  assert.deepEqual(JSON.parse(JSON.stringify(puts()[2].options.body)), {
    pricePerMonitorMinor: 3200,
    version: 2,
    reason: "после явного обновления",
  });
  assert.equal(writes, 3);
});
