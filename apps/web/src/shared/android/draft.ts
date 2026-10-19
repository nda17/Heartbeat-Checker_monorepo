export type MonitorDraft = { v: 1; id: string; url: string; name?: string };
type StoredDraft = {
  draft: MonitorDraft;
  expiresAt: number;
  accountId?: string;
};
const key = "heartbeat.monitor-draft.v1";
const seenKey = "heartbeat.monitor-draft-seen.v1";
const ttl = 30 * 60 * 1000;
export const draftChanged = "heartbeat:monitor-draft";
const controls = /[\u0000-\u001f\u007f]/;

export function parseMonitorDraft(encoded: string): MonitorDraft | null {
  if (!/^[A-Za-z0-9_-]{1,5462}$/.test(encoded)) return null;
  try {
    const bytes = Uint8Array.from(
      atob(encoded.replace(/-/g, "+").replace(/_/g, "/")),
      (character) => character.charCodeAt(0),
    );
    if (bytes.length > 4096) return null;
    const value = JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    );
    if (
      !value ||
      typeof value !== "object" ||
      Array.isArray(value) ||
      Object.keys(value).some(
        (field) => !["v", "id", "url", "name"].includes(field),
      ) ||
      value.v !== 1 ||
      typeof value.id !== "string" ||
      !/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(
        value.id,
      ) ||
      typeof value.url !== "string" ||
      value.url.trim() !== value.url ||
      controls.test(value.url) ||
      /%[01][0-9a-f]|%7f/i.test(value.url) ||
      value.url.includes("\\") ||
      /^https?:\/\/[^/?#]*@/i.test(value.url) ||
      !/^https?:\/\//i.test(value.url) ||
      new TextEncoder().encode(value.url).length > 2048 ||
      (value.name !== undefined &&
        (typeof value.name !== "string" ||
          [...value.name].length > 100 ||
          controls.test(value.name)))
    )
      return null;
    const url = new URL(value.url);
    if (
      !["http:", "https:"].includes(url.protocol) ||
      !url.hostname ||
      url.username ||
      url.password ||
      value.url.includes("#")
    )
      return null;
    return {
      v: 1,
      id: value.id.toLowerCase(),
      url: value.url,
      ...(value.name === undefined ? {} : { name: value.name }),
    };
  } catch {
    return null;
  }
}

// The fragment never reaches the server, auth returnTo, logs, or API requests.
export function captureMonitorDraft(accountId?: string): string | null {
  if (!window.location.hash.startsWith("#monitor-draft=")) return null;
  const encoded = window.location.hash.slice("#monitor-draft=".length);
  window.history.replaceState(
    window.history.state,
    "",
    window.location.pathname + window.location.search,
  );
  const draft = parseMonitorDraft(encoded);
  if (!draft)
    return "Не удалось открыть черновик: проверьте адрес в Android-приложении.";
  try {
    const now = Date.now();
    const storedSeen: unknown = JSON.parse(
      sessionStorage.getItem(seenKey) ?? "[]",
    );
    const seen: [string, number][] = Array.isArray(storedSeen)
      ? storedSeen
          .filter(
            (item) =>
              Array.isArray(item) &&
              typeof item[0] === "string" &&
              typeof item[1] === "number" &&
              item[1] > now,
          )
          .slice(-49)
      : [];
    if (seen.some(([id]) => id === draft.id)) return null;
    // Keep an already pending draft until the user opens or dismisses it.
    if (readMonitorDraft(accountId))
      return "Сначала откройте или удалите предыдущий черновик в обзоре мониторов, затем повторите передачу из приложения.";
    sessionStorage.setItem(
      key,
      JSON.stringify({ draft, expiresAt: now + ttl, accountId }),
    );
    sessionStorage.setItem(
      seenKey,
      JSON.stringify([...seen, [draft.id, now + ttl]]),
    );
    window.dispatchEvent(new Event(draftChanged));
    return null;
  } catch {
    return "Для передачи черновика разрешите временное хранилище браузера и повторите действие.";
  }
}

export function readMonitorDraft(accountId?: string): MonitorDraft | null {
  try {
    const value: StoredDraft | null = JSON.parse(
      sessionStorage.getItem(key) ?? "null",
    );
    if (!value) return null;
    if (
      !value.draft ||
      !Number.isFinite(value.expiresAt) ||
      value.expiresAt <= Date.now() ||
      (accountId && value.accountId && value.accountId !== accountId)
    ) {
      sessionStorage.removeItem(key);
      return null;
    }
    if (accountId && !value.accountId) {
      value.accountId = accountId;
      sessionStorage.setItem(key, JSON.stringify(value));
    }
    return value.draft;
  } catch {
    return null;
  }
}

export function clearMonitorDraft() {
  try {
    sessionStorage.removeItem(key);
  } catch {
    /* Browser storage can be disabled. */
  }
}
