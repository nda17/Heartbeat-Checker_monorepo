"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import type { PushInstallation, PushPairing } from "@/entities/push";
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
  const [pairing, setPairing] = useState<PushPairing | null>(null);
  const [pairingLoading, setPairingLoading] = useState(false);
  const [pairingError, setPairingError] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [disconnect, setDisconnect] = useState<string | null>(null);
  const [compared, setCompared] = useState(false);
  const [now, setNow] = useState(0);
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
    // Browser-only capability and public pairing id. Viewing never confirms a pairing.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setAndroid(/Android/i.test(navigator.userAgent));
    const params = new URLSearchParams(window.location.search);
    const id = pushPairingId(params);
    setPairingId(id);
    if (params.has("pushPair") && !id)
      setPairingError(
        "Ссылка подключения некорректна. Начните подключение в Android-приложении заново.",
      );
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      window.clearInterval(timer);
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

  useEffect(() => {
    if (!pairingId) return;
    const controller = new AbortController();
    let alive = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setPairingLoading(true);
    void boundedRequest(controller, (signal) =>
      api<PushPairing>(`/push/pairings/${pairingId}`, { signal }),
    )
      .then((result) => {
        if (!alive || controller.signal.aborted) return;
        if (
          result.id !== pairingId ||
          typeof result.verificationCode !== "string" ||
          result.verificationCode.length !== 6 ||
          !/^\d{6}$/.test(result.verificationCode) ||
          !["pending", "confirmed"].includes(result.status) ||
          !Number.isFinite(Date.parse(result.expiresAt))
        )
          throw new Error("Invalid pairing response");
        setPairing(result);
        setPairingError("");
      })
      .catch((cause) => {
        if (!alive) return;
        setPairingError(
          cause instanceof ApiError && cause.status === 404
            ? "Подключение не найдено или срок ссылки истёк. Начните подключение в приложении заново."
            : controller.signal.aborted
              ? "Не удалось загрузить подключение вовремя. Повторите загрузку."
              : errorMessage(cause),
        );
      })
      .finally(() => {
        if (alive) setPairingLoading(false);
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [pairingId, attempt]);

  async function change(kind: "confirm" | "disconnect", id: string) {
    if (mutation.current || uncertain) return;
    if (
      kind === "confirm" &&
      (!pairing ||
        pairingLoading ||
        pairing.status !== "pending" ||
        !compared ||
        Date.parse(pairing.expiresAt) <= Date.now())
    )
      return;
    const controller = new AbortController();
    mutation.current = controller;
    const previousRead = read.current;
    read.current = null;
    previousRead?.abort();
    ++sequence.current;
    setBusy(kind === "confirm" ? "confirm" : id);
    setError("");
    try {
      await boundedRequest(controller, (signal) =>
        api(
          kind === "confirm"
            ? `/push/pairings/${encodeURIComponent(id)}/confirm`
            : `/push/installations/${encodeURIComponent(id)}`,
          {
            method: kind === "confirm" ? "POST" : "DELETE",
            body: kind === "confirm" ? {} : undefined,
            authenticated: true,
            signal,
          },
        ),
      );
      if (mutation.current !== controller || controller.signal.aborted) return;
      if (kind === "confirm") {
        setPairing((current) =>
          current ? { ...current, status: "confirmed" } : null,
        );
        notify.success(
          "Телефон подтверждён. Вернитесь в приложение для завершения подключения.",
        );
      } else {
        setInstallations(
          (current) => current?.filter((item) => item.id !== id) ?? null,
        );
        setDisconnect(null);
        notify.success("Телефон отключён от уведомлений.");
      }
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
          ? "Ответ на действие не получен. Обновите состояние перед повтором; запрос не повторяется автоматически."
          : cause instanceof ApiError && cause.status === 404
            ? "Подключение недоступно или уже завершено. Обновите состояние."
            : cause instanceof ApiError && cause.code === "pairing_consumed"
              ? "Подключение уже завершено. Обновите состояние телефона."
              : errorMessage(cause),
      );
    } finally {
      if (mutation.current === controller) {
        mutation.current = null;
        setBusy("");
      }
    }
  }

  const expired = pairing && Date.parse(pairing.expiresAt) <= now;
  return (
    <section className="panel android-push-panel">
      <div className="panel-heading">
        <span className="device-icon">
          <Icon name="phone" />
        </span>
        <div>
          <h2>Android push</h2>
          <p>Уведомления об инцидентах и восстановлении на ваши телефоны.</p>
        </div>
      </div>
      {(pairingId || pairingError) && (
        <div className="push-pairing form-stack">
          <h3>Подтверждение телефона</h3>
          <p className="small">
            Аккаунт: <strong>{profile.account.name}</strong> · {profile.email}
          </p>
          {pairingError ? (
            <Notice>{pairingError}</Notice>
          ) : !pairing || pairingLoading ? (
            <Loading text="Загружаем подключение…" />
          ) : (
            <>
              <p className="small">
                Телефон: <strong>{pairing.deviceLabel}</strong>
              </p>
              {pairing.status === "confirmed" && !expired ? (
                <>
                  <p className="small" role="status">
                    Подтверждение принято. Завершите настройку в приложении на
                    подключаемом телефоне.
                  </p>
                  <a
                    className="primary"
                    href={androidPushIntent("complete", pairing.id)}
                  >
                    Вернуться в приложение <Icon name="arrow-right" />
                  </a>
                </>
              ) : (
                <>
                  <p className="small">
                    Сверьте код с кодом в приложении на вашем телефоне:
                  </p>
                  <strong className="push-comparison-code">
                    {pairing.verificationCode}
                  </strong>
                  <p className="small muted">
                    Ссылка действует до {dateTime(pairing.expiresAt)}.
                    Подтверждайте только подключение, которое начали сами.
                  </p>
                  {expired ? (
                    <Notice>
                      Срок подтверждения истёк. Начните подключение в приложении
                      заново.
                    </Notice>
                  ) : (
                    <>
                      <label className="checkbox-label">
                        <input
                          type="checkbox"
                          checked={compared}
                          disabled={!!busy}
                          onChange={(event) =>
                            setCompared(event.target.checked)
                          }
                        />
                        <span>
                          Код совпадает, подключить телефон к этому аккаунту
                        </span>
                      </label>
                      <button
                        className="primary"
                        disabled={!compared || !!busy || uncertain}
                        onClick={() => void change("confirm", pairing.id)}
                      >
                        {busy === "confirm"
                          ? "Подтверждаем…"
                          : "Подтвердить подключение"}
                      </button>
                    </>
                  )}
                </>
              )}
            </>
          )}
        </div>
      )}
      {error && <Notice>{error}</Notice>}
      <div className="push-actions">
        {android && enabled && (
          <a className="secondary" href={androidPushIntent("connect")}>
            Подключить этот телефон <Icon name="arrow-right" />
          </a>
        )}
        <button
          type="button"
          className="quiet"
          disabled={!!busy}
          onClick={() => {
            if (pairingId) setPairingError("");
            setAttempt((value) => value + 1);
          }}
        >
          Обновить состояние
        </button>
      </div>
      {android && enabled && (
        <p className="small muted">
          Для push-уведомлений нужна версия приложения 1.1.0 или новее. Если у
          вас версия 1.0, <a href="/apps/android/download">обновите APK</a>{" "}
          перед подключением телефона.
        </p>
      )}
      {!android && (
        <p className="small muted">
          Для подключения откройте настройки на Android-телефоне с установленным{" "}
          <Link href="/android">приложением Heartbeat Checker</Link>.
        </p>
      )}
      {installations === null ? (
        !error && <Loading text="Загружаем телефоны…" />
      ) : (
        <>
          {!enabled && (
            <Notice>
              Android push сейчас недоступен. Настройки подключённых телефонов
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
                        Откройте приложение на этом телефоне и повторите
                        подключение. Проверьте разрешение на уведомления.{" "}
                        {android && enabled && (
                          <a href={androidPushIntent("connect")}>
                            Переподключить текущий телефон
                          </a>
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
                            onClick={() => void change("disconnect", item.id)}
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
        После подключения включите Android push отдельно для каждого монитора в
        его настройках уведомлений. Можно выбрать несколько телефонов. Статус
        настройки не означает, что телефон сейчас в сети; доставка зависит от
        разрешений Android, сети и RuStore Push. Напоминания не отправляются.
      </p>
    </section>
  );
}
