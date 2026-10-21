export type CheckType = "http" | "tcp" | "icmp";
export type NotificationChannel =
  "telegram" | "max" | "email" | "webhook" | "push";
export type Monitor = {
  id: string;
  checkType?: CheckType;
  name: string;
  url: string;
  timeZone: string;
  provider: {
    names: string[];
    status: "pending" | "ready" | "unknown";
    checkedAt: string | null;
  };
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
export type Subscription = {
  id: string;
  quota: number;
  activeUntil: string | null;
  active: boolean;
  source: string;
  version: number;
};
export type Entitlement = {
  quota: number;
  activeUntil: string | null;
  source: string;
  monitorCount: number;
  enabledMonitorCount: number;
  activeMonitorCount: number;
  version: number;
  admin: {
    quota: number;
    activeUntil: string | null;
    version: number;
  } | null;
  packages: Subscription[];
  paid: {
    quota: number;
    activeUntil: string | null;
    source: string;
    version: number;
  } | null;
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

export function monitorDateTime(
  value: string | null | undefined,
  zone = "Europe/Moscow",
) {
  if (!value) return "—";
  const at = new Date(value);
  if (!Number.isFinite(at.getTime())) return "—";
  const options: Intl.DateTimeFormatOptions = {
    timeZone: zone,
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  };
  try {
    const offset =
      new Intl.DateTimeFormat("en", {
        timeZone: zone,
        timeZoneName: "longOffset",
      })
        .formatToParts(at)
        .find((part) => part.type === "timeZoneName")
        ?.value.replace("GMT", "UTC") ?? "UTC";
    const label = zone === "Europe/Moscow" ? "МСК" : zone;
    return `${new Intl.DateTimeFormat("ru-RU", options).format(at)} ${label} (${offset})`;
  } catch {
    const number = (part: number) => String(part).padStart(2, "0");
    const date = `${number(at.getUTCDate())}.${number(at.getUTCMonth() + 1)}.${at.getUTCFullYear()}`;
    const time = `${number(at.getUTCHours())}:${number(at.getUTCMinutes())}`;
    return `${date} ${time} UTC (пояс ${zone} недоступен)`;
  }
}

export function monitorProvider(monitor: Monitor) {
  return monitor.provider?.names.length
    ? monitor.provider.names.join("; ")
    : "Неизвестно";
}
