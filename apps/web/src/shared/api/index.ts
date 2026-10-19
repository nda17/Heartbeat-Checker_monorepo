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
  mock_billing_forbidden: "Тестовая оплата недоступна для вашего аккаунта.",
  billing_conflict:
    "Завершите текущий тестовый заказ. Для действующей квоты выберите прежнее количество мониторов.",
  invalid_credentials: "Неверный email или пароль.",
  unauthorized: "Сессия завершена. Войдите снова.",
  account_changed:
    "Аккаунт изменился. Черновик не сохранён.",
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
  link_not_ready:
    "Чат ещё не проверен ботом или срок привязки истёк. Повторите подключение.",
  mfa_enrollment_required:
    "Для работы с администрированием настройте приложение-аутентификатор.",
  mfa_required: "Подтвердите вход одноразовым кодом или резервным кодом.",
  mfa_step_up_required:
    "Для изменения данных повторно подтвердите вход одноразовым кодом.",
  conflict: "Настройки уже изменены. Обновите список и попробуйте снова.",
  oauth_provider_unavailable:
    "Вход через этого провайдера временно недоступен.",
  oauth_login_requires_logout:
    "Для входа через другой аккаунт сначала выйдите из текущего.",
  oauth_identity_conflict:
    "Этот профиль провайдера уже подключён к другому аккаунту.",
  oauth_email_conflict:
    "Аккаунт с этим email уже есть. Войдите существующим способом и подключите провайдера в настройках.",
  oauth_registration_expired:
    "Время подтверждения истекло. Начните регистрацию через провайдера заново.",
  password_setup_required:
    "Сначала установите пароль через восстановление доступа.",
  invalid_url:
    "Укажите публичный HTTP или HTTPS адрес. Внутренние и служебные адреса запрещены.",
};
export function errorMessage(error: unknown) {
  return error instanceof ApiError
    ? (messages[error.code] ??
        "Не удалось выполнить действие. Попробуйте ещё раз.")
    : "Не удалось выполнить действие. Попробуйте ещё раз.";
}
const oauthMessages: Record<string, string> = {
  oauth_cancelled:
    "Вход через провайдера отменён. Вы можете попробовать снова.",
  oauth_failed:
    "Не удалось завершить вход через провайдера. Попробуйте ещё раз.",
  oauth_state_invalid: "Срок проверки входа истёк. Начните вход заново.",
  oauth_email_required:
    "Провайдер не передал подтверждённый email. Зарегистрируйтесь по email, затем подключите провайдера в настройках.",
  oauth_email_conflict:
    "Аккаунт с этим email уже есть. Войдите существующим способом и подключите провайдера в настройках.",
  oauth_identity_conflict:
    "Этот профиль провайдера уже подключён к другому аккаунту.",
  oauth_session_changed:
    "Сессия изменилась во время подключения. Войдите снова и повторите действие.",
  oauth_registration_expired:
    "Время подтверждения истекло. Начните регистрацию через провайдера заново.",
  oauth_login_requires_logout:
    "Для входа через другой аккаунт сначала выйдите из текущего.",
  password_setup_required:
    "Сначала установите пароль через восстановление доступа.",
  oauth_provider_unavailable:
    "Вход через этого провайдера временно недоступен.",
  mfa_enrollment_required:
    "Для изменения способов входа настройте приложение-аутентификатор.",
  mfa_required: "Подтвердите вход одноразовым или резервным кодом.",
  mfa_step_up_required:
    "Для изменения способов входа повторно подтвердите вход одноразовым кодом.",
};
export function oauthCallbackMessage(code: string) {
  return oauthMessages[code] ?? oauthMessages.oauth_failed;
}
export function oauthAuthorizationUrl(provider: "yandex" | "vk", raw: string) {
  const url = new URL(raw);
  const origin =
    provider === "yandex" ? "https://oauth.yandex.ru" : "https://id.vk.ru";
  if (
    url.origin !== origin ||
    url.pathname !== "/authorize" ||
    url.hash ||
    url.username ||
    url.password
  )
    throw new Error("Invalid OAuth authorization URL");
  return url.href;
}
// Preserve only reviewed authenticated destinations after login.
export function safeAppReturnTo(value: string | null) {
  if (value === "/app" || value === "/app/admin" || value === "/app/settings")
    return value;
  if (!value?.startsWith("/app?")) return null;
  try {
    const url = new URL(value, "https://heartbeat-checker.invalid");
    if (
      url.origin !== "https://heartbeat-checker.invalid" ||
      url.pathname !== "/app" ||
      url.hash
    )
      return null;
    const params = url.searchParams;
    const uuid =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    const monitor = params.get("monitor");
    const incident = params.get("incident");
    if (
      params.size !== 2 ||
      !monitor ||
      !incident ||
      !uuid.test(monitor) ||
      !uuid.test(incident)
    )
      return null;
    return `/app?monitor=${encodeURIComponent(monitor)}&incident=${encodeURIComponent(incident)}`;
  } catch {
    return null;
  }
}
