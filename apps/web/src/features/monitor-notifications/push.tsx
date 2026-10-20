"use client";

import Link from "next/link";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import type { PushInstallation, PushMonitorSettings } from "@/entities/push";
import { api, ApiError, boundedRequest, errorMessage } from "@/shared/api";
import { notify } from "@/shared/notifications";
import { Icon, Loading, Notice } from "@/shared/ui";

export function AndroidPushRoutes({ monitorId }: { monitorId: string }) {
  const { profile } = useSession();
  return (
    <PushSettings
      key={`${profile.account.id}:${monitorId}`}
      monitorId={monitorId}
    />
  );
}

function PushSettings({ monitorId }: { monitorId: string }) {
  const [settings, setSettings] = useState<PushMonitorSettings | null>(null);
  const [available, setAvailable] = useState(false);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    void boundedRequest(controller, async (signal) => {
      const [devices, result] = await Promise.all([
        api<{ enabled: boolean; installations: PushInstallation[] }>(
          "/push/installations",
          { signal },
        ),
        api<PushMonitorSettings>(
          `/monitors/${encodeURIComponent(monitorId)}/push`,
          { signal },
        ),
      ]);
      if (!alive || signal.aborted) return;
      setSettings(result);
      setAvailable(devices.enabled);
      setConnected(
        devices.installations.some((item) => item.status === "ready"),
      );
      setError("");
    })
      .catch((cause) => {
        if (!alive) return;
        setError(
          controller.signal.aborted
            ? "Загрузка Push заняла слишком много времени. Повторите загрузку."
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
        <Icon name="phone" /> Push
      </h3>
      <p className="small muted">
        Уведомления о сбое и восстановлении приходят в установленное{" "}
        <Link href="/android">Android-приложение Heartbeat Checker</Link>. На
        телефоне нужно войти в этот аккаунт и разрешить уведомления.
      </p>
      {error && <Notice>{error}</Notice>}
      {loading ? (
        <Loading text="Загружаем настройки Push…" />
      ) : error ? (
        <button
          type="button"
          className="secondary"
          onClick={() => setAttempt((value) => value + 1)}
        >
          Повторить загрузку Push
        </button>
      ) : (
        settings && (
          <>
            {!available && (
              <Notice>Push сейчас недоступен. Ваш выбор сохранён.</Notice>
            )}
            <PushSettingsEditor
              key={settings.version}
              monitorId={monitorId}
              settings={settings}
              available={available}
              onSaved={setSettings}
              onReload={() => setAttempt((value) => value + 1)}
            />
            {!connected && (
              <p className="small muted">
                <Link href="/app/settings">
                  Подключённые телефоны <Icon name="arrow-right" />
                </Link>
              </p>
            )}
            <p className="small muted">
              После подключения телефона Push включается автоматически. Если
              выключить его здесь, выбор сохранится после повторного входа и
              подключения других телефонов.
            </p>
          </>
        )
      )}
    </section>
  );
}

function PushSettingsEditor({
  monitorId,
  settings,
  available,
  onSaved,
  onReload,
}: {
  monitorId: string;
  settings: PushMonitorSettings;
  available: boolean;
  onSaved: (settings: PushMonitorSettings) => void;
  onReload: () => void;
}) {
  const [enabled, setEnabled] = useState(settings.enabled);
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

  async function save(event: FormEvent) {
    event.preventDefault();
    if (request.current || stale || !available) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const result = await boundedRequest(controller, (signal) =>
        api<PushMonitorSettings>(
          `/monitors/${encodeURIComponent(monitorId)}/push`,
          {
            method: "PUT",
            authenticated: true,
            signal,
            body: { enabled, version: settings.version },
          },
        ),
      );
      if (request.current !== controller || controller.signal.aborted) return;
      onSaved(result);
      notify.success("Настройки Push сохранены.");
    } catch (cause) {
      if (request.current !== controller) return;
      setStale(true);
      setError(
        cause instanceof ApiError && cause.status === 409
          ? "Настройки уже изменены. Загрузите актуальное состояние и сохраните выбор заново."
          : cause instanceof ApiError && cause.status === 404
            ? "Монитор больше недоступен. Обновите настройки."
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
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={enabled}
          disabled={busy || stale || !available}
          onChange={(event) => setEnabled(event.target.checked)}
        />
        <span>Push в приложении</span>
      </label>
      <p className="small muted">
        Настройка действует на все подключённые телефоны. Напоминания не
        отправляются.
      </p>
      {error && <Notice>{error}</Notice>}
      {stale ? (
        <button type="button" className="secondary" onClick={onReload}>
          Обновить настройки Push
        </button>
      ) : (
        <button
          className="secondary"
          disabled={
            busy ||
            !available ||
            (settings.version > 0 && enabled === settings.enabled)
          }
        >
          {busy ? "Сохраняем…" : "Сохранить"}
        </button>
      )}
    </form>
  );
}
