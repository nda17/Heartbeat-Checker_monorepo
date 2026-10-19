import release from "./release.json";
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

export {
  captureMonitorDraft,
  clearMonitorDraft,
  draftChanged,
  parseMonitorDraft,
  readMonitorDraft,
} from "./draft";
export type { MonitorDraft } from "./draft";
