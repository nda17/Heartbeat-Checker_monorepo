export type CheckType = "http" | "tcp" | "icmp";
export type NotificationChannel = "telegram" | "max" | "email" | "webhook";
export type Monitor = {
  id: string;
  checkType?: CheckType;
  name: string;
  url: string;
  host?: string;
  port?: number;
  method: "GET" | "HEAD" | "";
  expectedStatus: number;
  bodyContains: string;
  intervalMinutes: number;
  failureThreshold: number;
  enabled: boolean;
  version: number;
  state: "UNKNOWN" | "UP" | "DOWN" | "PAUSED" | "SUBSCRIPTION_REQUIRED";
  lastCheckedAt: string | null;
  lastHttpStatus: number | null;
  lastLatencyMs: number | null;
  lastRoundTripMs?: number | null;
  nextCheckAt: string | null;
  notificationChannels?: NotificationChannel[];
};
export type MonitorSort =
  "created_desc" | "created_asc" | "name_asc" | "name_desc";
export type MonitorPagination = {
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
};
export type MonitorSummary = {
  total: number;
  up: number;
  down: number;
  unknown: number;
  paused: number;
  subscriptionRequired: number;
};
export type MonitorPage = {
  monitors: Monitor[];
  pagination: MonitorPagination;
  summary: MonitorSummary;
};
export type Entitlement = {
  quota: number;
  activeUntil: string | null;
  source: string;
  monitorCount: number;
};
export type CheckResult = {
  id: string;
  checkType?: CheckType;
  outcome: string;
  errorCode?: string;
  checkedAt: string;
  httpStatus?: number;
  latencyMs: number;
  roundTripMs?: number | null;
};
export type Incident = {
  id: string;
  number: number;
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

const resultErrors: Record<string, string> = {
  tcp_connection_refused:
    "Сервер отклонил TCP-соединение. Проверьте порт и сетевой экран.",
  tcp_timeout: "Время ожидания TCP-соединения истекло.",
  tcp_failed: "Не удалось установить TCP-соединение с указанным портом.",
  icmp_timeout:
    "Ответ ICMP не получен вовремя. Сервер или сетевой экран может блокировать ping.",
  icmp_failed:
    "Не удалось получить ответ ICMP. Проверьте адрес и доступность ICMP.",
  dns_failed: "Не удалось определить IP-адрес домена.",
  address_family_unavailable:
    "У домена нет доступного IPv4-адреса. Проверка TCP/ICMP через IPv6 пока не поддерживается; результат не определён.",
  request_timeout:
    "Сервер не ответил вовремя. Возможна задержка соединения или ответа.",
  tls_certificate_invalid: "Не удалось проверить HTTPS-сертификат сервера.",
  tls_handshake_failed: "Не удалось установить защищённое HTTPS-соединение.",
  request_failed:
    "Не удалось выполнить HTTP-запрос. Проверьте доступность адреса.",
  probe_unavailable: "Проверка временно недоступна. Результат не определён.",
  check_cancelled: "Проверка прервана. Результат не определён.",
  redirect_limit: "Сервер вернул слишком много перенаправлений.",
  invalid_redirect: "Сервер перенаправил запрос на недопустимый адрес.",
  response_too_large: "Ответ сервера превышает допустимый размер.",
  unexpected_status: "HTTP-код ответа отличается от ожидаемого.",
  body_mismatch: "В ответе нет ожидаемого текста.",
};
export function resultErrorMessage(code: string) {
  return (
    resultErrors[code] ??
    "Проверка завершилась ошибкой. Проверьте адрес и условия проверки."
  );
}

export function monitorCheckType(value?: string): CheckType {
  return value === "tcp" || value === "icmp" ? value : "http";
}
export function checkTypeLabel(value?: string) {
  const type = monitorCheckType(value);
  return type === "http" ? "HTTP/HTTPS" : type.toUpperCase();
}
export function monitorTarget(monitor: Monitor) {
  const type = monitorCheckType(monitor.checkType);
  if (type === "http") return monitor.url;
  return type === "tcp"
    ? `${monitor.host}:${monitor.port}`
    : (monitor.host ?? "");
}
export function checkTiming(
  checkType: CheckType | undefined,
  outcome: string,
  latencyMs: number | null,
  roundTripMs?: number | null,
) {
  if (latencyMs === null) return "Нет данных";
  const type = monitorCheckType(checkType);
  if (type === "icmp") {
    if ((outcome === "UP" || outcome === "SUCCESS") && roundTripMs != null)
      return `Пинг (ICMP): ${roundTripMs} мс`;
    if (outcome === "DOWN" || outcome === "FAILURE")
      return `Ответ ICMP не получен; ожидание ${latencyMs} мс`;
    return `Длительность ICMP-проверки: ${latencyMs} мс`;
  }
  return `Длительность ${type.toUpperCase()}-проверки: ${latencyMs} мс`;
}
