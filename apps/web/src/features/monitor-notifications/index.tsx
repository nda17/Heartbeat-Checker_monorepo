"use client";
import { WebhookNotifications } from "./webhook";
import Link from "next/link";
import { FormEvent, useEffect, useState } from "react";
import { Monitor } from "@/entities/monitor";
import {
  Destination,
  MessengerRoute,
  Provider,
  destinationKindName,
  providerName,
} from "@/entities/messenger";
import { api, errorMessage } from "@/shared/api";
import { Dialog } from "@/shared/dialog";
import { Loading, Notice, Icon } from "@/shared/ui";

const reminderOptions = [
  [0, "Без напоминаний"],
  [15, "Через 15 минут"],
  [30, "Через 30 минут"],
  [60, "Через 1 час"],
  [180, "Через 3 часа"],
  [360, "Через 6 часов"],
  [1440, "Через 24 часа"],
] as const;

function RouteEditor({
  provider,
  monitor,
  destinations,
  route,
  onSaved,
}: {
  provider: Provider;
  monitor: Monitor;
  destinations: Destination[];
  route?: MessengerRoute;
  onSaved: (route: MessengerRoute) => void;
}) {
  const available = destinations.filter(
    (item) => item.provider === provider && item.ready,
  );
  const current = destinations.find(
    (item) => item.provider === provider && item.id === route?.destinationId,
  );
  const options =
    current && !current.ready ? [...available, current] : available;
  const selected =
    current?.id ?? (provider === "email" ? available[0]?.id : "") ?? "";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const destinationId = String(data.get("destinationId") ?? "");
    const enabled = data.get("enabled") === "on";
    if (
      !options.some((item) => item.id === destinationId) ||
      (enabled && !available.some((item) => item.id === destinationId))
    ) {
      setError(
        provider === "email"
          ? "Подключите подтверждённый email аккаунта."
          : "Выберите подтверждённый доступный чат.",
      );
      return;
    }
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await api<{ route: MessengerRoute }>(
        `/monitors/${encodeURIComponent(monitor.id)}/messenger-routes/${provider}`,
        {
          method: "PUT",
          authenticated: true,
          body: {
            destinationId,
            enabled,
            reminderMinutes: Number(data.get("reminderMinutes")),
            version: route?.version ?? 0,
          },
        },
      );
      onSaved(response.route);
      setSuccess("Настройки уведомлений сохранены.");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form
      className="messenger-route"
      onSubmit={save}
      key={`${provider}-${route?.version ?? 0}`}
    >
      <h3>{providerName[provider]}</h3>
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      {options.length ? (
        <>
          <label>
            {provider === "email" ? "Email" : "Чат"}
            <select name="destinationId" defaultValue={selected} required>
              <option value="" disabled>
                {provider === "email" ? "Выберите email" : "Выберите чат"}
              </option>
              {options.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label} · {destinationKindName[item.kind]}
                  {!item.ready && " · недоступен"}
                </option>
              ))}
            </select>
            {current && !current.ready && (
              <span className="field-hint">
                {provider === "email"
                  ? "Email отключён или больше не подтверждён. Подключите текущий адрес в настройках аккаунта."
                  : "Текущий чат недоступен. Уведомления можно выключить или выбрать другой доступный чат."}
              </span>
            )}
          </label>
          <label>
            Напоминание об открытом инциденте
            <select
              name="reminderMinutes"
              defaultValue={route?.reminderMinutes ?? 0}
            >
              {reminderOptions.map(([minutes, title]) => (
                <option key={minutes} value={minutes}>
                  {title}
                </option>
              ))}
            </select>
            <span className="field-hint">
              По умолчанию напоминания выключены.
            </span>
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              name="enabled"
              defaultChecked={route?.enabled ?? false}
            />
            <span>Отправлять уведомления об инцидентах и восстановлении</span>
          </label>
          <button className="primary" disabled={busy}>
            {busy ? "Сохраняем…" : "Сохранить"}
          </button>
        </>
      ) : (
        <p className="small muted">
          {provider === "email"
            ? "Сначала подключите email аккаунта. "
            : route
              ? "Выбранный чат отключён или недоступен. "
              : "Сначала подключите чат. "}
          <Link href="/app/settings">
            Перейти к настройкам аккаунта <Icon name="arrow-right" />
          </Link>
        </p>
      )}
    </form>
  );
}

export function MonitorNotifications({
  monitor,
  onClose,
}: {
  monitor: Monitor;
  onClose: () => void;
}) {
  const [destinations, setDestinations] = useState<Destination[] | null>(null);
  const [routes, setRoutes] = useState<MessengerRoute[] | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [emailEnabled, setEmailEnabled] = useState(false);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setError("");
    const timeout = window.setTimeout(() => {
      controller.abort();
      setError(
        "Загрузка настроек заняла слишком много времени. Повторите попытку.",
      );
    }, 10_000);
    Promise.all([
      api<{
        enabled?: boolean;
        emailEnabled?: boolean;
        destinations: Destination[];
      }>("/messenger-destinations", { signal: controller.signal }),
      api<{
        enabled?: boolean;
        emailEnabled?: boolean;
        routes: MessengerRoute[];
      }>(`/monitors/${encodeURIComponent(monitor.id)}/messenger-routes`, {
        signal: controller.signal,
      }),
    ])
      .then(([destinationResponse, routeResponse]) => {
        if (controller.signal.aborted) return;
        setEmailEnabled(
          destinationResponse.emailEnabled === true &&
            routeResponse.emailEnabled === true,
        );
        setEnabled(
          destinationResponse.enabled !== false &&
            routeResponse.enabled !== false,
        );
        setDestinations(destinationResponse.destinations ?? []);
        setRoutes(routeResponse.routes ?? []);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      })
      .finally(() => window.clearTimeout(timeout));
    return () => {
      controller.abort();
      window.clearTimeout(timeout);
    };
  }, [monitor.id, loadAttempt]);
  function updateRoute(route: MessengerRoute) {
    setRoutes((current) => [
      ...(current ?? []).filter((item) => item.provider !== route.provider),
      route,
    ]);
  }
  const providers = (["email", "telegram", "max"] as const).filter(
    (provider) => (provider === "email" ? emailEnabled : enabled),
  );
  return (
    <Dialog title={`Уведомления · ${monitor.name}`} wide onClose={onClose}>
      <p className="dialog-description">
        Выберите подтверждённого получателя для каждого канала. При выключенном
        канале сообщения не отправляются.
      </p>
      {error && <Notice>{error}</Notice>}
      {!destinations || !routes ? (
        error ? (
          <button
            className="secondary"
            onClick={() => setLoadAttempt((value) => value + 1)}
          >
            Повторить загрузку настроек
          </button>
        ) : (
          <Loading text="Загружаем настройки…" />
        )
      ) : !enabled && !emailEnabled ? (
        <Notice>Email, Telegram и MAX сейчас недоступны.</Notice>
      ) : (
        <div className="messenger-routes">
          {providers.map((provider) => (
            <RouteEditor
              key={provider}
              provider={provider}
              monitor={monitor}
              destinations={destinations}
              route={routes.find((item) => item.provider === provider)}
              onSaved={updateRoute}
            />
          ))}
        </div>
      )}
      <WebhookNotifications monitorId={monitor.id} />
    </Dialog>
  );
}
