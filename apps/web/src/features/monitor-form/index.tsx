"use client";
import { FormEvent, useState } from "react";
import { useSession } from "@/entities/session";
import { Monitor } from "@/entities/monitor";
import { api, ApiError, errorMessage } from "@/shared/api";
import { Dialog } from "@/shared/dialog";
import { Notice, Spinner } from "@/shared/ui";
export function MonitorForm({
  monitor,
  draft,
  onClose,
  onSaved,
  basePath = "/monitors",
  requireReason = false,
}: {
  monitor?: Monitor;
  draft?: { name?: string; url: string; accountId: string };
  onClose: () => void;
  onSaved: () => Promise<void>;
  basePath?: string;
  requireReason?: boolean;
}) {
  const { refresh } = useSession();
  const [method, setMethod] = useState(monitor?.method ?? "GET");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError("");
    const body = {
      name: String(data.get("name")).trim(),
      url: String(data.get("url")).trim(),
      method,
      expectedStatus: Number(data.get("expectedStatus")),
      intervalMinutes: Number(data.get("intervalMinutes")),
      failureThreshold: Number(data.get("failureThreshold")),
      bodyContains:
        method === "HEAD" ? "" : String(data.get("bodyContains") ?? ""),
      enabled: data.get("enabled") === "on",
      ...(requireReason
        ? { reason: String(data.get("reason") ?? "").trim() }
        : {}),
      ...(monitor ? { version: monitor.version } : {}),
      ...(draft && !monitor ? { expectedAccountId: draft.accountId } : {}),
    };
    if (new TextEncoder().encode(body.bodyContains).length > 1000) {
      setError("Текст условия слишком длинный: максимум 1000 байт.");
      return;
    }
    try {
      const url = new URL(body.url);
      if (
        !["http:", "https:"].includes(url.protocol) ||
        url.username ||
        url.password ||
        url.hash
      ) {
        setError(
          "Укажите HTTP или HTTPS URL без логина, пароля и фрагмента после #.",
        );
        return;
      }
    } catch {
      setError("Проверьте адрес монитора.");
      return;
    }
    setBusy(true);
    try {
      await api(
        `${basePath}${monitor ? `/${encodeURIComponent(monitor.id)}` : ""}`,
        { method: monitor ? "PATCH" : "POST", body, authenticated: true },
      );
      await onSaved();
      onClose();
    } catch (e) {
      setError(errorMessage(e));
      if (e instanceof ApiError && e.code === "account_changed")
        await refresh();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      title={monitor ? "Настройки монитора" : "Новый монитор"}
      onClose={onClose}
      busy={busy}
    >
      <p className="dialog-description">
        Один адрес, один HTTP-запрос. Укажите условия успешной проверки.
      </p>
      <form className="form-stack" onSubmit={submit}>
        {error && <Notice>{error}</Notice>}
        <label>
          Название
          <input
            name="name"
            autoFocus
            defaultValue={monitor?.name ?? draft?.name}
            required
            minLength={1}
            maxLength={100}
            placeholder="Основной сайт"
          />
        </label>
        <label>
          URL
          <input
            name="url"
            type="url"
            defaultValue={monitor?.url ?? draft?.url}
            required
            maxLength={2048}
            placeholder="https://example.com"
          />
          <span className="field-hint">
            Публичный HTTP или HTTPS адрес. Без логина и пароля в URL.
          </span>
        </label>
        <div className="form-row">
          <label>
            Метод
            <select
              value={method}
              onChange={(e) => setMethod(e.target.value as "GET" | "HEAD")}
            >
              <option>GET</option>
              <option>HEAD</option>
            </select>
          </label>
          <label>
            Ожидаемый HTTP-код
            <input
              type="number"
              name="expectedStatus"
              defaultValue={monitor?.expectedStatus ?? 200}
              required
              min={100}
              max={599}
            />
          </label>
        </div>
        {method === "GET" && (
          <label>
            Текст в ответе <span className="optional">необязательно</span>
            <input
              name="bodyContains"
              defaultValue={monitor?.bodyContains}
              maxLength={1000}
              placeholder="Например: Welcome"
            />
            <span className="field-hint">
              Успех, если в теле ответа присутствует этот текст.
            </span>
          </label>
        )}
        <div className="form-row">
          <label>
            Интервал проверки
            <select
              name="intervalMinutes"
              defaultValue={monitor ? monitor.intervalMinutes : 5}
              aria-describedby="monitor-check-policy"
              required
            >
              {Array.from({ length: 10 }, (_, index) => index + 1).map(
                (value) => (
                  <option key={value} value={value}>
                    {value} мин
                  </option>
                ),
              )}
            </select>
          </label>
          <label>
            Сбоев подряд до инцидента
            <select
              name="failureThreshold"
              defaultValue={monitor ? monitor.failureThreshold : 2}
              aria-describedby="monitor-check-policy"
              required
            >
              {Array.from({ length: 10 }, (_, index) => index + 1).map(
                (value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ),
              )}
            </select>
          </label>
        </div>
        <p id="monitor-check-policy" className="field-hint">
          Первый успешный ответ закрывает инцидент. Неизвестный результат
          (UNKNOWN) сбрасывает серию сбоев.
        </p>
        {monitor && (
          <p className="field-hint">
            При изменении интервала или порога открытый инцидент завершится с
            причиной «Настройки изменены», серия сбоев сбросится, а следующая
            проверка будет запланирована сразу.
          </p>
        )}
        <label className="checkbox-label">
          <input
            type="checkbox"
            name="enabled"
            defaultChecked={monitor?.enabled ?? true}
          />
          <span>
            Включить проверки
            <span className="field-hint">
              Проверки выполняются при действующей квоте.
            </span>
          </span>
        </label>
        {requireReason && (
          <label>
            Причина изменения
            <textarea
              name="reason"
              required
              minLength={1}
              maxLength={500}
              rows={3}
            />
          </label>
        )}
        <div className="dialog-actions">
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={onClose}
          >
            Отмена
          </button>
          <button className="primary" disabled={busy}>
            {busy ? (
              <>
                <Spinner />
                Сохраняем…
              </>
            ) : monitor ? (
              "Сохранить"
            ) : (
              "Добавить монитор"
            )}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
