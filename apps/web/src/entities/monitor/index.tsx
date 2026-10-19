export type Monitor = {
  id: string;
  name: string;
  url: string;
  method: "GET" | "HEAD";
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
