import { createHash } from "node:crypto";
import { androidRelease } from "@/shared/android";

export const runtime = "nodejs";
const APK_TYPE = "application/vnd.android.package-archive";
const MAX_ACTIVE = 4;
let active = 0;

function failure(status: number) {
  return new Response("Не удалось скачать APK. Повторите попытку позже.", {
    status,
    headers: {
      "Cache-Control": "no-store",
      ...(status === 503 ? { "Retry-After": "5" } : {}),
    },
  });
}
async function abortable<T>(operation: Promise<T>, signal: AbortSignal) {
  let onAbort: () => void = () => {};
  const aborted = new Promise<never>((_, reject) => {
    onAbort = () => reject(new Error("APK request aborted"));
    signal.addEventListener("abort", onAbort, { once: true });
    if (signal.aborted) onAbort();
  });
  try {
    return await Promise.race([operation, aborted]);
  } finally {
    signal.removeEventListener("abort", onAbort);
  }
}
async function download(request: Request, head: boolean) {
  const release = androidRelease();
  if (!release)
    return new Response("Подписанный APK пока не опубликован.", {
      status: 404,
      headers: { "Cache-Control": "no-store" },
    });
  if (active >= MAX_ACTIVE) return failure(503);
  active++;
  const upstream = new AbortController();
  let finished = false;
  let timedOut = false;
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let output: ReadableStreamDefaultController<Uint8Array> | undefined;
  const cleanup = () => {
    if (finished) return;
    finished = true;
    active--;
    clearTimeout(headerTimer);
    clearTimeout(totalTimer);
    request.signal.removeEventListener("abort", cancel);
  };
  const stop = () => {
    if (finished) return;
    cleanup();
    upstream.abort();
    void reader?.cancel().catch(() => {});
    output?.error(new Error("APK download interrupted"));
  };
  const cancel = () => stop();
  const headerTimer = setTimeout(() => {
    timedOut = true;
    stop();
  }, 5_000);
  const totalTimer = setTimeout(() => {
    timedOut = true;
    stop();
  }, 5 * 60_000);
  request.signal.addEventListener("abort", cancel, { once: true });
  if (request.signal.aborted) cancel();
  try {
    // Only the validated immutable manifest URL is fetched. No browser headers,
    // URL parameters, credentials or Range are forwarded to the object store.
    const response = await abortable(
      fetch(release.downloadUrl, {
        method: head ? "HEAD" : "GET",
        cache: "no-store",
        redirect: "manual",
        headers: { "Accept-Encoding": "identity" },
        signal: upstream.signal,
      }),
      upstream.signal,
    );
    clearTimeout(headerTimer);
    // The published S3 object labels its verified raw APK as aws-chunked.
    // Accept only that exact metadata value; never decode or forward it.
    const encoding = response.headers.get("content-encoding");
    const length = response.headers.get("content-length");
    if (
      finished ||
      response.status !== 200 ||
      response.headers.get("content-type")?.toLowerCase() !== APK_TYPE ||
      (encoding !== null &&
        !["identity", "aws-chunked"].includes(encoding.toLowerCase())) ||
      !length ||
      !/^\d+$/.test(length) ||
      Number(length) !== release.sizeBytes ||
      (!head && !response.body)
    ) {
      void response.body?.cancel().catch(() => {});
      stop();
      return failure(timedOut ? 504 : 502);
    }
    const headers = {
      "Content-Type": APK_TYPE,
      "Content-Length": String(release.sizeBytes),
      "Content-Disposition": `attachment; filename="Heartbeat-Checker-${release.versionName}.apk"`,
      "Cache-Control": "no-store, no-transform",
      "X-Content-Type-Options": "nosniff",
      "X-Robots-Tag": "noindex",
      "Accept-Ranges": "none",
    };
    if (head) {
      void response.body?.cancel().catch(() => {});
      cleanup();
      return new Response(null, { headers });
    }
    reader = response.body!.getReader();
    const hash = createHash("sha256");
    let bytes = 0;
    let lastByte: Uint8Array | undefined;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        output = controller;
      },
      async pull(controller) {
        if (finished) return;
        try {
          // One byte is withheld until EOF and hash validation. A corrupt or
          // truncated object can never complete the advertised Content-Length.
          let part;
          do {
            part = await abortable(reader!.read(), upstream.signal);
          } while (!part.done && part.value.byteLength === 0);
          if (finished) return;
          if (part.done) {
            if (
              bytes !== release.sizeBytes ||
              hash.digest("hex") !== release.sha256
            )
              throw new Error("APK integrity mismatch");
            if (lastByte) controller.enqueue(lastByte);
            controller.close();
            cleanup();
            return;
          }
          bytes += part.value.byteLength;
          if (bytes > release.sizeBytes) throw new Error("APK size mismatch");
          hash.update(part.value);
          if (lastByte) controller.enqueue(lastByte);
          if (part.value.byteLength > 1)
            controller.enqueue(
              part.value.subarray(0, part.value.byteLength - 1),
            );
          lastByte = part.value.slice(-1);
        } catch {
          stop();
        }
      },
      cancel() {
        output = undefined;
        stop();
      },
    });
    return new Response(stream, { headers });
  } catch {
    stop();
    return failure(timedOut || request.signal.aborted ? 504 : 502);
  }
}
export function GET(request: Request) {
  return download(request, false);
}
export function HEAD(request: Request) {
  return download(request, true);
}
