"use client";
import { notify } from "@/shared/notifications";
import { FormEvent, useEffect, useId, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import { Monitor, monitorCheckType } from "@/entities/monitor";
import { api, ApiError, errorMessage } from "@/shared/api";
import { Dialog } from "@/shared/dialog";
import { Icon, Notice, Spinner } from "@/shared/ui";
export function MonitorForm({
  monitor,
  onClose,
  onSaved,
  basePath = "/monitors",
  requireReason = false,
  successMessage = "Монитор сохранён.",
  canEnable = true,
}: {
  monitor?: Monitor;
  onClose: () => void;
  onSaved: () => Promise<void>;
  basePath?: string;
  requireReason?: boolean;
  successMessage?: string;
  canEnable?: boolean;
}) {
  const { refresh } = useSession();
  const httpMonitor = monitorCheckType(monitor?.checkType) === "http";
  const [checkType, setCheckType] = useState(
    monitorCheckType(monitor?.checkType),
  );
  const [httpUrl, setHttpUrl] = useState(
    httpMonitor ? (monitor?.url ?? "") : "",
  );
  const [host, setHost] = useState(monitor?.host ?? "");
  const [port, setPort] = useState(monitor?.port ? String(monitor.port) : "");
  const [method, setMethod] = useState<"GET" | "HEAD">(
    httpMonitor && monitor?.method === "HEAD" ? "HEAD" : "GET",
  );
  const [expectedStatus, setExpectedStatus] = useState(
    String(
      httpMonitor && monitor?.expectedStatus ? monitor.expectedStatus : 200,
    ),
  );
  const [bodyContains, setBodyContains] = useState(
    httpMonitor ? (monitor?.bodyContains ?? "") : "",
  );
  const [timeZone, setTimeZone] = useState(
    monitor?.timeZone ?? "Europe/Moscow",
  );
  const [timeZones, setTimeZones] = useState<{ id: string; label: string }[]>(
    [],
  );
  const [zoneError, setZoneError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    api<{ timeZones: { id: string; label: string }[] }>("/monitor-time-zones", {
      authenticated: true,
      signal: controller.signal,
    })
      .then((result) => setTimeZones(result.timeZones))
      .catch(() => {
        if (!controller.signal.aborted)
          setZoneError(
            "Не удалось загрузить список часовых поясов. Текущий выбор можно сохранить.",
          );
      });
    return () => controller.abort();
  }, []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const submitting = useRef(false);
  async function refreshSaved() {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    try {
      await onSaved();
      onClose();
    } catch {
      setError(
        "Данные не обновились. Повторите обновление, чтобы увидеть сохранённый монитор.",
      );
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting.current || saved) return;
    const data = new FormData(event.currentTarget);
    setError("");
    const targetUrl = String(data.get("url") ?? "").trim();
    const targetHost = String(data.get("host") ?? "").trim();
    const targetPort = Number(data.get("port"));
    const targetBody =
      method === "GET" ? String(data.get("bodyContains") ?? "") : "";
    const target =
      checkType === "http"
        ? {
            url: targetUrl,
            method,
            expectedStatus: Number(data.get("expectedStatus")),
            bodyContains: targetBody,
          }
        : {
            host: targetHost,
            ...(checkType === "tcp" ? { port: targetPort } : {}),
          };
    const body = {
      name: String(data.get("name")).trim(),
      checkType,
      timeZone,
      ...target,
      intervalMinutes: Number(data.get("intervalMinutes")),
      failureThreshold: Number(data.get("failureThreshold")),
      enabled: data.get("enabled") === "on",
      ...(requireReason
        ? { reason: String(data.get("reason") ?? "").trim() }
        : {}),
      ...(monitor ? { version: monitor.version } : {}),
    };
    if (
      checkType === "http" &&
      method === "GET" &&
      new TextEncoder().encode(targetBody).length > 1000
    ) {
      setError("Текст условия слишком длинный: максимум 1000 байт.");
      return;
    }
    if (checkType === "http") {
      try {
        const url = new URL(targetUrl);
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
    } else {
      const address = targetHost;
      if (address.includes(":") && !address.includes("://")) {
        setError(
          "IPv6 пока не поддерживается для TCP и ICMP. Укажите публичный IPv4 или домен с IPv4-адресом.",
        );
        return;
      }
      if (!address || /[\s/\\?#@:\[\]]/.test(address)) {
        setError(
          "Укажите домен или публичный IPv4 без протокола, пути и порта.",
        );
        return;
      }
      if (
        checkType === "tcp" &&
        (!Number.isInteger(targetPort) || targetPort < 1 || targetPort > 65535)
      ) {
        setError("Укажите TCP-порт от 1 до 65535.");
        return;
      }
    }
    submitting.current = true;
    setBusy(true);
    try {
      await api(
        `${basePath}${monitor ? `/${encodeURIComponent(monitor.id)}` : ""}`,
        { method: monitor ? "PATCH" : "POST", body, authenticated: true },
      );
      setSaved(true);
      notify.success(successMessage);
      try {
        await onSaved();
        onClose();
      } catch {
        setError(
          "Данные не обновились. Повторите обновление, чтобы увидеть сохранённый монитор.",
        );
      }
    } catch (e) {
      notify.error(errorMessage(e));
      if (e instanceof ApiError && e.code === "account_changed")
        await refresh().catch(() => undefined);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  return (
    <Dialog
      title={monitor ? "Настройки монитора" : "Новый монитор"}
      onClose={onClose}
      busy={busy}
    >
      {saved ? (
        <div className="form-stack">
          <p>Монитор сохранён.</p>
          {error && <Notice>{error}</Notice>}
          <div className="dialog-actions">
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={onClose}
            >
              Закрыть
            </button>
            <button
              type="button"
              className="primary"
              disabled={busy}
              onClick={() => void refreshSaved()}
            >
              Обновить данные
            </button>
          </div>
        </div>
      ) : (
        <>
          <p className="dialog-description">
            Один монитор проверяет одну цель выбранным способом. Настройте тип
            проверки и условия успеха.
          </p>
          <form className="form-stack" onSubmit={submit}>
            <label>
              Название
              <input
                name="name"
                autoFocus
                defaultValue={monitor?.name}
                required
                minLength={1}
                maxLength={100}
                placeholder="Основной сайт"
              />
            </label>
            <label>
              Тип проверки
              <select
                name="checkType"
                value={checkType}
                onChange={(event) =>
                  setCheckType(monitorCheckType(event.target.value))
                }
                aria-describedby="monitor-type-purpose"
              >
                <option value="http">Сайт или API · HTTP/HTTPS</option>
                <option value="tcp">Открытый порт · TCP</option>
                <option value="icmp">Пинг сервера · ICMP</option>
              </select>
            </label>
            <p id="monitor-type-purpose" className="field-hint">
              {checkType === "http"
                ? "Проверяет ответ сайта или API: код ответа и, при необходимости, текст страницы. Например: https://example.com/health, ожидаемый код 200."
                : checkType === "tcp"
                  ? "Проверяет, принимает ли сервер TCP-соединение на указанном порту. Например: server.example.com, порт 22. Открытый порт не подтверждает, что SSH или другое приложение работает правильно."
                  : "Проверяет, отвечает ли сервер на пинг, даже если на нём нет сайта. Укажите публичный IPv4 или домен с IPv4-адресом, например server.example.com. Сервер может блокировать ICMP и при этом работать — тогда выберите проверку нужного TCP-порта."}
            </p>
            {checkType === "http" ? (
              <>
                <label>
                  URL
                  <input
                    name="url"
                    type="url"
                    value={httpUrl}
                    onChange={(event) => setHttpUrl(event.target.value)}
                    required
                    maxLength={2048}
                    placeholder="https://example.com"
                  />
                  <span className="field-hint">
                    Публичный HTTP или HTTPS адрес. Без логина и пароля в URL.
                  </span>
                </label>
                <div className="form-row monitor-method-row">
                  <label>
                    Метод
                    <select
                      value={method}
                      onChange={(e) =>
                        setMethod(e.target.value as "GET" | "HEAD")
                      }
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
                      value={expectedStatus}
                      onChange={(event) =>
                        setExpectedStatus(event.target.value)
                      }
                      required
                      min={100}
                      max={599}
                    />
                  </label>
                </div>
                {method === "GET" && (
                  <label>
                    Текст в ответе{" "}
                    <span className="optional">необязательно</span>
                    <input
                      name="bodyContains"
                      value={bodyContains}
                      onChange={(event) => setBodyContains(event.target.value)}
                      maxLength={1000}
                      placeholder="Например: Welcome"
                    />
                    <span className="field-hint">
                      Успех, если в теле ответа присутствует этот текст.
                    </span>
                  </label>
                )}
              </>
            ) : (
              <div className={checkType === "tcp" ? "form-row" : undefined}>
                <label>
                  Хост
                  <input
                    name="host"
                    value={host}
                    onChange={(event) => setHost(event.target.value)}
                    required
                    maxLength={253}
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    placeholder="server.example.com"
                  />
                  <span className="field-hint">
                    Публичный IPv4 или домен. Без https://, пути и порта.
                  </span>
                </label>
                {checkType === "tcp" && (
                  <label>
                    TCP-порт
                    <input
                      name="port"
                      type="number"
                      value={port}
                      onChange={(event) => setPort(event.target.value)}
                      required
                      min={1}
                      max={65535}
                      step={1}
                      placeholder="22"
                    />
                  </label>
                )}
              </div>
            )}
            <TimeZoneSelect
              value={timeZone}
              onChange={setTimeZone}
              zones={timeZones}
              error={zoneError}
            />
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
              Первая успешная проверка закрывает инцидент. Неизвестный результат
              (UNKNOWN) сбрасывает серию сбоев.
            </p>
            {monitor && (
              <p className="field-hint">
                При изменении типа проверки, цели, интервала или порога открытый
                инцидент завершится с причиной «Настройки изменены», серия сбоев
                сбросится, а следующая проверка будет запланирована сразу.
              </p>
            )}
            <label className="checkbox-label">
              <input
                type="checkbox"
                name="enabled"
                defaultChecked={monitor?.enabled ?? true}
                disabled={!monitor?.enabled && !canEnable}
              />
              <span>
                Включить проверки
                <span className="field-hint">
                  {canEnable || monitor?.enabled
                    ? "Проверки выполняются при действующей квоте."
                    : "Все места заняты. Поставьте другой монитор на паузу, чтобы включить этот."}
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
            {error && <Notice>{error}</Notice>}
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
        </>
      )}
    </Dialog>
  );
}

type TimeZoneOption = { id: string; label: string };
function TimeZoneSelect({
  value,
  onChange,
  zones,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  zones: TimeZoneOption[];
  error: string;
}) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const options = zones.length
    ? zones
    : [
        {
          id: value,
          label: value === "Europe/Moscow" ? "Москва · Европа" : value,
        },
      ];
  const filtered = options.filter((zone) =>
    `${zone.label} ${zone.id}`
      .toLocaleLowerCase("ru")
      .includes(query.trim().toLocaleLowerCase("ru")),
  );
  const selected = options.find((zone) => zone.id === value);
  function close() {
    setOpen(false);
    trigger.current?.focus();
  }
  function select(zone: TimeZoneOption) {
    onChange(zone.id);
    close();
  }
  useEffect(() => {
    if (!open) return;
    search.current?.focus();
    const outside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  useEffect(() => {
    if (open)
      document
        .getElementById(`${id}-option-${active}`)
        ?.scrollIntoView?.({ block: "nearest" });
  }, [active, open, id]);
  return (
    <div className="monitor-timezone" ref={root}>
      <span id={`${id}-label`} className="monitor-timezone-label">
        Часовой пояс отчётов
      </span>
      <div className="monitor-timezone-control">
        <button
          type="button"
          ref={trigger}
          className="monitor-timezone-trigger"
          aria-labelledby={`${id}-label ${id}-value`}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={`${id}-list`}
          onClick={() => {
            setQuery("");
            setActive(0);
            setOpen(!open);
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              setOpen(true);
              setActive(0);
            }
          }}
        >
          <span id={`${id}-value`}>
            {selected?.label ?? value}
            <span className="field-hint">{value}</span>
          </span>
          <Icon name="chevron-down" />
        </button>
        {open && (
          <div className="monitor-timezone-popover">
            <input
              ref={search}
              type="search"
              role="combobox"
              aria-label="Поиск часового пояса по городу или IANA"
              aria-autocomplete="list"
              aria-expanded={true}
              aria-controls={`${id}-list`}
              aria-activedescendant={
                filtered[active] ? `${id}-option-${active}` : undefined
              }
              value={query}
              placeholder="Город или IANA: Москва, Berlin…"
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  event.stopPropagation();
                  close();
                } else if (
                  event.key === "ArrowDown" ||
                  event.key === "ArrowUp"
                ) {
                  event.preventDefault();
                  setActive((index) =>
                    Math.max(
                      0,
                      Math.min(
                        filtered.length - 1,
                        index + (event.key === "ArrowDown" ? 1 : -1),
                      ),
                    ),
                  );
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  if (filtered[active]) select(filtered[active]);
                } else if (event.key === "Tab") setOpen(false);
              }}
            />
            <div
              id={`${id}-list`}
              role="listbox"
              aria-labelledby={`${id}-label`}
              className="monitor-timezone-options"
            >
              {filtered.map((zone, index) => (
                <button
                  key={zone.id}
                  id={`${id}-option-${index}`}
                  type="button"
                  role="option"
                  aria-selected={zone.id === value}
                  tabIndex={-1}
                  className={active === index ? "is-active" : ""}
                  onPointerMove={() => setActive(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => select(zone)}
                >
                  {zone.label}
                  <span>{zone.id}</span>
                </button>
              ))}
              {filtered.length === 0 && (
                <p className="field-hint" role="status">
                  Ничего не найдено. Измените запрос.
                </p>
              )}
            </div>
          </div>
        )}
      </div>
      <p className="field-hint">
        Время событий учитывает переходы на летнее время. Длительность сбоя не
        меняется.
      </p>
      {error && <Notice>{error}</Notice>}
    </div>
  );
}
