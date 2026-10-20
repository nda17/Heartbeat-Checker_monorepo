"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import type {
  PushInstallation,
  PushPairing,
  PushPairingConfirmation,
} from "@/entities/push";
import { pushStatusLabels } from "@/entities/push";
import { api, ApiError, boundedRequest, errorMessage } from "@/shared/api";
import { androidPushIntent, pushPairingId } from "@/shared/android";
import { notify } from "@/shared/notifications";
import { dateTime, Icon, Loading, Notice } from "@/shared/ui";

export function AndroidPushSettings() {
  const { profile } = useSession();
  return <PushSettings key={profile.account.id} />;
}

function PushSettings() {
  const { profile } = useSession();
  const [installations, setInstallations] = useState<PushInstallation[] | null>(
    null,
  );
  const [enabled, setEnabled] = useState(false);
  const [android, setAndroid] = useState(false);
  const [pairingId, setPairingId] = useState<string | null>(null);
  const [invalidPairing, setInvalidPairing] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [disconnect, setDisconnect] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [uncertain, setUncertain] = useState(false);
  const mutation = useRef<AbortController | null>(null);
  const read = useRef<AbortController | null>(null);
  const sequence = useRef(0);

  const load = useCallback(async (signal: AbortSignal, request: number) => {
    const result = await api<{
      enabled: boolean;
      installations: PushInstallation[];
    }>("/push/installations", { signal });
    if (signal.aborted || sequence.current !== request) return;
    setInstallations(result.installations);
    setEnabled(result.enabled);
    setUncertain(false);
  }, []);

  useEffect(() => {
    // Capability affects presentation only. Pairing authorization uses the code,
    // PKCE and local native state, never the user-agent or public query id.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAndroid(/Android/i.test(navigator.userAgent));
    const params = new URLSearchParams(window.location.search);
    const id = pushPairingId(params);
    setPairingId(id);
    setInvalidPairing(params.has("pushPair") && !id);
    return () => {
      const controller = mutation.current;
      mutation.current = null;
      controller?.abort();
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    read.current = controller;
    const request = ++sequence.current;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setError("");
    void boundedRequest(controller, (signal) => load(signal, request)).catch(
      (cause) => {
        if (read.current === controller)
          setError(
            controller.signal.aborted
              ? "Загрузка телефонов заняла слишком много времени. Повторите загрузку."
              : errorMessage(cause),
          );
      },
    );
    return () => {
      read.current = null;
      controller.abort();
    };
  }, [attempt, load]);

  async function disconnectPhone(id: string) {
    if (mutation.current || uncertain) return;
    const controller = new AbortController();
    mutation.current = controller;
    const previousRead = read.current;
    read.current = null;
    previousRead?.abort();
    ++sequence.current;
    setBusy(id);
    setError("");
    try {
      await boundedRequest(controller, (signal) =>
        api(`/push/installations/${encodeURIComponent(id)}`, {
          method: "DELETE",
          authenticated: true,
          signal,
        }),
      );
      if (mutation.current !== controller || controller.signal.aborted) return;
      setInstallations(
        (current) => current?.filter((item) => item.id !== id) ?? null,
      );
      setDisconnect(null);
      notify.success("Телефон отключён от уведомлений.");
      setAttempt((value) => value + 1);
    } catch (cause) {
      if (mutation.current !== controller) return;
      const ambiguous =
        controller.signal.aborted ||
        !(cause instanceof ApiError) ||
        cause.status === 0 ||
        cause.status >= 500;
      if (ambiguous) setUncertain(true);
      setError(
        ambiguous
          ? "Ответ на отключение не получен. Обновите состояние перед повтором; запрос не повторяется автоматически."
          : cause instanceof ApiError && cause.status === 404
            ? "Телефон уже отключён или недоступен. Обновите состояние."
            : errorMessage(cause),
      );
    } finally {
      if (mutation.current === controller) {
        mutation.current = null;
        setBusy("");
      }
    }
  }

  return (
    <section className="panel android-push-panel">
      <div className="panel-heading">
        <span className="device-icon">
          <Icon name="phone" />
        </span>
        <div>
          <h2>Push на телефоны</h2>
          <p>Уведомления об инцидентах и восстановлении.</p>
        </div>
      </div>
      {invalidPairing && (
        <Notice>
          Ссылка подключения некорректна. Откройте Android-приложение заново.
        </Notice>
      )}
      {pairingId && (
        <PairingConnection
          key={pairingId}
          pairingId={pairingId}
          accountId={profile.account.id}
          accountName={profile.account.name}
          email={profile.email}
        />
      )}
      {error && <Notice>{error}</Notice>}
      <div className="push-actions">
        {android && enabled && (
          <a className="secondary" href={androidPushIntent("connect")}>
            Настроить этот телефон <Icon name="arrow-right" />
          </a>
        )}
        <button
          type="button"
          className="quiet"
          disabled={!!busy}
          onClick={() => setAttempt((value) => value + 1)}
        >
          Обновить состояние
        </button>
      </div>
      <p className="small muted">
        Войдите в{" "}
        <Link href="/android">Android-приложение Heartbeat Checker</Link> и
        разрешите уведомления — телефон подключится автоматически. Если вы
        отключили телефон, включите уведомления в приложении.
      </p>
      {installations === null ? (
        !error && <Loading text="Загружаем телефоны…" />
      ) : (
        <>
          {!enabled && (
            <Notice>
              Push сейчас недоступен. Настройки подключённых телефонов
              сохранены.
            </Notice>
          )}
          {!installations.length ? (
            <p className="small muted">Подключённых телефонов пока нет.</p>
          ) : (
            <ul className="session-list push-installations">
              {installations.map((item) => (
                <li key={item.id}>
                  <span className="device-icon">
                    <Icon name="phone" />
                  </span>
                  <div className="session-info">
                    <strong>{item.deviceLabel}</strong>
                    <p className="small">{pushStatusLabels[item.status]}</p>
                    <p className="small muted">
                      Последнее обращение приложения:{" "}
                      {dateTime(item.lastSeenAt)}
                    </p>
                    {[
                      "session_expired",
                      "session_revoked",
                      "invalid_token",
                      "permission_denied",
                    ].includes(item.status) && (
                      <p className="small muted">
                        Откройте приложение на этом телефоне и проверьте
                        разрешение на уведомления.
                        {android && enabled && (
                          <>
                            {" "}
                            <a href={androidPushIntent("connect")}>
                              Настроить текущий телефон
                            </a>
                          </>
                        )}
                      </p>
                    )}
                    {disconnect === item.id && (
                      <div className="push-disconnect form-stack">
                        <p className="small">
                          Отключить {item.deviceLabel}? Уведомления для этого
                          телефона будут выключены на всех мониторах.
                        </p>
                        <div className="push-actions">
                          <button
                            className="secondary danger-text"
                            disabled={!!busy || uncertain}
                            onClick={() => void disconnectPhone(item.id)}
                          >
                            {busy === item.id ? "Отключаем…" : "Да, отключить"}
                          </button>
                          <button
                            className="quiet"
                            disabled={!!busy}
                            onClick={() => setDisconnect(null)}
                          >
                            Отмена
                          </button>
                        </div>
                      </div>
                    )}
                  </div>
                  {disconnect !== item.id && (
                    <button
                      className="quiet danger-text"
                      disabled={!!busy || uncertain}
                      onClick={() => setDisconnect(item.id)}
                    >
                      Отключить
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <p className="small muted push-explanation">
        Push включается по умолчанию после первого подключения. Переключатель
        Push у монитора действует на все текущие и будущие телефоны; явно
        выключенный монитор остаётся выключенным. Доставка зависит от разрешений
        Android, сети и RuStore Push. Напоминания не отправляются.
      </p>
    </section>
  );
}

function PairingConnection({
  pairingId,
  accountId,
  accountName,
  email,
}: {
  pairingId: string;
  accountId: string;
  accountName: string;
  email: string;
}) {
  const [pairing, setPairing] = useState<PushPairing | null>(null);
  const [completion, setCompletion] = useState<PushPairingConfirmation | null>(
    null,
  );
  const [error, setError] = useState("");
  const [restart, setRestart] = useState(false);
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  const [now, setNow] = useState(0);
  const panel = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    let issued = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    setNow(Date.now());
    panel.current?.scrollIntoView?.({ block: "start" });
    panel.current?.focus({ preventScroll: true });
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    void boundedRequest(controller, async (signal) => {
      const result = await api<PushPairing>(`/push/pairings/${pairingId}`, {
        signal,
      });
      if (!alive || signal.aborted) return;
      if (
        result.id !== pairingId ||
        !["pending", "confirmed"].includes(result.status) ||
        !Number.isFinite(Date.parse(result.expiresAt))
      )
        throw new Error("Invalid pairing response");
      setPairing(result);
      if (Date.parse(result.expiresAt) <= Date.now()) {
        setRestart(true);
        throw new Error("Срок подключения истёк. Откройте приложение заново.");
      }
      // Persist only a non-secret attempt marker. A remount or lost response
      // must not issue the one-time code twice; the code itself stays in memory.
      const marker = `heartbeat.push.confirm:${accountId}:${pairingId}`;
      if (
        result.status !== "pending" ||
        window.sessionStorage.getItem(marker)
      ) {
        setRestart(true);
        throw new Error(
          "Подтверждение уже запрошено. Если возврат в приложение недоступен, начните подключение заново.",
        );
      }
      window.sessionStorage.setItem(marker, "attempted");
      issued = true;
      const confirmed = await api<PushPairingConfirmation>(
        `/push/pairings/${pairingId}/confirm`,
        {
          method: "POST",
          body: {},
          authenticated: true,
          signal,
        },
      );
      if (!alive || signal.aborted) return;
      if (
        confirmed.pairingId !== pairingId ||
        Date.parse(confirmed.expiresAt) !== Date.parse(result.expiresAt) ||
        Date.parse(confirmed.expiresAt) <= Date.now()
      )
        throw new Error("Invalid pairing confirmation");
      const intent = androidPushIntent("complete", confirmed);
      setCompletion(confirmed);
      setError("");
      panel.current?.scrollIntoView?.({ block: "start" });
      panel.current?.focus({ preventScroll: true });
      // Browsers may require a gesture. The same explicit Return link remains
      // available below; neither confirmation nor navigation runs in a loop.
      try {
        window.location.assign(intent);
      } catch {
        /* Manual Return remains available. */
      }
    })
      .catch((cause) => {
        if (!alive) return;
        if (issued || (cause instanceof ApiError && cause.status === 404))
          setRestart(true);
        setError(
          issued
            ? "Ответ на подключение не получен. Начните подключение в приложении заново; подтверждение не повторяется автоматически."
            : cause instanceof ApiError && cause.status === 404
              ? "Подключение не найдено или срок ссылки истёк. Откройте приложение заново."
              : controller.signal.aborted
                ? "Не удалось загрузить подключение вовремя. Повторите загрузку."
                : errorMessage(cause),
        );
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [accountId, pairingId, attempt]);

  const expired = completion && Date.parse(completion.expiresAt) <= now;
  return (
    <div
      className="push-pairing form-stack"
      ref={panel}
      tabIndex={-1}
      aria-label="Подключение телефона"
    >
      <h3>Подключаем телефон</h3>
      <p className="small">
        Аккаунт: <strong>{accountName}</strong> · {email}
      </p>
      {pairing && (
        <p className="small">
          Телефон: <strong>{pairing.deviceLabel}</strong>
        </p>
      )}
      {loading ? (
        <Loading text="Подключаем приложение…" />
      ) : completion && !expired ? (
        <>
          <p className="small" role="status">
            Продолжите в приложении и разрешите уведомления. Если приложение не
            открылось автоматически, нажмите кнопку.
          </p>
          <a
            className="primary"
            href={androidPushIntent("complete", completion)}
          >
            Вернуться в приложение <Icon name="arrow-right" />
          </a>
        </>
      ) : (
        <>
          {error && <Notice>{error}</Notice>}
          {expired && (
            <Notice>
              Срок подключения истёк. Начните подключение в приложении заново.
            </Notice>
          )}
          {restart || expired ? (
            <a className="secondary" href={androidPushIntent("connect")}>
              Начать подключение заново
            </a>
          ) : (
            <button
              type="button"
              className="secondary"
              onClick={() => setAttempt((value) => value + 1)}
            >
              Повторить загрузку подключения
            </button>
          )}
        </>
      )}
    </div>
  );
}
