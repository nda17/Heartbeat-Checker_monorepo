"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import type { Destination } from "./index";
import { api, ApiError, boundedRequest, errorMessage } from "@/shared/api";
import {
  captureEmailCode,
  EmailCodeState,
  normalizeEmailCode,
  useEmailCodeClock,
} from "@/shared/email-code";
import { dateTime, Loading, Notice, Spinner } from "@/shared/ui";

const normalizedEmail = (value: string | undefined) =>
  (value ?? "").trim().toLowerCase();
export function EmailDestinationEditor({
  current,
  accountEmail,
  accountEmailVerified = false,
  disabled = false,
  onConnected,
  onBusyChange,
}: {
  current?: Destination;
  accountEmail?: string;
  accountEmailVerified?: boolean;
  disabled?: boolean;
  onConnected: (
    destination: Destination,
    resetRoutes: boolean,
  ) => void | Promise<void>;
  onBusyChange?: (busy: boolean) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [email, setEmail] = useState(
    current?.email ?? current?.label ?? accountEmail ?? "",
  );
  const [state, setState] = useState<EmailCodeState | null>(null);
  const stateRef = useRef<EmailCodeState | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState("");
  const [reading, setReading] = useState(true);
  const [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [notice, setNotice] = useState("");
  const [success, setSuccess] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [uncertain, setUncertain] = useState(false);
  const uncertainTarget = useRef("");
  const [rateUntil, setRateUntil] = useState(0);
  const clock = useEmailCodeClock(state);
  const rateSeconds = Math.max(0, Math.ceil((rateUntil - clock.now) / 1000));
  const resendSeconds = Math.max(
    clock.resendSeconds,
    Math.max(0, Math.ceil((rateUntil - clock.now) / 1000)),
  );
  const operation = useRef<AbortController | null>(null);
  const writing = useRef(false);
  const generation = useRef(0);
  const alive = useRef(false);
  const draftMode = useRef(false);
  const busyCallback = useRef(onBusyChange);
  const connectedCallback = useRef(onConnected);
  const currentRef = useRef(current);
  useEffect(() => {
    busyCallback.current = onBusyChange;
    connectedCallback.current = onConnected;
    currentRef.current = current;
  }, [onBusyChange, onConnected, current]);
  const accept = useCallback((value: unknown) => {
    const next = value === null ? null : captureEmailCode(value);
    if (next?.challenge.id !== stateRef.current?.challenge.id) setCode("");
    stateRef.current = next;
    setState(next);
    if (next) {
      setEmail(next.challenge.email);
      setEditing(true);
    }
  }, []);
  const committed = useCallback(
    (destination: Destination, view: number) => {
      const previous = currentRef.current;
      const resetRoutes =
        !!previous &&
        (previous.id !== destination.id ||
          previous.version !== destination.version);
      accept(null);
      setCode("");
      setEditing(false);
      setUncertain(false);
      uncertainTarget.current = "";
      setError("");
      setReadError("");
      setNotice("");
      setRefreshError("");
      setSuccess(
        resetRoutes
          ? "Адрес уведомлений подтверждён и подключён. После смены или повторного подключения адреса включите нужные мониторы заново."
          : "Email уведомлений подключён.",
      );
      // A committed connection remains successful even if the caller's refresh fails.
      void Promise.resolve()
        .then(() => connectedCallback.current(destination, resetRoutes))
        .catch(() => {
          if (alive.current && generation.current === view)
            setRefreshError(
              "Email подключён, но настройки мониторов не обновились. Обновите их перед включением уведомлений.",
            );
        });
    },
    [accept],
  );
  const restore = useCallback(
    async (reconcileTarget = "") => {
      if (!alive.current || operation.current) return;
      const controller = new AbortController();
      operation.current = controller;
      const view = ++generation.current;
      setReading(true);
      try {
        const result = await boundedRequest(controller, async (signal) => {
          const verification = await api<{ challenge: unknown }>(
            "/messenger-destinations/email/verification",
            { signal },
          );
          const destinations = reconcileTarget
            ? await api<{ destinations: Destination[] }>(
                "/messenger-destinations",
                { signal },
              )
            : null;
          return { verification, destinations };
        });
        if (
          !alive.current ||
          operation.current !== controller ||
          generation.current !== view ||
          controller.signal.aborted
        )
          return;
        const matched = result.destinations?.destinations.find(
          (destination) =>
            destination.provider === "email" &&
            destination.ready &&
            normalizedEmail(destination.email ?? destination.label) ===
              normalizedEmail(reconcileTarget),
        );
        if (!result.verification.challenge && matched) {
          committed(matched, view);
          return;
        }
        const hadChallenge = !!stateRef.current;
        accept(result.verification.challenge);
        setReadError("");
        setUncertain(false);
        uncertainTarget.current = "";
        if (result.verification.challenge)
          setNotice(
            "Подтверждение адреса ещё ожидается. Используйте последнее письмо. Запрос не повторяется автоматически.",
          );
        else if (hadChallenge || reconcileTarget)
          setNotice(
            "Эта попытка подтверждения больше недоступна. Если адрес не подключён, начните подтверждение заново.",
          );
      } catch (cause) {
        if (
          alive.current &&
          operation.current === controller &&
          generation.current === view
        )
          setReadError(
            controller.signal.aborted
              ? "Проверка состояния заняла слишком много времени. Повторите обновление."
              : errorMessage(cause),
          );
      } finally {
        if (operation.current === controller) {
          operation.current = null;
          if (alive.current && generation.current === view) setReading(false);
        }
      }
    },
    [accept, committed],
  );
  useEffect(() => {
    alive.current = true;
    const refresh = () => {
      if (document.visibilityState !== "hidden" && !draftMode.current)
        void restore(uncertainTarget.current);
    };
    refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("pageshow", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      alive.current = false;
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the latest destination operation.
      ++generation.current;
      operation.current?.abort();
      operation.current = null;
      if (writing.current) {
        writing.current = false;
        busyCallback.current?.(false);
      }
      window.removeEventListener("focus", refresh);
      window.removeEventListener("pageshow", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [restore]);
  async function submit(
    kind: "start" | "confirm" | "resend",
    event?: FormEvent<HTMLFormElement>,
  ) {
    event?.preventDefault();
    if (
      disabled ||
      operation.current ||
      reading ||
      uncertain ||
      !!readError ||
      rateSeconds > 0
    )
      return;
    if (kind === "resend" && (!state || resendSeconds > 0)) return;
    const normalized = normalizeEmailCode(code);
    if (kind === "confirm" && (!state || !normalized)) {
      setError("Введите шесть цифр из последнего письма. Пробелы допустимы.");
      return;
    }
    const target =
      kind === "start" ? normalizedEmail(email) : state!.challenge.email;
    const previous = currentRef.current;
    const body =
      kind === "start"
        ? {
            email: target,
            destinationId: previous?.id ?? null,
            version: previous?.version ?? 0,
          }
        : kind === "confirm"
          ? { code: normalized! }
          : {};
    const controller = new AbortController();
    operation.current = controller;
    const view = ++generation.current;
    setBusy(kind);
    setNotice("");
    setSuccess("");
    setRefreshError("");
    writing.current = true;
    busyCallback.current?.(true);
    let reconcile = false;
    try {
      const path =
        kind === "start"
          ? "/messenger-destinations/email"
          : `/messenger-destinations/email/verification/${encodeURIComponent(state!.challenge.id)}/${kind}`;
      const response = await boundedRequest(controller, (signal) =>
        api<{ destination?: Destination; challenge?: unknown }>(path, {
          method: "POST",
          authenticated: true,
          body,
          signal,
        }),
      );
      if (
        !alive.current ||
        operation.current !== controller ||
        generation.current !== view ||
        controller.signal.aborted
      )
        return;
      if (response?.destination) {
        draftMode.current = false;
        committed(response.destination, view);
      } else if (response?.challenge) {
        draftMode.current = false;
        accept(response.challenge);
        setCode("");
        setRateUntil(0);
        setError("");
        setNotice(
          "Код поставлен в очередь. Проверьте почту и папку «Спам», используйте последнее письмо. Текущий получатель не меняется до подтверждения.",
        );
      } else throw new Error("Invalid email destination response");
    } catch (cause) {
      if (
        !alive.current ||
        operation.current !== controller ||
        generation.current !== view
      )
        return;
      const unknown =
        controller.signal.aborted ||
        !(cause instanceof ApiError) ||
        cause.status === 0 ||
        cause.status >= 500;
      if (unknown) {
        setUncertain(true);
        uncertainTarget.current = target;
        setNotice(
          "Ответ не получен. Проверяем адрес и попытку подтверждения; запрос не повторяется автоматически.",
        );
        reconcile = true;
      } else {
        setError(errorMessage(cause));
        if (cause.status === 429)
          setRateUntil(
            Date.now() + Math.max(60, cause.retryAfterSeconds ?? 60) * 1000,
          );
        reconcile = kind !== "start" && cause.status === 400;
      }
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        writing.current = false;
        busyCallback.current?.(false);
        if (alive.current && generation.current === view) setBusy("");
      }
    }
    if (reconcile && alive.current) await restore(uncertainTarget.current);
  }
  function editAddress() {
    ++generation.current;
    operation.current?.abort();
    operation.current = null;
    if (writing.current) {
      writing.current = false;
      busyCallback.current?.(false);
    }
    draftMode.current = true;
    accept(null);
    setBusy("");
    setReading(false);
    setEditing(true);
    setUncertain(false);
    uncertainTarget.current = "";
    setCode("");
    setError("");
    setNotice("");
    setSuccess("");
    setEmail(
      currentRef.current?.email ??
        currentRef.current?.label ??
        accountEmail ??
        "",
    );
  }
  const challenge = state?.challenge;
  return (
    <div className="form-stack email-destination-editor">
      {current && (
        <div>
          <strong>{current.email ?? current.label}</strong>
          <p className="small muted">
            {current.ready
              ? "Подтверждённый получатель уведомлений."
              : "Адрес сейчас недоступен. Его можно подключить заново или заменить."}
          </p>
        </div>
      )}
      {success && <Notice success>{success}</Notice>}
      {error && <Notice>{error}</Notice>}
      {readError && <Notice>{readError}</Notice>}
      {refreshError && <Notice>{refreshError}</Notice>}
      {notice && (
        <p className="small" role="status">
          {notice}
        </p>
      )}
      {challenge ? (
        <form
          className="form-stack"
          onSubmit={(event) => void submit("confirm", event)}
        >
          <p>
            Подтвердите адрес уведомлений: <strong>{challenge.email}</strong>.
          </p>
          <p className="field-hint">
            Код действует до {dateTime(challenge.expiresAt)}. Повторная отправка
            не продлевает срок и отменяет прежний код. Не более пяти неверных
            попыток. Email для входа и пароль не меняются.
          </p>
          <label>
            Код из письма
            <input
              name="emailCode"
              value={code}
              onChange={(event) => setCode(event.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={32}
              required
              disabled={!!busy || disabled}
            />
          </label>
          <button
            className="secondary"
            disabled={
              !!busy ||
              disabled ||
              reading ||
              uncertain ||
              !!readError ||
              rateSeconds > 0
            }
          >
            {busy === "confirm" ? (
              <>
                <Spinner />
                Подтверждаем…
              </>
            ) : (
              "Подтвердить адрес уведомлений"
            )}
          </button>
          <button
            type="button"
            className="quiet"
            disabled={
              !!busy ||
              disabled ||
              reading ||
              uncertain ||
              !!readError ||
              resendSeconds > 0
            }
            onClick={() => void submit("resend")}
          >
            {busy === "resend"
              ? "Отправляем…"
              : `Отправить повторно${resendSeconds > 0 ? ` (${resendSeconds} с)` : ""}`}
          </button>
          <button
            type="button"
            className="quiet"
            disabled={!!busy || disabled}
            onClick={editAddress}
          >
            Указать другой email
          </button>
        </form>
      ) : !current?.ready || editing ? (
        <form
          className="form-stack"
          onSubmit={(event) => void submit("start", event)}
        >
          <label>
            Email для уведомлений
            <input
              name="notificationEmail"
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              autoComplete="email"
              maxLength={254}
              required
              disabled={!!busy || disabled}
            />
          </label>
          <p className="field-hint">
            {accountEmailVerified
              ? "Подтверждённый email аккаунта подключается без нового кода. Другой адрес подтвердите кодом из письма."
              : "Адрес уведомлений подтвердите кодом из письма. Это не меняет email для входа или пароль."}{" "}
            После смены адреса или подключения ранее отключённого канала
            включите уведомления у нужных мониторов заново.{" "}
            {current?.ready
              ? "Прежний получатель работает до подтверждения нового."
              : "Подключение адреса не включает уведомления мониторов автоматически."}
          </p>
          <button
            className="secondary"
            disabled={
              !!busy ||
              disabled ||
              reading ||
              uncertain ||
              !!readError ||
              resendSeconds > 0
            }
          >
            {busy === "start" ? (
              <>
                <Spinner />
                Подключаем…
              </>
            ) : current ? (
              "Подтвердить и заменить email"
            ) : normalizedEmail(email) === normalizedEmail(accountEmail) ? (
              "Подключить email аккаунта"
            ) : (
              "Подтвердить адрес уведомлений"
            )}
          </button>
          {editing && current?.ready && (
            <button
              type="button"
              className="quiet"
              disabled={!!busy || disabled}
              onClick={() => {
                setEditing(false);
                draftMode.current = false;
              }}
            >
              Отмена
            </button>
          )}
        </form>
      ) : (
        <button
          type="button"
          className="quiet"
          disabled={!!busy || disabled}
          onClick={editAddress}
        >
          Изменить email уведомлений
        </button>
      )}
      {reading && <Loading text="Проверяем адрес уведомлений…" />}
      {(error || readError || refreshError || uncertain) && (
        <button
          type="button"
          className="quiet"
          disabled={!!busy || reading || disabled}
          onClick={() => void restore(uncertainTarget.current)}
        >
          Обновить состояние email
        </button>
      )}
    </div>
  );
}
