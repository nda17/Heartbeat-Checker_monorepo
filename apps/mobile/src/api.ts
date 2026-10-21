import { native } from "./native";
import type { AuthResult } from "./types";

export const ORIGIN = "https://heartbeat-checker.ru";
const API_ORIGIN = native.API_ORIGIN;
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
  ) {
    super(code);
  }
}
class NetworkError extends Error {}

let current: AuthResult | null = null;
let epoch = 0;
const sessionProofs = new WeakMap<object, { js: number; native: number }>();
let sessionExpiredHandler: (() => void) | null = null;
export function session() {
  return current;
}
export function sessionEpoch() {
  return epoch;
}
export function onSessionExpired(handler: (() => void) | null) {
  sessionExpiredHandler = handler;
}
export async function restoreSession() {
  const value = await native.restore();
  current = value;
  epoch++;
  return value;
}
export async function acceptSession(value: AuthResult) {
  const proof = sessionProofs.get(value) ?? sessionProofs.get(value.session);
  if (!proof || proof.js !== epoch) throw new ApiError(409, "session_changed");
  await native.saveSession(value, proof.native);
  const saved = await native.restore();
  if (proof.js !== epoch) throw new ApiError(409, "session_changed");
  if (!saved) throw new ApiError(409, "session_changed");
  current = saved;
  epoch++;
}
export async function closeSession() {
  // Native writes the durable logout gate before any network operation.
  current = null;
  epoch++;
  await native.logout();
}

export async function api<T>(
  path: string,
  options: {
    method?: string;
    body?: unknown;
    auth?: boolean;
    headers?: Record<string, string>;
  } = {},
): Promise<T> {
  if (!path.startsWith("/") || path.startsWith("//") || path.includes("://"))
    throw new Error("Invalid API path");
  const capturedEpoch = epoch;
  const nativeEpoch = await native.authEpoch();
  if (capturedEpoch !== epoch) throw new ApiError(409, "session_changed");
  const auth = options.auth !== false;
  const token = auth ? current?.session.token : undefined;
  const extraHeaders = options.headers ?? {};
  if (
    Object.keys(extraHeaders).some(
      (key) => key.toLowerCase() !== "x-heartbeat-registration-token",
    )
  )
    throw new Error("Invalid API header");
  if (auth && !token) throw new ApiError(401, "session_expired");
  if (auth && Date.parse(current!.session.expiresAt) <= Date.now()) {
    await closeSession();
    sessionExpiredHandler?.();
    throw new ApiError(401, "session_expired");
  }
  const response = await fetch(`${API_ORIGIN}/api/v1${path}`, {
    method: options.method ?? "GET",
    headers: {
      Accept: "application/json",
      ...(options.body === undefined
        ? {}
        : { "Content-Type": "application/json" }),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...extraHeaders,
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    credentials: "omit",
  }).catch(() => {
    throw new NetworkError();
  });
  // An earlier account's response must not be rendered after logout or account switch.
  if (capturedEpoch !== epoch) throw new ApiError(409, "session_changed");
  const payload =
    response.status === 204 ||
    (response.status === 202 &&
      !response.headers.get("content-type")?.includes("json"))
      ? null
      : await response.json().catch(() => null);
  if (capturedEpoch !== epoch) throw new ApiError(409, "session_changed");
  if (auth && response.status === 401) {
    await closeSession();
    sessionExpiredHandler?.();
    throw new ApiError(401, "session_expired");
  }
  if (!response.ok)
    throw new ApiError(response.status, payload?.error ?? "network_error");
  if (payload && typeof payload === "object") {
    const proof = { js: capturedEpoch, native: nativeEpoch };
    sessionProofs.set(payload, proof);
    if (payload.session && typeof payload.session === "object")
      sessionProofs.set(payload.session, proof);
  }
  return payload as T;
}

const messages: Record<string, string> = {
  invalid_credentials: "Неверный email или пароль.",
  unauthorized: "Войдите снова.",
  session_expired: "Сессия истекла. Войдите снова.",
  version_conflict: "Данные изменились. Обновите экран и повторите.",
  quota_exceeded: "Недостаточно доступных мониторов.",
  entitlement_required: "Для включения монитора требуется доступная квота.",
  price_changed: "Цена изменилась. Обновите стоимость перед оплатой.",
  session_changed: "Аккаунт изменился. Обновите экран.",
  invalid_request: "Проверьте заполнение полей.",
  temporarily_unavailable: "Сервис временно недоступен. Повторите позже.",
  challenge_failed: "Проверка безопасности не прошла. Повторите её.",
  rate_limited: "Слишком много попыток. Подождите и повторите позже.",
  email_code_rate_limited:
    "Слишком много запросов кода. Подождите и повторите позже.",
};
export function errorText(error: unknown) {
  if (error instanceof NetworkError)
    return "Не удалось подключиться. Проверьте сеть и повторите.";
  if (error instanceof ApiError)
    return (
      messages[error.code] ?? "Не удалось выполнить действие. Повторите позже."
    );
  return "Ошибка приложения. Перезапустите его и повторите действие.";
}
