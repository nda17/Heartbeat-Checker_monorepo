export type Monitor = {
  id: string;
  name: string;
  url: string;
  method: "GET" | "HEAD";
  expectedStatus: number;
  bodyContains: string;
  enabled: boolean;
  version: number;
  state: "UNKNOWN" | "UP" | "DOWN" | "PAUSED" | "SUBSCRIPTION_REQUIRED";
  lastCheckedAt: string | null;
  lastHttpStatus: number | null;
  lastLatencyMs: number | null;
  nextCheckAt: string | null;
};
export type Entitlement = {
  quota: number;
  activeUntil: string | null;
  source: string;
  monitorCount: number;
};
export type CheckResult = {
  id: string;
  outcome: string;
  errorCode?: string;
  checkedAt: string;
  httpStatus?: number;
  latencyMs: number;
};
export type Incident = {
  id: string;
  openedAt: string;
  confirmedAt: string;
  closedAt?: string | null;
  closeReason?: "recovered" | "config_changed" | "paused" | "deleted";
};
const labels: Record<string, string> = {
  UNKNOWN: "Ожидает проверки",
  UP: "Доступен",
  DOWN: "Недоступен",
  PAUSED: "На паузе",
  SUBSCRIPTION_REQUIRED: "Нужна квота",
};
export function Status({ state }: { state: string }) {
  return (
    <span className={`badge ${state.toLowerCase()}`}>
      <span className="status-dot" />
      {labels[state] ?? state}
    </span>
  );
}
