import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";
import ts from "typescript";

const source = fs.readFileSync(
  new URL("../src/shared/android/draft.ts", import.meta.url),
  "utf8",
);
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2022,
  },
}).outputText;

function createHarness({ now = 1_800_000_000_000 } = {}) {
  const values = new Map();
  const events = [];
  const location = { hash: "", pathname: "/app", search: "?tab=monitors" };
  const window = {
    location,
    history: {
      state: { preserved: true },
      replaceState(state, _title, url) {
        assert.deepEqual(state, { preserved: true });
        this.lastUrl = url;
      },
    },
    dispatchEvent(event) {
      events.push(event.type);
    },
  };
  const sessionStorage = {
    getItem(key) {
      return values.has(key) ? values.get(key) : null;
    },
    setItem(key, value) {
      values.set(key, String(value));
    },
    removeItem(key) {
      values.delete(key);
    },
  };
  class FixedDate extends Date {
    static now() {
      return now;
    }
  }
  const fixtureModule = { exports: {} };
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: fixtureModule.exports,
    window,
    sessionStorage,
    atob,
    TextDecoder,
    TextEncoder,
    URL,
    Event,
    Date: FixedDate,
  });
  return {
    api: fixtureModule.exports,
    values,
    events,
    location,
    window,
    setNow(value) {
      now = value;
    },
  };
}

function encodedDraft(
  id = "123e4567-e89b-12d3-a456-426614174000",
  url = "https://monitor.example/health",
) {
  const bytes = Buffer.from(
    JSON.stringify({ v: 1, id, url, name: "Проверка 🚦" }),
  );
  return bytes.toString("base64url");
}

test("parser accepts a draft and rejects oversized, malformed, unsafe, and noncanonical fields", () => {
  const { api } = createHarness();
  const valid = encodedDraft();
  assert.equal(api.parseMonitorDraft(valid).name, "Проверка 🚦");
  assert.equal(api.parseMonitorDraft(valid + "="), null);
  assert.equal(api.parseMonitorDraft("!".repeat(40)), null);
  assert.equal(
    api.parseMonitorDraft(Buffer.from("{bad json}").toString("base64url")),
    null,
  );
  assert.equal(
    api.parseMonitorDraft(Buffer.from([0xff]).toString("base64url")),
    null,
  );
  for (const url of [
    "file:///etc/passwd",
    "https://user:pass@monitor.example/",
    "https://monitor.example/path#fragment",
    "https://monitor.example/%00",
    " https://monitor.example/",
  ]) {
    assert.equal(
      api.parseMonitorDraft(encodedDraft(undefined, url)),
      null,
      url,
    );
  }
  assert.equal(api.parseMonitorDraft(encodedDraft("bad-id")), null);
  assert.equal(
    api.parseMonitorDraft(
      Buffer.from(
        JSON.stringify({
          v: 1,
          id: "123e4567-e89b-12d3-a456-426614174000",
          url: "https://x.test",
          extra: true,
        }),
      ).toString("base64url"),
    ),
    null,
  );
  assert.equal(
    api.parseMonitorDraft(
      Buffer.from(
        JSON.stringify({
          v: 1,
          id: "123e4567-e89b-12d3-a456-426614174000",
          url: "https://x.test",
          name: "x".repeat(101),
        }),
      ).toString("base64url"),
    ),
    null,
  );
});

test("capture removes fragment before validation, saves once, and refuses a second pending draft", () => {
  const harness = createHarness();
  const { api, location, window, values, events } = harness;
  location.hash = `#monitor-draft=${encodedDraft()}`;
  assert.equal(api.captureMonitorDraft("acct-A"), null);
  assert.equal(window.history.lastUrl, "/app?tab=monitors");
  assert.equal(
    api.readMonitorDraft("acct-A").url,
    "https://monitor.example/health",
  );
  assert.deepEqual(events, [api.draftChanged]);

  location.hash = `#monitor-draft=${encodedDraft("123e4567-e89b-12d3-a456-426614174001")}`;
  assert.match(api.captureMonitorDraft("acct-A"), /предыдущий черновик/);
  assert.equal(
    JSON.parse(values.get("heartbeat.monitor-draft.v1")).draft.id,
    "123e4567-e89b-12d3-a456-426614174000",
  );
  assert.deepEqual(events, [api.draftChanged]);
});

test("capture deduplicates already consumed IDs while allowing distinct subsequent drafts", () => {
  const { api, location, events } = createHarness();
  location.hash = `#monitor-draft=${encodedDraft()}`;
  assert.equal(api.captureMonitorDraft("acct-A"), null);
  api.clearMonitorDraft();

  location.hash = `#monitor-draft=${encodedDraft()}`;
  assert.equal(api.captureMonitorDraft("acct-A"), null);
  assert.equal(api.readMonitorDraft("acct-A"), null);

  location.hash = `#monitor-draft=${encodedDraft("123e4567-e89b-12d3-a456-426614174001")}`;
  assert.equal(api.captureMonitorDraft("acct-A"), null);
  assert.equal(
    api.readMonitorDraft("acct-A").id,
    "123e4567-e89b-12d3-a456-426614174001",
  );
  assert.equal(events.length, 2);
});

test("stored draft is account-bound and expires at the TTL boundary", () => {
  const harness = createHarness();
  const { api, location, setNow, values } = harness;
  location.hash = `#monitor-draft=${encodedDraft()}`;
  assert.equal(api.captureMonitorDraft(), null);
  assert.equal(
    api.readMonitorDraft("acct-A").id,
    "123e4567-e89b-12d3-a456-426614174000",
  );
  assert.equal(
    JSON.parse(values.get("heartbeat.monitor-draft.v1")).accountId,
    "acct-A",
  );
  assert.equal(api.readMonitorDraft("acct-B"), null);

  location.hash = `#monitor-draft=${encodedDraft("123e4567-e89b-12d3-a456-426614174001")}`;
  assert.equal(api.captureMonitorDraft("acct-A"), null);
  setNow(1_800_000_000_000 + 30 * 60 * 1000 + 1);
  assert.equal(api.readMonitorDraft("acct-A"), null);
});
