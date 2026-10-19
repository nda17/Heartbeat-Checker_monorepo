import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";
import ts from "typescript";

const APK_TYPE = "application/vnd.android.package-archive";
const APK_URL =
  "https://s3.twcstorage.ru/heartbeat-data-1/heartbeat-checker/android/2.3.4/Heartbeat-Checker.apk";

function compile(relativePath, aliases, runtime = {}) {
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
      if (id === "node:crypto") return crypto;
      throw new Error(`Unexpected import: ${id}`);
    },
    Response,
    Request,
    ReadableStream,
    AbortController,
    setTimeout,
    clearTimeout,
    ...runtime,
  });
  return fixtureModule.exports;
}

function releaseFor(bytes, overrides = {}) {
  return {
    schemaVersion: 1,
    packageName: "ru.heartbeatchecker.app",
    versionCode: 2,
    versionName: "2.3.4",
    sourceSha: "a".repeat(40),
    releasedAt: "2026-10-19T00:00:00Z",
    sizeBytes: bytes.byteLength,
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
    signingCertificateSha256: "AA:".repeat(31) + "AA",
    downloadUrl: APK_URL,
    ...overrides,
  };
}

function streamRoute(release, fetchImpl) {
  return compile(
    "../app/apps/android/download/route.ts",
    {
      "@/shared/android": { androidRelease: () => release },
    },
    { fetch: fetchImpl },
  );
}

function upstream(bytes, overrides = {}) {
  return new Response(bytes, {
    status: 200,
    headers: {
      "Content-Type": APK_TYPE,
      "Content-Length": String(bytes.byteLength),
      ...overrides,
    },
  });
}

test("APK GET streams exact validated bytes with bounded identity request and safe download headers", async () => {
  const bytes = Buffer.from([0x50, 0x4b, 0x03, 0x04, 0x10, 0x20, 0x30]);
  const release = releaseFor(bytes);
  const calls = [];
  const route = streamRoute(release, async (url, options) => {
    calls.push({ url, options });
    return upstream(bytes);
  });
  const response = await route.GET(
    new Request("https://app.example/apps/android/download", {
      headers: {
        Cookie: "hc_session=secret",
        Range: "bytes=0-3",
        "X-Other": "not-forwarded",
      },
    }),
  );
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Content-Type"), APK_TYPE);
  assert.equal(
    response.headers.get("Content-Length"),
    String(bytes.byteLength),
  );
  assert.equal(
    response.headers.get("Content-Disposition"),
    'attachment; filename="Heartbeat-Checker-2.3.4.apk"',
  );
  assert.equal(response.headers.get("Cache-Control"), "no-store, no-transform");
  assert.equal(response.headers.get("X-Content-Type-Options"), "nosniff");
  assert.equal(response.headers.get("X-Robots-Tag"), "noindex");
  assert.equal(response.headers.get("Accept-Ranges"), "none");
  assert.deepEqual(Buffer.from(await response.arrayBuffer()), bytes);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, APK_URL);
  assert.equal(calls[0].options.method, "GET");
  assert.equal(calls[0].options.cache, "no-store");
  assert.equal(calls[0].options.redirect, "manual");
  assert.equal(calls[0].options.headers["Accept-Encoding"], "identity");
  assert.deepEqual(Object.keys(calls[0].options.headers), ["Accept-Encoding"]);
  assert.equal(calls[0].options.signal.aborted, false);
});

test("APK HEAD validates upstream metadata and returns headers without a body", async () => {
  const bytes = Buffer.from([1, 2, 3, 4]);
  const calls = [];
  const route = streamRoute(releaseFor(bytes), async (url, options) => {
    calls.push({ url, options });
    return new Response(null, {
      status: 200,
      headers: {
        "Content-Type": APK_TYPE,
        "Content-Length": String(bytes.byteLength),
      },
    });
  });
  const response = await route.HEAD(
    new Request("https://app.example/apps/android/download", {
      method: "HEAD",
    }),
  );
  assert.equal(response.status, 200);
  assert.equal(response.body, null);
  assert.equal(response.headers.get("Content-Length"), "4");
  assert.equal(calls[0].options.method, "HEAD");
});

test("APK accepts the verified aws-chunked S3 label for GET and HEAD without forwarding it", async (t) => {
  const bytes = Buffer.from([0x50, 0x4b, 0x03, 0x04, 0x21]);
  for (const method of ["GET", "HEAD"]) {
    await t.test(method, async () => {
      const route = streamRoute(releaseFor(bytes), async () =>
        method === "GET"
          ? upstream(bytes, { "Content-Encoding": "aws-chunked" })
          : new Response(null, {
              status: 200,
              headers: {
                "Content-Type": APK_TYPE,
                "Content-Length": String(bytes.length),
                "Content-Encoding": "aws-chunked",
              },
            }),
      );
      const response = await route[method](
        new Request("https://app.example/apps/android/download", { method }),
      );
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("Content-Encoding"), null);
      if (method === "GET")
        assert.deepEqual(Buffer.from(await response.arrayBuffer()), bytes);
      else assert.equal(response.body, null);
    });
  }
});

test("APK rejects redirects, non-APK metadata, encoded bodies, and wrong advertised lengths", async (t) => {
  const bytes = Buffer.from([1, 2, 3]);
  const cases = [
    [
      "redirect",
      new Response(null, {
        status: 302,
        headers: { Location: "https://attacker.example/file.apk" },
      }),
    ],
    ["wrong MIME", upstream(bytes, { "Content-Type": "application/zip" })],
    ["encoded", upstream(bytes, { "Content-Encoding": "gzip" })],
    [
      "aws-chunked combined with gzip",
      upstream(bytes, { "Content-Encoding": "aws-chunked, gzip" }),
    ],
    [
      "aws-chunked combined with identity",
      upstream(bytes, { "Content-Encoding": "aws-chunked, identity" }),
    ],
    ["wrong length", upstream(bytes, { "Content-Length": "99" })],
  ];
  for (const [name, response] of cases) {
    await t.test(name, async () => {
      const route = streamRoute(releaseFor(bytes), async () => response);
      const result = await route.GET(
        new Request("https://app.example/apps/android/download"),
      );
      assert.equal(result.status, 502);
      assert.equal(result.headers.get("Location"), null);
      assert.equal(result.headers.get("Cache-Control"), "no-store");
    });
  }
});

test("APK size and SHA-256 must match before the advertised final byte is sent", async (t) => {
  const bytes = Buffer.from([0x50, 0x4b, 0x03, 0x04, 0x01]);
  for (const [name, release] of [
    ["wrong digest", releaseFor(bytes, { sha256: "0".repeat(64) })],
  ]) {
    await t.test(name, async () => {
      const route = streamRoute(release, async () => upstream(bytes));
      const response = await route.GET(
        new Request("https://app.example/apps/android/download"),
      );
      assert.equal(response.status, 200);
      const reader = response.body.getReader();
      const received = [];
      await assert.rejects(async () => {
        while (true) {
          const part = await reader.read();
          if (part.done) return;
          received.push(Buffer.from(part.value));
        }
      });
      assert.equal(Buffer.concat(received).length, bytes.length - 1);
    });
  }
  await t.test("truncated object", async () => {
    const route = streamRoute(
      releaseFor(Buffer.from([1, 2, 3, 4, 5, 6])),
      async () => upstream(bytes, { "Content-Length": "6" }),
    );
    const response = await route.GET(
      new Request("https://app.example/apps/android/download"),
    );
    assert.equal(response.status, 200);
    await assert.rejects(response.arrayBuffer());
  });
  await t.test("upstream body larger than manifest", async () => {
    const route = streamRoute(releaseFor(Buffer.from([1, 2, 3])), async () =>
      upstream(Buffer.from([1, 2, 3, 4]), { "Content-Length": "3" }),
    );
    const response = await route.GET(
      new Request("https://app.example/apps/android/download"),
    );
    assert.equal(response.status, 200);
    await assert.rejects(response.arrayBuffer());
  });
});

test("APK fetch timeout or client abort returns a bounded failure and aborts upstream", async () => {
  const bytes = Buffer.from([1]);
  let upstreamSignal;
  const route = streamRoute(releaseFor(bytes), (_url, options) => {
    upstreamSignal = options.signal;
    return new Promise(() => {});
  });
  const controller = new AbortController();
  const request = new Request("https://app.example/apps/android/download", {
    signal: controller.signal,
  });
  const response = route.GET(request);
  await Promise.resolve();
  controller.abort();
  const result = await response;
  assert.equal(result.status, 504);
  assert.equal(upstreamSignal.aborted, true);
  assert.equal(result.headers.get("Cache-Control"), "no-store");
});

test("APK header timeout returns 504 and aborts the upstream request", async () => {
  const timers = new Map();
  let nextTimer = 0;
  let upstreamSignal;
  const route = compile(
    "../app/apps/android/download/route.ts",
    {
      "@/shared/android": {
        androidRelease: () => releaseFor(Buffer.from([1])),
      },
    },
    {
      fetch: (_url, options) => {
        upstreamSignal = options.signal;
        return new Promise(() => {});
      },
      setTimeout(callback, delay) {
        const id = ++nextTimer;
        timers.set(id, { callback, delay });
        return id;
      },
      clearTimeout(id) {
        timers.delete(id);
      },
    },
  );
  const responsePromise = route.GET(
    new Request("https://app.example/apps/android/download"),
  );
  await Promise.resolve();
  const headerTimer = [...timers.values()].find(({ delay }) => delay === 5_000);
  assert.ok(headerTimer, "header deadline is bounded to five seconds");
  headerTimer.callback();
  const response = await responsePromise;
  assert.equal(response.status, 504);
  assert.equal(upstreamSignal.aborted, true);
});

test("APK total stream deadline aborts the reader and rejects an incomplete response", async () => {
  const timers = new Map();
  let nextTimer = 0;
  let upstreamSignal;
  const bytes = Buffer.from([1, 2]);
  const route = compile(
    "../app/apps/android/download/route.ts",
    { "@/shared/android": { androidRelease: () => releaseFor(bytes) } },
    {
      fetch: (_url, options) => {
        upstreamSignal = options.signal;
        return Promise.resolve(
          new Response(new ReadableStream({ pull() {} }), {
            status: 200,
            headers: {
              "Content-Type": APK_TYPE,
              "Content-Length": String(bytes.length),
            },
          }),
        );
      },
      setTimeout(callback, delay) {
        const id = ++nextTimer;
        timers.set(id, { callback, delay });
        return id;
      },
      clearTimeout(id) {
        timers.delete(id);
      },
    },
  );
  const response = await route.GET(
    new Request("https://app.example/apps/android/download"),
  );
  assert.equal(response.status, 200);
  const body = response.arrayBuffer();
  const totalTimer = [...timers.values()].find(
    ({ delay }) => delay === 5 * 60_000,
  );
  assert.ok(totalTimer, "stream has a five-minute total deadline");
  totalTimer.callback();
  await assert.rejects(body);
  assert.equal(upstreamSignal.aborted, true);
});

test("APK concurrency is capped at four and cancellation releases a slot", async () => {
  const bytes = Buffer.from([1]);
  const signals = [];
  const route = streamRoute(releaseFor(bytes), (_url, options) => {
    signals.push(options.signal);
    return new Promise(() => {});
  });
  const requests = Array.from({ length: 4 }, () => new AbortController());
  const pending = requests.map((controller) =>
    route.GET(
      new Request("https://app.example/apps/android/download", {
        signal: controller.signal,
      }),
    ),
  );
  await Promise.resolve();
  const saturated = await route.GET(
    new Request("https://app.example/apps/android/download"),
  );
  assert.equal(saturated.status, 503);
  assert.equal(saturated.headers.get("Retry-After"), "5");
  requests[0].abort();
  assert.equal((await pending[0]).status, 504);
  const admittedController = new AbortController();
  const admittedPromise = route.GET(
    new Request("https://app.example/apps/android/download", {
      signal: admittedController.signal,
    }),
  );
  await Promise.resolve();
  admittedController.abort();
  assert.equal((await admittedPromise).status, 504);
  assert.equal(signals.length, 5);
  assert.equal(signals[0].aborted, true);
  requests.slice(1).forEach((controller) => controller.abort());
  await Promise.all(pending.slice(1));
});

test("legacy APK route redirects only to the same-origin stream endpoint for GET and HEAD", async () => {
  const route = compile("../app/downloads/android/route.ts", {
    "@/shared/android": { androidRelease: () => releaseFor(Buffer.from([1])) },
  });
  for (const response of [route.GET(), route.HEAD()]) {
    assert.equal(response.status, 307);
    assert.equal(response.headers.get("Location"), "/apps/android/download");
    assert.equal(response.headers.get("Cache-Control"), "no-store");
  }
});

test("APK manifest URL and digest validation reject altered release metadata", () => {
  const source = fs.readFileSync(
    new URL("../src/shared/android/index.ts", import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  function androidRelease(metadata) {
    const fixtureModule = { exports: {} };
    vm.runInNewContext(compiled, {
      module: fixtureModule,
      exports: fixtureModule.exports,
      require(id) {
        if (id === "./release.json") return { default: metadata };
        if (id === "./draft") return {};
        throw new Error(`Unexpected import: ${id}`);
      },
    });
    return fixtureModule.exports.androidRelease;
  }
  const good = releaseFor(Buffer.from([1]));
  assert.equal(androidRelease(good)().downloadUrl, APK_URL);
  assert.throws(
    () => androidRelease({ ...good, sha256: "z".repeat(64) })(),
    /Invalid Android release metadata/,
  );
  assert.throws(
    () =>
      androidRelease({
        ...good,
        downloadUrl: "https://attacker.example/file.apk",
      })(),
    /Invalid immutable APK URL/,
  );
});
