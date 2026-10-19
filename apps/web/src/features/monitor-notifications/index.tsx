"use client";
import { WebhookNotifications } from "./webhook";
import Link from "next/link";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Profile, useSession } from "@/entities/session";
import { Monitor } from "@/entities/monitor";
import {
  Destination,
  EmailDestinationEditor,
  MessengerRoute,
  Provider,
  destinationKindName,
  providerName,
} from "@/entities/messenger";
import { api, boundedRequest, errorMessage } from "@/shared/api";
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
  embedded = false,
  disabled = false,
}: {
  provider: Provider;
  monitor: Monitor;
  destinations: Destination[];
  route?: MessengerRoute;
  onSaved: (route: MessengerRoute) => void;
  embedded?: boolean;
  disabled?: boolean;
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
  const saveRequest = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      const controller = saveRequest.current;
      saveRequest.current = null;
      controller?.abort();
    },
    [monitor.id, provider],
  );

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (disabled || busy || saveRequest.current) return;
    const data = new FormData(event.currentTarget);
    const destinationId = String(data.get("destinationId") ?? "");
    const enabled = data.get("enabled") === "on";
    if (
      !options.some((item) => item.id === destinationId) ||
      (enabled && !available.some((item) => item.id === destinationId))
    ) {
      setError(
        provider === "email"
          ? "Подключите подтверждённый адрес уведомлений."
          : "Выберите подтверждённый доступный чат.",
      );
      return;
    }
    const controller = new AbortController();
    saveRequest.current = controller;
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await boundedRequest(controller, (signal) =>
        api<{ route: MessengerRoute }>(
          `/monitors/${encodeURIComponent(monitor.id)}/messenger-routes/${provider}`,
          {
            method: "PUT",
            authenticated: true,
            signal,
            body: {
              destinationId,
              enabled,
              reminderMinutes: Number(data.get("reminderMinutes")),
              version: route?.version ?? 0,
            },
          },
        ),
      );
      if (saveRequest.current !== controller || controller.signal.aborted)
        return;
      onSaved(response.route);
      setSuccess("Настройки уведомлений сохранены.");
    } catch (e) {
      if (saveRequest.current === controller)
        setError(
          controller.signal.aborted
            ? "Ответ на сохранение не получен. Обновите настройки перед повтором; запрос не повторяется автоматически."
            : errorMessage(e),
        );
    } finally {
      if (saveRequest.current === controller) {
        saveRequest.current = null;
        setBusy(false);
      }
    }
  }
  return (
    <form
      className={embedded ? "form-stack" : "messenger-route"}
      onSubmit={save}
    >
      {!embedded && <h3>{providerName[provider]}</h3>}
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      {options.length ? (
        <>
          <label>
            {provider === "email" ? "Email" : "Чат"}
            <select
              name="destinationId"
              defaultValue={selected}
              required
              disabled={disabled}
            >
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
                  ? "Адрес недоступен. Подключите или подтвердите адрес уведомлений выше."
                  : "Текущий чат недоступен. Уведомления можно выключить или выбрать другой доступный чат."}
              </span>
            )}
          </label>
          <label>
            Напоминание об открытом инциденте
            <select
              name="reminderMinutes"
              disabled={disabled}
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
              disabled={disabled}
              defaultChecked={route?.enabled ?? false}
            />
            <span>Отправлять уведомления об инцидентах и восстановлении</span>
          </label>
          <button className="primary" disabled={busy || disabled}>
            {busy ? "Сохраняем…" : "Сохранить"}
          </button>
        </>
      ) : (
        <p className="small muted">
          {provider === "email"
            ? "После подключения email здесь появятся настройки этого монитора. "
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
  const { profile } = useSession();
  const [emailVerified, setEmailVerified] = useState(
    profile.emailVerified === true,
  );
  const [emailRouteRevision, setEmailRouteRevision] = useState(0);
  const [emailRoutePending, setEmailRoutePending] = useState(false);
  const resetEmailRoute = useRef(false);
  const knownEmail = useRef<Destination | undefined>(undefined);
  const read = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const mutation = useRef(false);
  const loadedMonitor = useRef<string | null>(null);
  const [destinations, setDestinations] = useState<Destination[] | null>(null);
  const [routes, setRoutes] = useState<MessengerRoute[] | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [emailEnabled, setEmailEnabled] = useState(false);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [error, setError] = useState("");
  const [profileError, setProfileError] = useState("");
  useEffect(() => {
    let alive = true;
    let timeout: number | undefined;
    async function load() {
      if (
        !alive ||
        read.current ||
        mutation.current ||
        document.visibilityState === "hidden"
      )
        return;
      const controller = new AbortController();
      read.current = controller;
      const view = ++generation.current;
      timeout = window.setTimeout(() => {
        if (read.current !== controller) return;
        read.current = null;
        controller.abort();
        if (alive && generation.current === view)
          setError(
            "Загрузка настроек заняла слишком много времени. Повторите попытку.",
          );
      }, 10_000);
      const timer = timeout;
      // Profile ownership and destination connection are independent explicit steps.
      const profileRead = api<Profile>("/me", { signal: controller.signal })
        .then((current) => {
          if (
            alive &&
            !controller.signal.aborted &&
            generation.current === view
          ) {
            setEmailVerified(current.emailVerified === true);
            setProfileError("");
          }
        })
        .catch((cause) => {
          if (
            alive &&
            !controller.signal.aborted &&
            generation.current === view
          )
            setProfileError(errorMessage(cause));
        });
      try {
        const [destinationResponse, routeResponse] = await Promise.all([
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
        ]);
        if (!alive || controller.signal.aborted || generation.current !== view)
          return;
        setEmailEnabled(
          destinationResponse.emailEnabled === true &&
            routeResponse.emailEnabled === true,
        );
        setEnabled(
          destinationResponse.enabled !== false &&
            routeResponse.enabled !== false,
        );
        const nextEmail = destinationResponse.destinations.find(
          (item) => item.provider === "email",
        );
        const previousEmail = knownEmail.current;
        const emailChanged =
          resetEmailRoute.current ||
          (!!previousEmail &&
            (!nextEmail ||
              previousEmail.id !== nextEmail.id ||
              previousEmail.version !== nextEmail.version));
        knownEmail.current = nextEmail;
        resetEmailRoute.current = false;
        if (emailChanged) {
          setEmailRouteRevision((value) => value + 1);
          setEmailRoutePending(false);
        }
        setDestinations(destinationResponse.destinations ?? []);
        const preserve = loadedMonitor.current === monitor.id;
        loadedMonitor.current = monitor.id;
        setRoutes((current) =>
          !preserve || current === null
            ? (routeResponse.routes ?? [])
            : emailChanged
              ? [
                  ...current.filter((item) => item.provider !== "email"),
                  ...(routeResponse.routes ?? []).filter(
                    (item) => item.provider === "email",
                  ),
                ]
              : current,
        );
        setError("");
        await profileRead;
      } catch (cause) {
        if (alive && !controller.signal.aborted && generation.current === view)
          setError(errorMessage(cause));
        controller.abort();
      } finally {
        window.clearTimeout(timer);
        if (read.current === controller) read.current = null;
      }
    }
    const update = () => {
      void load();
    };
    update();
    window.addEventListener("focus", update);
    window.addEventListener("pageshow", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      alive = false;
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate whichever request currently owns this modal.
      ++generation.current;
      read.current?.abort();
      read.current = null;
      window.clearTimeout(timeout);
      window.removeEventListener("focus", update);
      window.removeEventListener("pageshow", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, [monitor.id, loadAttempt, profile.emailVerified]);
  const destinationBusy = useCallback((value: boolean) => {
    mutation.current = value;
    if (value) {
      ++generation.current;
      read.current?.abort();
      read.current = null;
    } else setLoadAttempt((current) => current + 1);
  }, []);
  function connected(destination: Destination, resetRoutes: boolean) {
    setDestinations((current) => [
      ...(current ?? []).filter((item) => item.provider !== "email"),
      destination,
    ]);
    if (resetRoutes) {
      resetEmailRoute.current = true;
      setEmailRoutePending(true);
      setRoutes(
        (current) =>
          current?.map((route) =>
            route.provider === "email" ? { ...route, enabled: false } : route,
          ) ?? null,
      );
      setEmailRouteRevision((value) => value + 1);
    }
    setLoadAttempt((value) => value + 1);
  }
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
      {profileError && (
        <Notice>Не удалось обновить состояние email: {profileError}</Notice>
      )}
      {!!destinations && !!routes && (error || profileError) && (
        <button
          type="button"
          className="quiet"
          onClick={() => setLoadAttempt((value) => value + 1)}
        >
          Обновить настройки
        </button>
      )}
      {emailRoutePending && (
        <p className="small" role="status">
          После смены адреса уведомления выключены. Обновляем настройки перед
          повторным включением.
        </p>
      )}
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
          {providers.map((provider) =>
            provider === "email" ? (
              <section key={`${monitor.id}-email`} className="messenger-route">
                <h3>Email</h3>
                <EmailDestinationEditor
                  current={destinations.find(
                    (item) => item.provider === "email",
                  )}
                  accountEmail={profile.email}
                  accountEmailVerified={emailVerified}
                  onConnected={connected}
                  onBusyChange={destinationBusy}
                />
                <RouteEditor
                  key={`email-route-${emailRouteRevision}`}
                  embedded
                  disabled={emailRoutePending}
                  provider="email"
                  monitor={monitor}
                  destinations={destinations}
                  route={routes.find((item) => item.provider === "email")}
                  onSaved={updateRoute}
                />
              </section>
            ) : (
              <RouteEditor
                key={`${monitor.id}-${provider}`}
                provider={provider}
                monitor={monitor}
                destinations={destinations}
                route={routes.find((item) => item.provider === provider)}
                onSaved={updateRoute}
              />
            ),
          )}
        </div>
      )}
      <WebhookNotifications monitorId={monitor.id} />
    </Dialog>
  );
}
