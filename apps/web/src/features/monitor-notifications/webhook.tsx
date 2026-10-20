"use client";
import { notify } from "@/shared/notifications";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import type { PutWebhook, Webhook, WebhookDelivery } from "@/entities/webhook";
import { api, ApiError, errorMessage } from "@/shared/api";
import { dateTime, Icon, Loading, Notice } from "@/shared/ui";

const statusLabels: Record<string, string> = {
  pending: "Ожидает отправки",
  leased: "Отправляется",
  delivered: "Принято получателем",
  failed: "Доставка прекращена",
  cancelled: "Отменено",
};
const deliveryErrors: Record<string, string> = {
  http_status: "Получатель вернул неуспешный HTTP-код.",
  request_failed: "Сетевая ошибка или истекло время ожидания ответа.",
  blocked_target:
    "Адрес недоступен для безопасной отправки. Проверьте DNS и публичный HTTPS-адрес.",
  probe_unavailable: "Отправка временно недоступна.",
  endpoint_changed: "Настройки webhook изменились.",
  endpoint_inactive: "Webhook выключен или больше не доступен для доставки.",
  deadline_exceeded: "Истёк срок повторных попыток.",
  attempts_exhausted: "Исчерпаны попытки доставки.",
};

// Remounting discards the one-time secret when the selected monitor changes.
export function WebhookNotifications({ monitorId }: { monitorId: string }) {
  return <WebhookEditor key={monitorId} monitorId={monitorId} />;
}

function WebhookEditor({ monitorId }: { monitorId: string }) {
  const path = `/monitors/${encodeURIComponent(monitorId)}/webhook`;
  const [webhook, setWebhook] = useState<Webhook | null>();
  const [url, setUrl] = useState("");
  const [secret, setSecret] = useState("");
  const [copied, setCopied] = useState(false);
  const [needsSecret, setNeedsSecret] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const [uncertain, setUncertain] = useState(false);
  const [confirmation, setConfirmation] = useState<
    "url" | "rotate" | "delete" | null
  >(null);
  const [deliveries, setDeliveries] = useState<WebhookDelivery[] | null>(null);
  const [historyError, setHistoryError] = useState("");
  const [historyBusy, setHistoryBusy] = useState(false);
  const alive = useRef(true);
  const writing = useRef(false);
  const controllers = useRef(new Set<AbortController>());
  const secretRevision = useRef(0);
  const historyRequest = useRef(0);
  const configRequest = useRef(0);
  const generation = useRef(0);

  const request = useCallback(
    async <T,>(task: (signal: AbortSignal) => Promise<T>) => {
      const controller = new AbortController();
      controllers.current.add(controller);
      let timeout: ReturnType<typeof setTimeout>;
      const aborted = new Promise<never>((_, reject) => {
        controller.signal.addEventListener(
          "abort",
          () => reject(new DOMException("Request aborted", "AbortError")),
          { once: true },
        );
        timeout = setTimeout(() => controller.abort(), 10_000);
      });
      try {
        return await Promise.race([task(controller.signal), aborted]);
      } finally {
        clearTimeout(timeout!);
        controllers.current.delete(controller);
      }
    },
    [],
  );

  const loadConfig = useCallback(async () => {
    const view = generation.current;
    const sequence = ++configRequest.current;
    setLoadError("");
    try {
      let current: Webhook | null;
      try {
        const response = await request((signal) =>
          api<{ webhook: Webhook }>(path, { signal }),
        );
        current = response.webhook;
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 404) throw e;
        current = null;
      }
      if (
        !alive.current ||
        view !== generation.current ||
        sequence !== configRequest.current
      )
        return false;
      setWebhook(current);
      setUrl(current?.url ?? "");
      setUncertain(false);
      return true;
    } catch (e) {
      if (
        alive.current &&
        view === generation.current &&
        sequence === configRequest.current
      )
        setLoadError(
          e instanceof DOMException && e.name === "AbortError"
            ? "Загрузка webhook заняла слишком много времени. Повторите загрузку."
            : errorMessage(e),
        );
      return false;
    }
  }, [path, request]);

  const loadHistory = useCallback(async () => {
    const view = generation.current;
    const sequence = ++historyRequest.current;
    setHistoryBusy(true);
    setHistoryError("");
    try {
      const response = await request((signal) =>
        api<{ deliveries: WebhookDelivery[] }>(`${path}/deliveries?limit=50`, {
          signal,
        }),
      );
      if (
        alive.current &&
        view === generation.current &&
        sequence === historyRequest.current
      )
        setDeliveries(response.deliveries);
    } catch (e) {
      if (
        alive.current &&
        view === generation.current &&
        sequence === historyRequest.current
      )
        setHistoryError(
          e instanceof DOMException && e.name === "AbortError"
            ? "История не загрузилась вовремя. Повторите загрузку."
            : errorMessage(e),
        );
    } finally {
      if (
        alive.current &&
        view === generation.current &&
        sequence === historyRequest.current
      )
        setHistoryBusy(false);
    }
  }, [path, request]);

  useEffect(() => {
    const view = ++generation.current;
    alive.current = true;
    // Initial requests reset their retry indicators before awaiting the API.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void Promise.all([loadConfig(), loadHistory()]);
    const activeControllers = controllers.current;
    return () => {
      generation.current = view + 1;
      alive.current = false;
      for (const controller of activeControllers) controller.abort();
      activeControllers.clear();
    };
  }, [loadConfig, loadHistory]);

  async function write(input: PutWebhook | null) {
    if (writing.current || uncertain || !alive.current) return;
    writing.current = true;
    const view = generation.current;
    setBusy(true);
    setError("");

    setConfirmation(null);
    try {
      if (input) {
        const response = await request((signal) =>
          api<{ webhook: Webhook; secret?: string }>(path, {
            method: "PUT",
            authenticated: true,
            body: input,
            signal,
          }),
        );
        if (!alive.current || view !== generation.current) return;
        setWebhook(response.webhook);
        setUrl(response.webhook.url);
        if (response.secret) {
          updateSecret(response.secret);
          setNeedsSecret(false);
          setCopied(false);
        }
        notify.success(
          response.webhook.enabled
            ? "Webhook включён. Отправляются только новые события."
            : response.secret
              ? "Webhook сохранён и выключен. Сохраните секрет у получателя, затем включите доставку."
              : "Webhook выключен. Незавершённые доставки отменены.",
        );
      } else {
        if (!webhook) return;
        await request((signal) =>
          api<void>(path, {
            method: "DELETE",
            authenticated: true,
            body: { id: webhook.id, version: webhook.version },
            signal,
          }),
        );
        if (!alive.current || view !== generation.current) return;
        setWebhook(null);
        setUrl("");
        updateSecret("");
        setNeedsSecret(false);
        notify.success("Webhook и его история доставок удалены.");
      }
      // A failed history GET cannot turn a committed settings write into an error.
      void loadHistory();
    } catch (e) {
      if (!alive.current || view !== generation.current) return;
      if (!(e instanceof ApiError) || e.status === 0 || e.status >= 500) {
        setUncertain(true);
        updateSecret("");
        if (
          input &&
          (!webhook || input.rotateSecret || input.url !== webhook.url)
        )
          setNeedsSecret(true);
        setError(
          "Не удалось получить результат изменения. Сверяем настройки с сервером. Повторная запись автоматически не отправляется.",
        );
        const reconciled = await loadConfig();
        if (alive.current && view === generation.current && reconciled)
          setError(
            "Настройки сверены с сервером. Если ответ с новым секретом потерян, явно обновите секрет и настройте получателя перед включением.",
          );
      } else {
        notify.error(errorMessage(e));
        if (e.status === 409 || e.status === 404) {
          setUncertain(true);
          updateSecret("");
          await loadConfig();
        }
      }
    } finally {
      writing.current = false;
      if (alive.current && view === generation.current) setBusy(false);
    }
  }

  function currentInput(changes: Partial<PutWebhook> = {}): PutWebhook {
    return {
      id: webhook?.id,
      version: webhook?.version ?? 0,
      url: webhook?.url ?? url.trim(),
      enabled: webhook?.enabled ?? false,
      ...changes,
    };
  }
  function saveURL(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || uncertain) return;
    try {
      const target = new URL(url.trim());
      if (
        target.protocol !== "https:" ||
        target.username ||
        target.password ||
        target.hash ||
        (target.port && target.port !== "443")
      )
        throw new Error("Invalid URL");
    } catch {
      setError(
        "Укажите публичный HTTPS-адрес на порту 443, без логина, пароля или фрагмента.",
      );
      return;
    }
    if (webhook) setConfirmation("url");
    else void write(currentInput({ enabled: false }));
  }
  function updateSecret(value: string) {
    ++secretRevision.current;
    setSecret(value);
    setCopied(false);
  }
  async function copySecret() {
    const revision = secretRevision.current;
    try {
      await navigator.clipboard.writeText(secret);
      if (alive.current && revision === secretRevision.current) setCopied(true);
    } catch {
      if (alive.current && revision === secretRevision.current)
        notify.error(
          "Не удалось скопировать секрет. Выделите его и скопируйте вручную.",
        );
    }
  }

  return (
    <section className="messenger-route webhook-route" aria-label="Webhook">
      <h3>Webhook</h3>
      <p className="small muted">
        POST с событием сбоя (DOWN) или восстановления (UP) на ваш публичный
        HTTPS-адрес, порт 443. Например: https://example.com/hooks/heartbeat.
      </p>

      {error && <Notice>{error}</Notice>}
      {loadError && <Notice>{loadError}</Notice>}
      {webhook === undefined ? (
        loadError ? (
          <button className="secondary" onClick={() => void loadConfig()}>
            Повторить загрузку webhook
          </button>
        ) : (
          <Loading text="Загружаем webhook…" />
        )
      ) : (
        <>
          <p className="small">
            <strong>
              {webhook?.enabled ? "Доставка включена" : "Доставка выключена"}
            </strong>
          </p>
          <form className="form-stack" onSubmit={saveURL}>
            <label>
              Webhook URL
              <input
                type="url"
                name="webhookUrl"
                value={url}
                onChange={(event) => setUrl(event.target.value)}
                required
                maxLength={2048}
                placeholder="https://example.com/hooks/heartbeat"
                disabled={busy || uncertain}
              />
            </label>
            <button
              className="secondary"
              disabled={
                busy || uncertain || (!!webhook && url.trim() === webhook.url)
              }
            >
              {busy
                ? "Сохраняем…"
                : webhook
                  ? "Изменить URL"
                  : "Создать webhook"}
            </button>
          </form>
          {secret && (
            <div className="webhook-secret">
              <strong>Секрет подписи — показывается один раз</strong>
              <p className="small">
                Сохраните его у получателя до закрытия окна. После закрытия
                получить этот секрет снова нельзя.
              </p>
              <code>{secret}</code>
              <button
                type="button"
                className="secondary"
                disabled={busy || uncertain}
                onClick={() => void copySecret()}
              >
                {copied ? "Секрет скопирован" : "Скопировать секрет"}
              </button>
            </div>
          )}
          {needsSecret && webhook && (
            <Notice>
              Ответ с секретом мог потеряться. Обновите секрет, сохраните его у
              получателя, затем включите webhook.
            </Notice>
          )}
          {webhook && (
            <div className="webhook-actions">
              <button
                className="primary"
                disabled={
                  busy || uncertain || (!webhook.enabled && needsSecret)
                }
                onClick={() =>
                  void write(currentInput({ enabled: !webhook.enabled }))
                }
              >
                {webhook.enabled ? "Выключить webhook" : "Включить webhook"}
              </button>
              <button
                className="secondary"
                disabled={busy || uncertain}
                onClick={() => setConfirmation("rotate")}
              >
                Обновить секрет
              </button>
              <button
                className="quiet danger-text"
                disabled={busy || uncertain}
                onClick={() => setConfirmation("delete")}
              >
                Удалить webhook
              </button>
            </div>
          )}
          {uncertain && (
            <button
              className="secondary"
              disabled={busy}
              onClick={() => void loadConfig()}
            >
              Сверить настройки webhook
            </button>
          )}
          {confirmation && (
            <div
              className="webhook-confirmation"
              role="group"
              aria-label="Подтверждение изменения webhook"
            >
              <p>
                {confirmation === "delete"
                  ? "Удалить webhook и всю его историю доставок?"
                  : "Webhook будет выключен, незавершённые доставки отменены, прежний секрет перестанет действовать. Настройте получателя с новым секретом, затем явно включите доставку."}
              </p>
              <div className="webhook-actions">
                <button
                  className="danger"
                  disabled={busy}
                  onClick={() =>
                    void write(
                      confirmation === "delete"
                        ? null
                        : currentInput({
                            enabled: false,
                            url:
                              confirmation === "url"
                                ? url.trim()
                                : webhook!.url,
                            rotateSecret: true,
                          }),
                    )
                  }
                >
                  {confirmation === "delete"
                    ? "Подтвердить удаление webhook"
                    : confirmation === "url"
                      ? "Подтвердить изменение URL"
                      : "Подтвердить обновление секрета"}
                </button>
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => setConfirmation(null)}
                >
                  Отмена
                </button>
              </div>
            </div>
          )}
        </>
      )}
      <details className="webhook-instructions">
        <summary>
          Как настроить получателя <Icon name="chevron-down" />
        </summary>
        <p>
          Тело JSON: schemaVersion, id, type (monitor.down или monitor.up),
          occurredAt, monitorId, incidentId, sequence. Названия и адреса
          мониторов в тело не входят.
        </p>
        <pre>
          {
            '{"schemaVersion":1,"id":"<event UUID>","type":"monitor.down","occurredAt":"2026-10-19T12:00:00Z","monitorId":"<monitor UUID>","incidentId":"<incident UUID>","sequence":1}'
          }
        </pre>
        <p>
          Заголовки: <code>X-Heartbeat-Event-Id</code>,{" "}
          <code>X-Heartbeat-Timestamp</code> (Unix seconds),{" "}
          <code>X-Heartbeat-Signature</code> (<code>v1=&lt;hex&gt;</code>).
        </p>
        <p>
          Подпись: HMAC-SHA256 по точным байтам{" "}
          <code>timestamp.eventId.exactBody</code>. Ключ —
          base64url-декодированный секрет (32 байта). Читайте исходное тело до
          разбора JSON; сравнивайте подпись за постоянное время, проверяйте
          возраст timestamp (например, до 5 минут) и совпадение eventId с id
          тела.
        </p>
        <p>
          Обрабатывайте id один раз, сохраняйте его для защиты от дублей и
          отвечайте HTTP 2xx после приёма. До 8 попыток за 24 часа: повторяются
          сетевые ошибки, HTTP 408, 429 и 5xx. HTTP 2xx подтверждает приём, а не
          выполнение действия у получателя.
        </p>
        <p>
          Старые события после включения не воспроизводятся. Отключение отменяет
          дальнейшие попытки; уже начатый POST может завершиться после
          отключения.
        </p>
      </details>
      <div className="webhook-history">
        <div className="webhook-history-heading">
          <h4>История webhook</h4>
          <button
            className="secondary"
            disabled={historyBusy}
            onClick={() => void loadHistory()}
          >
            {historyBusy ? "Загружаем…" : "Обновить историю webhook"}
          </button>
        </div>
        <p className="small muted">
          Последние 50 доставок. Завершённые записи хранятся 30 дней.
        </p>
        {historyError && <Notice>{historyError}</Notice>}
        {deliveries === null ? (
          !historyError && <Loading text="Загружаем историю webhook…" />
        ) : deliveries.length ? (
          <ul className="webhook-deliveries">
            {deliveries.map((delivery) => (
              <li key={delivery.id}>
                <strong>
                  {delivery.type === "monitor.down"
                    ? "Сбой (DOWN)"
                    : delivery.type === "monitor.up"
                      ? "Восстановление (UP)"
                      : "Событие"}{" "}
                  · {statusLabels[delivery.status] ?? "Неизвестный статус"}
                </strong>
                <span>
                  {dateTime(delivery.occurredAt)} · Попыток: {delivery.attempts}
                  {delivery.lastHttpStatus
                    ? ` · HTTP ${delivery.lastHttpStatus}`
                    : ""}
                </span>
                {delivery.completedAt && (
                  <span>Завершено: {dateTime(delivery.completedAt)}</span>
                )}
                {delivery.lastErrorCode && (
                  <span>
                    {deliveryErrors[delivery.lastErrorCode] ??
                      "Доставка не завершилась успешно. Проверьте получателя."}
                  </span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="small muted">Доставок пока нет.</p>
        )}
      </div>
    </section>
  );
}
