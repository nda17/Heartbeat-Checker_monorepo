"use client";

import Link from "next/link";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import type { PushInstallation, PushRoute } from "@/entities/push";
import { pushStatusLabels } from "@/entities/push";
import { api, ApiError, boundedRequest, errorMessage } from "@/shared/api";
import { notify } from "@/shared/notifications";
import { Icon, Loading, Notice } from "@/shared/ui";

export function AndroidPushRoutes({ monitorId }: { monitorId: string }) {
  const { profile } = useSession();
  return (
    <PushRoutes
      key={`${profile.account.id}:${monitorId}`}
      monitorId={monitorId}
    />
  );
}

function PushRoutes({ monitorId }: { monitorId: string }) {
  const [installations, setInstallations] = useState<PushInstallation[] | null>(
    null,
  );
  const [routes, setRoutes] = useState<PushRoute[] | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    void boundedRequest(controller, async (signal) => {
      const [devices, settings] = await Promise.all([
        api<{ enabled: boolean; installations: PushInstallation[] }>(
          "/push/installations",
          { signal },
        ),
        api<{ routes: PushRoute[] }>(
          `/monitors/${encodeURIComponent(monitorId)}/push-routes`,
          { signal },
        ),
      ]);
      if (!alive || signal.aborted) return;
      setInstallations(devices.installations);
      setEnabled(devices.enabled);
      setRoutes(settings.routes);
      setError("");
    })
      .catch((cause) => {
        if (!alive) return;
        setError(
          controller.signal.aborted
            ? "Загрузка Android push заняла слишком много времени. Повторите загрузку."
            : errorMessage(cause),
        );
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [monitorId, attempt]);

  return (
    <section className="messenger-route android-push-routes">
      <h3>
        <Icon name="phone" /> Android push
      </h3>
      <p className="small muted">
        Включите уведомления об инцидентах и восстановлении отдельно для каждого
        телефона. По умолчанию они выключены. Напоминания не отправляются.
      </p>
      {error && <Notice>{error}</Notice>}
      {loading ? (
        <Loading text="Загружаем телефоны…" />
      ) : error ? (
        <button
          type="button"
          className="secondary"
          onClick={() => setAttempt((value) => value + 1)}
        >
          Повторить загрузку Android push
        </button>
      ) : (
        <>
          {!enabled && <Notice>Android push сейчас недоступен.</Notice>}
          {!installations?.length ? (
            <p className="small muted">
              Сначала{" "}
              <Link href="/app/settings">
                подключите Android-телефон в настройках аккаунта{" "}
                <Icon name="arrow-right" />
              </Link>
              .
            </p>
          ) : (
            installations.map((installation) => {
              const route = routes?.find(
                (item) => item.installationId === installation.id,
              );
              return (
                <PushRouteEditor
                  key={`${installation.id}:${installation.bindingGeneration}:${installation.status}:${route?.version ?? 0}:${route?.ready ?? false}`}
                  monitorId={monitorId}
                  installation={installation}
                  route={route}
                  available={enabled}
                  onSaved={(saved) =>
                    setRoutes((current) => [
                      ...(current ?? []).filter(
                        (item) => item.installationId !== saved.installationId,
                      ),
                      saved,
                    ])
                  }
                  onReload={() => setAttempt((value) => value + 1)}
                />
              );
            })
          )}
          <p className="small muted">
            Готовность подключения не подтверждает, что телефон сейчас в сети.
            Разрешения Android, сеть и RuStore Push могут влиять на доставку.
          </p>
        </>
      )}
    </section>
  );
}

function PushRouteEditor({
  monitorId,
  installation,
  route,
  available,
  onSaved,
  onReload,
}: {
  monitorId: string;
  installation: PushInstallation;
  route?: PushRoute;
  available: boolean;
  onSaved: (route: PushRoute) => void;
  onReload: () => void;
}) {
  const [enabled, setEnabled] = useState(route?.enabled ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      const controller = request.current;
      request.current = null;
      controller?.abort();
    },
    [],
  );
  const ready =
    available && installation.status === "ready" && (route?.ready ?? true);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (request.current || stale || (enabled && !ready)) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const result = await boundedRequest(controller, (signal) =>
        api<{ route: PushRoute }>(
          `/monitors/${encodeURIComponent(monitorId)}/push-routes/${encodeURIComponent(installation.id)}`,
          {
            method: "PUT",
            authenticated: true,
            signal,
            body: { enabled, version: route?.version ?? 0 },
          },
        ),
      );
      if (request.current !== controller || controller.signal.aborted) return;
      onSaved(result.route);
      notify.success("Настройки Android push сохранены.");
    } catch (cause) {
      if (request.current !== controller) return;
      setStale(true);
      setError(
        cause instanceof ApiError &&
          ["session_expired", "session_revoked"].includes(cause.code)
          ? "Подключение телефона истекло или отозвано. Подключите телефон заново в Android-приложении и обновите настройки."
          : cause instanceof ApiError && cause.status === 409
            ? "Настройки уже изменены. Загрузите актуальное состояние, проверьте его и сохраните выбор заново."
            : cause instanceof ApiError && cause.status === 404
              ? "Телефон или монитор больше недоступен. Обновите настройки."
              : controller.signal.aborted ||
                  !(cause instanceof ApiError) ||
                  cause.status === 0 ||
                  cause.status >= 500
                ? "Ответ на сохранение не получен. Обновите настройки перед повтором; запрос не повторяется автоматически."
                : errorMessage(cause),
      );
    } finally {
      if (request.current === controller) {
        request.current = null;
        setBusy(false);
      }
    }
  }
  return (
    <form className="push-route-editor form-stack" onSubmit={save}>
      <strong>{installation.deviceLabel}</strong>
      <p className="small muted">{pushStatusLabels[installation.status]}</p>
      {!ready && (
        <p className="small muted">
          Для включения проверьте подключение и разрешения в Android-приложении.
          Уже включённый маршрут можно выключить.
        </p>
      )}
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={enabled}
          disabled={busy || stale || (!ready && !enabled)}
          onChange={(event) => setEnabled(event.target.checked)}
        />
        <span>Отправлять на этот телефон</span>
      </label>
      {error && <Notice>{error}</Notice>}
      {stale ? (
        <button type="button" className="secondary" onClick={onReload}>
          Обновить настройки Android push
        </button>
      ) : (
        <button
          className="secondary"
          disabled={
            busy || (enabled && !ready) || enabled === (route?.enabled ?? false)
          }
        >
          {busy ? "Сохраняем…" : "Сохранить"}
        </button>
      )}
    </form>
  );
}
