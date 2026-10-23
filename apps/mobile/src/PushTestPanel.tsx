import React, { useCallback, useEffect, useRef, useState } from "react";
import { AppState } from "react-native";
import { sessionEpoch } from "./api";
import { native, type PushTest } from "./native";
import { Button, ErrorNotice, Label, TextAction } from "./ui";
const labels: Record<PushTest["state"], string> = {
  queued: "Проверка в очереди отправки.",
  sending: "Отправляем уведомление…",
  provider_accepted:
    "Сервис доставки принял уведомление. Телефон пока не подтвердил получение.",
  received: "Телефон получил проверку. Ожидаем результат показа.",
  display_submitted:
    "Android принял запрос показа уведомления. Проверьте шторку уведомлений на телефоне.",
  failed: "Доставка не удалась. Проверьте настройки и попробуйте позже.",
  expired: "Время проверки истекло. Телефон не подтвердил показ.",
  cancelled: "Проверка отменена после изменения подключения.",
};
const done = (test: PushTest) =>
  ["display_submitted", "failed", "expired", "cancelled"].includes(
    test.state,
  ) || Date.parse(test.expiresAt) <= Date.now();
export function PushTestPanel({
  ready,
  disabled,
  onRefresh,
}: {
  ready: boolean;
  disabled: boolean;
  onRefresh: () => Promise<void>;
}) {
  const [pending, setPending] = useState<{
    requestId: string;
    test?: PushTest;
  } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now());
  const mounted = useRef(true);
  const busyRef = useRef(false);
  const epoch = useRef(sessionEpoch());
  const generation = useRef(0);
  const inspecting = useRef(false);
  const valid = () => mounted.current && sessionEpoch() === epoch.current;
  const restore = useCallback(async () => {
    if (busyRef.current || inspecting.current) return;
    const request = ++generation.current;
    try {
      const saved = await native.restorePushTest();
      if (valid() && request === generation.current) setPending(saved);
    } catch {
      if (valid() && request === generation.current)
        setError("Не удалось восстановить состояние проверки.");
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void restore();
    return () => {
      mounted.current = false;
      generation.current++;
    };
  }, [restore]);
  const inspect = useCallback(async (id: string) => {
    if (busyRef.current || inspecting.current) return;
    inspecting.current = true;
    const request = ++generation.current;
    try {
      const value = await native.pushTestStatus(id);
      if (valid() && request === generation.current && value.test.id === id) {
        setPending((current) =>
          current?.test?.id === id ? { ...current, test: value.test } : current,
        );
        setError("");
      }
    } catch {
      if (valid() && request === generation.current)
        setError(
          "Не удалось обновить проверку. Состояние сохранено, повторите позже.",
        );
    } finally {
      inspecting.current = false;
    }
  }, []);
  useEffect(() => {
    if (!pending?.test || done(pending.test)) return;
    const timer = setInterval(() => {
      if (AppState.currentState === "active") void inspect(pending.test!.id);
    }, 5000);
    return () => clearInterval(timer);
  }, [pending?.test?.id, pending?.test?.state, inspect]);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    const listener = AppState.addEventListener("change", (next) => {
      if (next === "active") {
        void onRefresh();
        if (pending?.test) void inspect(pending.test.id);
        else void restore();
      }
    });
    return () => {
      clearInterval(timer);
      listener.remove();
    };
  }, [pending?.test?.id, inspect, onRefresh, restore]);
  const send = async () => {
    if (busyRef.current || disabled || !ready) return;
    busyRef.current = true;
    const request = ++generation.current;
    setBusy(true);
    setError("");
    try {
      const retry = pending && (!pending.test || !done(pending.test));
      const requestId = retry ? pending.requestId : await native.randomUUID();
      if (!valid() || request !== generation.current) return;
      setPending({
        requestId,
        ...(retry && pending.test ? { test: pending.test } : {}),
      });
      const value = await native.requestPushTest(requestId);
      if (valid() && request === generation.current)
        setPending({ requestId, test: value.test });
    } catch (cause) {
      if (!valid()) return;
      const retryAfter = Number(
        (cause as { userInfo?: { retryAfterSeconds?: number } })?.userInfo
          ?.retryAfterSeconds,
      );
      setError(
        (cause as { code?: string })?.code === "rate_limited"
          ? `Повторная проверка доступна позже${retryAfter > 0 ? `, примерно через ${retryAfter} сек` : ""}.`
          : "Результат запроса пока неизвестен. Повтор продолжит ту же проверку.",
      );
    } finally {
      busyRef.current = false;
      if (valid()) setBusy(false);
    }
  };
  const wait = pending?.test
    ? Math.max(
        0,
        Math.ceil((Date.parse(pending.test.nextAllowedAt) - now) / 1000),
      )
    : 0;
  return (
    <>
      <Label muted>
        Проверка отправляет настоящее push на этот телефон. Статус доставки и
        показ Android различаются; проверьте уведомление самостоятельно.
      </Label>
      {!ready ? (
        <Label muted>
          Для проверки включите push, разрешения Android и подключение телефона.
        </Label>
      ) : null}
      {pending?.test ? (
        <Label>{labels[pending.test.state]}</Label>
      ) : pending ? (
        <Label>Запрос сохранён. Его результат пока неизвестен.</Label>
      ) : null}
      {wait > 0 ? (
        <Label muted>Следующая проверка не раньше чем через {wait} сек.</Label>
      ) : null}
      <Button
        label={
          pending && (!pending.test || !done(pending.test))
            ? "Повторить запрос проверки"
            : "Отправить проверочное push"
        }
        secondary
        iconName="bell"
        loading={busy}
        disabled={
          disabled ||
          !ready ||
          (wait > 0 && !!pending?.test && done(pending.test))
        }
        onPress={() => void send()}
      />
      {pending?.test &&
      (!done(pending.test) ||
        (pending.test.state === "failed" &&
          Date.parse(pending.test.expiresAt) > now)) ? (
        <TextAction
          label="Обновить статус"
          disabled={busy}
          onPress={() => void inspect(pending.test!.id)}
        />
      ) : null}
      <ErrorNotice message={error} />
    </>
  );
}
