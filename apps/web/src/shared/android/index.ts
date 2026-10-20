import release from "./release.json";

const publicUUID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function pushPairingId(params: URLSearchParams) {
  const values = params.getAll("pushPair");
  return params.size === 1 &&
    values.length === 1 &&
    values[0].length === 36 &&
    publicUUID.test(values[0])
    ? values[0].toLowerCase()
    : null;
}

// Completion secrets stay in Intent String extras inside the fragment. They
// never become an HTTP query, browser history URL or referrer.
export function androidPushIntent(
  action: "connect" | "complete",
  completion?: {
    pairingId: string;
    completionCode: string;
    clientState: string;
  },
) {
  if (action === "connect" && completion === undefined)
    return "intent://heartbeat-checker.ru/android/push/connect#Intent;scheme=https;package=ru.heartbeatchecker.app;end";
  const secret = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;
  if (
    action === "complete" &&
    completion &&
    completion.pairingId.length === 36 &&
    publicUUID.test(completion.pairingId) &&
    secret.test(completion.completionCode) &&
    secret.test(completion.clientState)
  )
    return `intent://heartbeat-checker.ru/android/push/complete#Intent;scheme=https;package=ru.heartbeatchecker.app;S.pairingId=${completion.pairingId.toLowerCase()};S.completionCode=${completion.completionCode};S.clientState=${completion.clientState};end`;
  throw new Error("Invalid Android push action");
}

export type AndroidRelease = {
  schemaVersion: 1;
  packageName: "ru.heartbeatchecker.app";
  versionName: string;
  versionCode: number;
  sourceSha: string;
  releasedAt: string;
  sizeBytes: number;
  sha256: string;
  signingCertificateSha256: string;
  downloadUrl: string;
};
export function androidRelease(): AndroidRelease | null {
  if (release === null) return null;
  const value = release as AndroidRelease;
  if (
    value.schemaVersion !== 1 ||
    value.packageName !== "ru.heartbeatchecker.app" ||
    !/^\d+\.\d+\.\d+$/.test(value.versionName) ||
    !Number.isSafeInteger(value.versionCode) ||
    value.versionCode < 1 ||
    !/^[0-9a-f]{40}$/.test(value.sourceSha) ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value.releasedAt) ||
    !Number.isSafeInteger(value.sizeBytes) ||
    value.sizeBytes < 1 ||
    value.sizeBytes >= 100 * 1024 * 1024 ||
    !/^[0-9a-f]{64}$/.test(value.sha256) ||
    !/^([0-9A-F]{2}:){31}[0-9A-F]{2}$/.test(value.signingCertificateSha256)
  )
    throw new Error("Invalid Android release metadata");
  if (
    value.downloadUrl !==
    `https://s3.twcstorage.ru/heartbeat-data-1/heartbeat-checker/android/${value.versionName}/Heartbeat-Checker.apk`
  )
    throw new Error("Invalid immutable APK URL");
  return value;
}
