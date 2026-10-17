export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
  ) {
    super(code);
  }
}
export async function api<T>(
  path: string,
  options: {
    method?: string;
    body?: unknown;
    authenticated?: boolean;
    signal?: AbortSignal;
  } = {},
): Promise<T> {
  const method = options.method ?? "GET";
  const headers: Record<string, string> = {};
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  if (options.authenticated && !["GET", "HEAD"].includes(method)) {
    const { csrfToken } = await api<{ csrfToken: string }>("/auth/csrf");
    headers["X-CSRF-Token"] = csrfToken;
  }
  let response: Response;
  try {
    response = await fetch(`/api/v1${path}`, {
      method,
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin",
      cache: "no-store",
      signal: options.signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError")
      throw error;
    throw new ApiError(0, "network_error");
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new ApiError(response.status, data.error ?? "unknown_error");
  }
  if (response.status === 204 || response.status === 202) return undefined as T;
  return response.json() as Promise<T>;
}
const messages: Record<string, string> = {
  invalid_credentials: "Неверный email или пароль.",
  unauthorized: "Сессия завершена. Войдите снова.",
  invalid_request:
    "Проверьте введённые данные. Ссылка также могла истечь или уже быть использована.",
  challenge_failed:
    "Проверка безопасности не пройдена. Повторите её и отправьте форму снова.",
  invalid_csrf:
    "Не удалось подтвердить запрос. Обновите страницу и повторите действие.",
  forbidden: "Нет доступа к этому действию. Проверьте адрес сайта.",
  rate_limited: "Слишком много попыток. Подождите и попробуйте позже.",
  temporarily_unavailable:
    "Сервис временно недоступен. Попробуйте немного позже.",
  network_error:
    "Не удалось связаться с сервером. Проверьте соединение и повторите попытку.",
  not_found: "Запись не найдена. Возможно, она уже удалена.",
  quota_exceeded:
    "Достигнут лимит мониторов. Удалите ненужный монитор или увеличьте квоту.",
  entitlement_required: "Для включения проверок нужна действующая квота.",
  subscription_required: "Для включения проверок нужна действующая квота.",
  version_conflict:
    "Настройки уже изменены. Обновите список и попробуйте снова.",
  conflict: "Настройки уже изменены. Обновите список и попробуйте снова.",
  invalid_url:
    "Укажите публичный HTTP или HTTPS адрес. Внутренние и служебные адреса запрещены.",
};
export function errorMessage(error: unknown) {
  return error instanceof ApiError
    ? (messages[error.code] ??
        "Не удалось выполнить действие. Попробуйте ещё раз.")
    : "Не удалось выполнить действие. Попробуйте ещё раз.";
}
