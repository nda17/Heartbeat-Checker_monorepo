"use client";
import { notify } from "@/shared/notifications";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  errorMessage,
  paymentConfirmationUrl,
} from "@/shared/api";
import { isPricing } from "@/entities/billing";
import { Entitlement } from "@/entities/monitor";
import { useSession } from "@/entities/session";
import { dateTime, Loading, Notice } from "@/shared/ui";
type Order = {
  id: string;
  provider: "mock" | "yookassa";
  status: string;
  reconciliationStatus?: "pending" | "complete" | "manual_review" | "stopped";
  fulfillmentStatus?: "pending" | "fulfilled" | "blocked" | "not_required";
  fulfillmentReason?:
    | "admin_grant"
    | "account_deleted"
    | "account_inactive"
    | "capacity_changed"
    | "paid_entitlement";
  monitorCount: number;
  amountMinor: number;
  currency: "RUB";
  confirmationUrl?: string;
  test?: boolean;
  lastErrorCode?: string;
  createdAt: string;
  expiresAt: string | null;
  completedAt: string | null;
  activeUntil: string | null;
};
type Billing = {
  provider: "disabled" | "mock" | "yookassa";
  canSimulate: boolean;
  canPay?: boolean;
  test?: boolean;
  accessUnchanged?: boolean;
  pricePerMonitorMinor: number;
  priceVersion: number;
  orders: Order[];
};
type OrderIntent = {
  count: number;
  key: string;
  priceVersion?: number;
  pricePerMonitorMinor?: number;
};
type Quote = Pick<Billing, "pricePerMonitorMinor" | "priceVersion">;
function validQuote(billing: Quote) {
  return (
    Number.isSafeInteger(billing.priceVersion) &&
    billing.priceVersion > 0 &&
    Number.isSafeInteger(billing.pricePerMonitorMinor) &&
    billing.pricePerMonitorMinor >= 100 &&
    billing.pricePerMonitorMinor <= 1_000_000
  );
}
function maxOrderCount(billing: Billing) {
  return Math.min(1000, Math.floor(35_000_000 / billing.pricePerMonitorMinor));
}
function orderStatus(order: Order) {
  if (order.provider === "mock") {
    if (order.status === "succeeded") return "Демонстрация завершена";
    if (order.status === "canceled") return "Демонстрация отменена";
    return "Ожидает результата демонстрации";
  }
  const label = order.test ? "Тестовый платёж" : "Платёж";
  if (order.status === "succeeded") return `${label} подтверждён ЮKassa`;
  if (order.status === "canceled") return `${label} отменён`;
  return `Ожидает подтверждения ЮKassa (${order.test ? "тест" : "оплата"})`;
}
function fulfillmentMessage(order: Order) {
  if (order.fulfillmentStatus === "blocked") {
    switch (order.fulfillmentReason) {
      case "admin_grant":
        return "На момент обработки заказа применение было заблокировано административной квотой. Этот заказ не выдал новую квоту.";
      case "account_deleted":
        return "Аккаунт удалён. Квота по заказу не применяется.";
      case "account_inactive":
        return "Аккаунт неактивен. Квота по заказу не применяется.";
      case "capacity_changed":
        return "Параметры квоты изменились. Для применения заказа обратитесь в поддержку.";
      case "paid_entitlement":
        return "Действующая оплаченная квота сохранена. Тестовый заказ её не изменил.";
      default:
        return "Квота не изменена. Для проверки заказа обратитесь в поддержку.";
    }
  }
  if (order.fulfillmentStatus === "fulfilled") return "Квота применена.";
  if (order.status === "canceled") return "Квота по этому заказу не выдана.";
  if (order.status === "succeeded" && order.fulfillmentStatus === "pending")
    return "Применение квоты ещё не завершено. Обновите статус заказа позже.";
  return "";
}
function unfinishedOrder(order: Order) {
  return (
    order.status === "pending" ||
    (order.status === "succeeded" && order.fulfillmentStatus === "pending")
  );
}
function money(amountMinor: number) {
  return new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency: "RUB",
  }).format(amountMinor / 100);
}
function activePart(part: { activeUntil: string | null } | null) {
  return !!part?.activeUntil && Date.parse(part.activeUntil) > Date.now();
}
function paidSourceLabel(source: string) {
  if (source === "yookassa") return "Оплаченная подписка";
  if (source === "yookassa_test") return "Тестовый платёж ЮKassa";
  if (source === "mock_test") return "Демонстрационная квота, без списания";
  if (source === "local_test") return "Локальная тестовая квота, без оплаты";
  return source;
}
function YooKassaCheckout({
  billing,
  renewalQuota,
  onOrder,
  onRefresh,
}: {
  billing: Billing;
  renewalQuota: number | null;
  onOrder: (order: Order) => void;
  onRefresh: (signal?: AbortSignal) => Promise<Billing | undefined>;
}) {
  const { profile } = useSession();
  const storageKey = `heartbeat-billing-request:${profile.account.id}`;
  const [count, setCount] = useState(renewalQuota ?? 1);
  const [order, setOrder] = useState<Order | null>(null);
  const [orderId, setOrderId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [statusBusy, setStatusBusy] = useState(false);
  const [error, setError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [statusNote, setStatusNote] = useState("");
  const [statusAttempt, setStatusAttempt] = useState(0);
  const [uncertain, setUncertain] = useState(false);
  const [intentAmount, setIntentAmount] = useState<number | null>(null);
  const request = useRef<OrderIntent | null>(null);
  const creating = useRef<AbortController | null>(null);
  const creationTimeout = useRef<number | undefined>(undefined);
  useEffect(() => {
    const uuid =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    try {
      const saved = JSON.parse(
        window.sessionStorage.getItem(storageKey) ?? "null",
      );
      if (
        saved &&
        uuid.test(saved.key) &&
        Number.isInteger(saved.count) &&
        saved.count >= 1 &&
        saved.count <= 1000 &&
        (saved.priceVersion === undefined ||
          (Number.isSafeInteger(saved.priceVersion) &&
            saved.priceVersion > 0)) &&
        (saved.pricePerMonitorMinor === undefined ||
          (Number.isSafeInteger(saved.pricePerMonitorMinor) &&
            saved.pricePerMonitorMinor >= 100 &&
            saved.pricePerMonitorMinor <= 1_000_000))
      ) {
        request.current = saved;
        // eslint-disable-next-line react-hooks/set-state-in-effect -- Restore the account-bound immutable quote from tab storage.
        setIntentAmount(
          saved.pricePerMonitorMinor === undefined
            ? null
            : saved.count * saved.pricePerMonitorMinor,
        );
        // Hydrate the account-bound retry key before allowing a new order.
        setCount(saved.count);
        setUncertain(true);
      }
    } catch {
      // In-memory retries remain available when tab storage is blocked.
    }
    const returnedId = new URLSearchParams(window.location.search).get("order");
    const pending = billing.orders.find(
      (item) => item.provider === "yookassa" && unfinishedOrder(item),
    );
    if (pending && !request.current) setCount(pending.monitorCount);
    if (returnedId !== null && !uuid.test(returnedId)) {
      setStatusError("Неверный номер заказа в ссылке.");
      setOrderId(pending?.id ?? null);
    } else setOrderId(returnedId ?? pending?.id ?? null);
    const resume = () => setStatusAttempt((value) => value + 1);
    const visible = () => {
      if (document.visibilityState === "visible") resume();
    };
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) resume();
    };
    window.addEventListener("focus", resume);
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("pageshow", restored);
    return () => {
      creating.current?.abort();
      creating.current = null;
      window.clearTimeout(creationTimeout.current);
      window.removeEventListener("focus", resume);
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("pageshow", restored);
    };
    // The overview supplies a starting order; later updates come from the order endpoint.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey]);
  useEffect(() => {
    if (!orderId) return;
    let active = true;
    let controller: AbortController | null = null;
    let timer: number | undefined;
    let timeout: number | undefined;
    let attempts = 0;
    const poll = async () => {
      if (!active || document.visibilityState === "hidden") return;
      const current = new AbortController();
      controller = current;
      setStatusBusy(true);
      setStatusError("");
      setStatusNote("");
      timeout = window.setTimeout(() => {
        current.abort();
        if (active) {
          setStatusBusy(false);
          setStatusError(
            "Не удалось дождаться статуса заказа. Повторите проверку; новый заказ не создаётся.",
          );
        }
      }, 10_000);
      try {
        const result = await api<{ order: Order }>(
          `/billing/orders/${encodeURIComponent(orderId)}`,
          { signal: current.signal },
        );
        if (!active || current.signal.aborted) return;
        if (result.order.id !== orderId) throw new Error("Unexpected order");
        setOrder(result.order);
        if (unfinishedOrder(result.order) && !request.current)
          setCount(result.order.monitorCount);
        onOrder(result.order);
        if (
          result.order.reconciliationStatus === "manual_review" ||
          result.order.reconciliationStatus === "stopped"
        ) {
          setStatusNote(
            "Нужна проверка результата. Новый платёж не создаётся автоматически; обратитесь в поддержку.",
          );
        } else if (unfinishedOrder(result.order)) {
          if (++attempts < 10)
            timer = window.setTimeout(() => void poll(), 3000);
          else
            setStatusNote(
              "Заказ ещё обрабатывается. Статус можно проверить позже без создания нового заказа.",
            );
        } else {
          await onRefresh(current.signal);
        }
      } catch (cause) {
        if (active && !current.signal.aborted)
          setStatusError(errorMessage(cause));
      } finally {
        window.clearTimeout(timeout);
        if (active && !current.signal.aborted) setStatusBusy(false);
      }
    };
    void poll();
    return () => {
      active = false;
      controller?.abort();
      window.clearTimeout(timer);
      window.clearTimeout(timeout);
    };
  }, [orderId, statusAttempt, onOrder, onRefresh]);
  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      creating.current ||
      billing.canPay !== true ||
      (!request.current &&
        ((!!orderId && order?.id !== orderId) ||
          (order && unfinishedOrder(order))))
    )
      return;
    const monitorCount =
      request.current?.count ??
      renewalQuota ??
      Number(new FormData(event.currentTarget).get("monitorCount"));
    if (
      !Number.isInteger(monitorCount) ||
      monitorCount < 1 ||
      monitorCount > 1000
    ) {
      setError("Укажите количество мониторов от 1 до 1000.");
      return;
    }
    if (!request.current && !validQuote(billing)) {
      setError(
        "Цена недоступна. Обновите условия оплаты перед оформлением заказа.",
      );
      return;
    }
    if (!request.current && monitorCount > maxOrderCount(billing)) {
      setError(
        "Сумма заказа не должна превышать 350 000 рублей. Уменьшите количество мониторов.",
      );
      return;
    }
    if (
      !request.current &&
      renewalQuota !== null &&
      monitorCount !== renewalQuota
    ) {
      setError(
        `Количество оплаченных мест при продлении: ${renewalQuota}. Изменить количество можно после её окончания.`,
      );
      return;
    }
    request.current ??= {
      count: monitorCount,
      key: crypto.randomUUID(),
      priceVersion: billing.priceVersion,
      pricePerMonitorMinor: billing.pricePerMonitorMinor,
    };
    try {
      window.sessionStorage.setItem(
        storageKey,
        JSON.stringify(request.current),
      );
    } catch {
      /* Retry the same request in this tab. */
    }
    const controller = new AbortController();
    creating.current = controller;
    setCount(monitorCount);
    setBusy(true);
    setIntentAmount(
      request.current?.pricePerMonitorMinor === undefined
        ? null
        : request.current.count * request.current.pricePerMonitorMinor,
    );
    setUncertain(true);
    setError("");
    setStatusError("");
    let definitiveRejection = false;
    const timer = window.setTimeout(() => {
      controller.abort();
      if (creating.current === controller) {
        creating.current = null;
        setBusy(false);
        if (definitiveRejection)
          setStatusError(
            "Не удалось обновить условия оплаты. Обновите страницу перед новым заказом.",
          );
        else
          setError(
            "Ответ на создание заказа не получен. Повторите запрос на этой странице с теми же параметрами и ключом заказа.",
          );
      }
    }, 10_000);
    creationTimeout.current = timer;
    try {
      const result = await api<{ order: Order }>("/billing/orders", {
        method: "POST",
        authenticated: true,
        signal: controller.signal,
        body: {
          monitorCount: request.current.count,
          idempotencyKey: request.current.key,
          priceVersion: request.current.priceVersion,
        },
      });
      if (creating.current !== controller || controller.signal.aborted) return;
      setOrder(result.order);
      setOrderId(result.order.id);
      setStatusAttempt((value) => value + 1);
      onOrder(result.order);
      request.current = null;
      setUncertain(false);
      try {
        window.sessionStorage.removeItem(storageKey);
      } catch {
        /* The accepted order remains visible. */
      }
    } catch (cause) {
      if (creating.current !== controller || controller.signal.aborted) return;
      if (
        cause instanceof ApiError &&
        cause.status >= 400 &&
        cause.status < 500
      )
        notify.error(errorMessage(cause));
      else setError(errorMessage(cause));
      if (cause instanceof ApiError && [400, 403, 409].includes(cause.status)) {
        definitiveRejection = true;
        request.current = null;
        setUncertain(false);
        try {
          window.sessionStorage.removeItem(storageKey);
        } catch {
          /* The current form is unlocked even when storage is unavailable. */
        }
        try {
          const latest = await onRefresh(controller.signal);
          if (creating.current !== controller || controller.signal.aborted)
            return;
          const current = latest?.orders.find(
            (item) => item.provider === "yookassa" && unfinishedOrder(item),
          );
          setOrder(current ?? null);
          setOrderId(current?.id ?? null);
          if (current) {
            setCount(current.monitorCount);
            setStatusAttempt((value) => value + 1);
          }
        } catch (refreshFailure) {
          if (creating.current === controller && !controller.signal.aborted)
            setStatusError(
              `Не удалось обновить заказы: ${errorMessage(refreshFailure)} Обновите страницу перед новым заказом.`,
            );
        }
      }
    } finally {
      window.clearTimeout(timer);
      if (creating.current === controller) {
        creating.current = null;
        setBusy(false);
      }
    }
  }
  const canPay = billing.canPay === true;
  const pending = order && unfinishedOrder(order);
  const quotedAmount =
    order && pending && !uncertain
      ? order.amountMinor
      : uncertain
        ? intentAmount
        : validQuote(billing)
          ? (renewalQuota ?? count) * billing.pricePerMonitorMinor
          : null;
  const awaitingOrder = !!orderId && order?.id !== orderId;
  const halted =
    order?.reconciliationStatus === "manual_review" ||
    order?.reconciliationStatus === "stopped";
  const confirmationUrl =
    order?.provider === "yookassa" && order.status === "pending" && !halted
      ? paymentConfirmationUrl(order.confirmationUrl)
      : null;
  return (
    <section
      className="panel settings-panel billing-checkout"
      aria-labelledby="checkout-title"
    >
      <div className="panel-heading">
        <div>
          <h2 id="checkout-title">Оформление заказа</h2>
          <p>
            {billing.test
              ? "Тестовый платёж на странице ЮKassa. Деньги не списываются."
              : "Оплата на странице ЮKassa. Сумма заказа будет списана после подтверждения платежа."}
          </p>
        </div>
      </div>
      {renewalQuota !== null && (
        <p className="small">
          Количество оплаченных мест при продлении: {renewalQuota}.
          Административная квота действует отдельно и не меняет размер покупки.
        </p>
      )}
      {!canPay ? (
        <p>Оплата пока недоступна для вашего аккаунта.</p>
      ) : (
        <form className="form-stack" onSubmit={create}>
          <label>
            Количество мониторов
            <input
              name="monitorCount"
              type="number"
              min={1}
              max={uncertain ? 1000 : maxOrderCount(billing)}
              step={1}
              required
              value={
                pending && order
                  ? order.monitorCount
                  : !uncertain && renewalQuota !== null
                    ? renewalQuota
                    : count
              }
              onChange={(event) => setCount(Number(event.target.value))}
              disabled={busy || !!pending || uncertain || awaitingOrder}
              readOnly={renewalQuota !== null}
            />
          </label>
          <p>
            Сумма:{" "}
            <strong>
              {quotedAmount === null
                ? "Уточняется по исходному заказу"
                : money(quotedAmount)}
            </strong>
          </p>
          {!validQuote(billing) && !uncertain && (
            <p>
              Цена недоступна.{" "}
              <button
                type="button"
                className="quiet"
                disabled={busy}
                onClick={() =>
                  void onRefresh().catch(() =>
                    notify.error(
                      "Не удалось обновить цену. Повторите попытку.",
                    ),
                  )
                }
              >
                Обновить условия оплаты
              </button>
            </p>
          )}
          <p className="field-hint">
            Максимальная сумма заказа — 350 000 рублей. Окончательная сумма и
            параметры заказа рассчитываются сервером. Автопродление не
            подключается.
          </p>
          {error && <Notice>{error}</Notice>}
          <button
            className="primary"
            disabled={
              busy ||
              (!uncertain &&
                (!validQuote(billing) || !!pending || awaitingOrder))
            }
          >
            {busy
              ? "Создаём заказ…"
              : uncertain
                ? "Повторить запрос заказа"
                : "Оформить заказ"}
          </button>
        </form>
      )}
      {order && (
        <div className="form-stack billing-order-status" aria-live="polite">
          <h3>{orderStatus(order)}</h3>
          <p>
            Мониторы: {order.monitorCount} · {money(order.amountMinor)}.
          </p>
          {fulfillmentMessage(order) && <p>{fulfillmentMessage(order)}</p>}
          {confirmationUrl && canPay && (
            <a className="button" href={confirmationUrl}>
              Перейти к оплате в ЮKassa
            </a>
          )}
          {order.status === "pending" && !confirmationUrl && !halted && (
            <p>
              Ссылка на оплату готовится. Возврат на эту страницу сам по себе не
              подтверждает платёж.
            </p>
          )}
        </div>
      )}
      {statusError && <Notice>{statusError}</Notice>}
      {statusNote && (
        <p className="small" role="status">
          {statusNote}
        </p>
      )}
      {orderId && (
        <button
          type="button"
          className="secondary"
          disabled={statusBusy}
          onClick={() => setStatusAttempt((value) => value + 1)}
        >
          {statusBusy ? "Проверяем статус…" : "Обновить статус заказа"}
        </button>
      )}
    </section>
  );
}
export function BillingPage() {
  const [billing, setBilling] = useState<Billing | null>(null);
  const [loadError, setLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [count, setCount] = useState(1);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [intentAmount, setIntentAmount] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [demo, setDemo] = useState<"succeeded" | "canceled" | null>(null);
  const request = useRef<OrderIntent | null>(null);
  const quote = useRef<Quote>({ priceVersion: 0, pricePerMonitorMinor: 0 });
  const quoteGeneration = useRef(0);
  const highestQuoteVersion = useRef(0);
  const mergeQuote = useCallback((next: Quote, generation: number): Quote => {
    // Overview and background pricing share one fence. A later request owns
    // failures, and even a newer request cannot roll an applied version back.
    if (generation === quoteGeneration.current) {
      if (validQuote(next)) {
        if (next.priceVersion >= highestQuoteVersion.current) {
          highestQuoteVersion.current = next.priceVersion;
          quote.current = {
            priceVersion: next.priceVersion,
            pricePerMonitorMinor: next.pricePerMonitorMinor,
          };
        }
      } else {
        quote.current = { priceVersion: 0, pricePerMonitorMinor: 0 };
      }
    }
    return quote.current;
  }, []);
  const rememberOrder = useCallback((order: Order) => {
    setBilling((current) =>
      current
        ? {
            ...current,
            orders: [
              order,
              ...current.orders.filter((item) => item.id !== order.id),
            ],
          }
        : current,
    );
  }, []);
  const load = useCallback(
    async (signal?: AbortSignal) => {
      const generation = ++quoteGeneration.current;
      try {
        const [result, access] = await Promise.all([
          api<Billing>("/billing", { signal }),
          api<{ entitlement: Entitlement }>("/entitlement", { signal }),
        ]);
        if (signal?.aborted) return;
        const overview = { ...result, ...mergeQuote(result, generation) };
        setBilling(overview);
        setEntitlement(access.entitlement);
        return overview;
      } catch (cause) {
        if (!signal?.aborted) {
          const currentQuote = mergeQuote(
            { priceVersion: 0, pricePerMonitorMinor: 0 },
            generation,
          );
          setBilling((current) =>
            current ? { ...current, ...currentQuote } : current,
          );
        }
        throw cause;
      }
    },
    [mergeQuote],
  );
  useEffect(() => {
    let alive = true;
    let current: AbortController | null = null;
    let timer: number | undefined;
    async function refreshPrice() {
      if (!alive || current || document.visibilityState === "hidden") return;
      const controller = new AbortController();
      current = controller;
      const generation = ++quoteGeneration.current;
      const invalidate = () => {
        const currentQuote = mergeQuote(
          { priceVersion: 0, pricePerMonitorMinor: 0 },
          generation,
        );
        setBilling((value) => (value ? { ...value, ...currentQuote } : value));
      };
      timer = window.setTimeout(() => {
        if (current !== controller) return;
        current = null;
        controller.abort();
        if (alive) invalidate();
      }, 5_000);
      const timeout = timer;
      try {
        const result = await api<{ pricing: unknown }>("/billing/pricing", {
          signal: controller.signal,
        });
        if (!alive || current !== controller || controller.signal.aborted)
          return;
        const pricing = result.pricing;
        if (!isPricing(pricing)) {
          invalidate();
          return;
        }
        const currentQuote = mergeQuote(
          {
            pricePerMonitorMinor: pricing.pricePerMonitorMinor,
            priceVersion: pricing.version,
          },
          generation,
        );
        setBilling((value) => (value ? { ...value, ...currentQuote } : value));
      } catch {
        if (alive && current === controller && !controller.signal.aborted)
          invalidate();
      } finally {
        window.clearTimeout(timeout);
        if (current === controller) current = null;
      }
    }
    const update = () => {
      void refreshPrice();
    };
    window.addEventListener("focus", update);
    window.addEventListener("pageshow", update);
    document.addEventListener("visibilitychange", update);
    const interval = window.setInterval(update, 60_000);
    return () => {
      alive = false;
      current?.abort();
      current = null;
      window.clearTimeout(timer);
      window.clearInterval(interval);
      window.removeEventListener("focus", update);
      window.removeEventListener("pageshow", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, [mergeQuote]);
  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoadError("");
    const timer = window.setTimeout(() => {
      controller.abort();
      setLoadError(
        "Загрузка оплаты заняла слишком много времени. Повторите попытку.",
      );
    }, 10_000);
    void load(controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setLoadError(errorMessage(cause));
          controller.abort();
        }
      })
      .finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [load, loadAttempt]);
  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!billing?.canSimulate) {
      setDemo("succeeded");
      return;
    }
    setBusy(true);
    setError("");

    if (!request.current && !validQuote(billing)) {
      setBusy(false);
      setError("Цена недоступна. Обновите условия оплаты.");
      return;
    }
    if (
      !request.current &&
      (paidRenewalQuota ?? count) > maxOrderCount(billing)
    ) {
      setBusy(false);
      setError(
        "Сумма заказа не должна превышать 350 000 рублей. Уменьшите количество мониторов.",
      );
      return;
    }
    request.current ??= {
      count: paidRenewalQuota ?? count,
      key: crypto.randomUUID(),
      priceVersion: billing.priceVersion,
      pricePerMonitorMinor: billing.pricePerMonitorMinor,
    };
    setIntentAmount(
      request.current?.pricePerMonitorMinor === undefined
        ? null
        : request.current.count * request.current.pricePerMonitorMinor,
    );
    setUncertain(true);
    try {
      const result = await api<{ order: Order }>("/billing/orders", {
        method: "POST",
        authenticated: true,
        body: {
          monitorCount: request.current.count,
          idempotencyKey: request.current.key,
          priceVersion: request.current.priceVersion,
        },
      });
      request.current = null;
      setUncertain(false);
      setBilling((current) =>
        current
          ? {
              ...current,
              orders: [
                result.order,
                ...current.orders.filter(
                  (order) => order.id !== result.order.id,
                ),
              ],
            }
          : current,
      );
      notify.success(
        "Тестовый заказ создан. Выберите результат ниже. Деньги не списываются.",
      );
      try {
        await load();
      } catch (e) {
        setError(`Заказ создан, но список не обновился: ${errorMessage(e)}`);
      }
    } catch (e) {
      if (e instanceof ApiError && e.status >= 400 && e.status < 500)
        notify.error(errorMessage(e));
      else setError(errorMessage(e));
      if (e instanceof ApiError && [400, 403, 409].includes(e.status)) {
        request.current = null;
        setUncertain(false);
        try {
          await load();
        } catch {
          setError(
            "Не удалось обновить условия оплаты. Обновите страницу перед новым заказом.",
          );
        }
      }
    } finally {
      setBusy(false);
    }
  }
  async function simulate(order: Order, outcome: "succeeded" | "canceled") {
    setBusy(true);
    setError("");

    try {
      const result = await api<{ order: Order }>(
        `/billing/orders/${encodeURIComponent(order.id)}/simulate`,
        {
          method: "POST",
          authenticated: true,
          body: { outcome },
        },
      );
      setBilling((current) =>
        current
          ? {
              ...current,
              orders: current.orders.map((item) =>
                item.id === order.id ? result.order : item,
              ),
            }
          : current,
      );
      notify.success(
        result.order.status === "succeeded"
          ? "Тестовая квота активирована. Деньги не списаны."
          : "Тестовый заказ отменён. Деньги не списаны.",
      );
      try {
        await load();
      } catch (e) {
        setError(
          `Результат сохранён, но список не обновился: ${errorMessage(e)}`,
        );
      }
    } catch (e) {
      if (e instanceof ApiError && e.status >= 400 && e.status < 500)
        notify.error(errorMessage(e));
      else setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const pending = billing?.orders.find((order) => order.status === "pending");
  const paidRenewalQuota =
    entitlement &&
    entitlement.quota > 0 &&
    activePart(entitlement.paid) &&
    (entitlement.source === "combined" ||
      entitlement.source === entitlement.paid?.source)
      ? entitlement.paid!.quota
      : null;
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">ПОДПИСКА И ЗАКАЗЫ</p>
          <h1>Оплата</h1>
          <p>
            {billing
              ? validQuote(billing)
                ? `${money(billing.pricePerMonitorMinor)} за один монитор в месяц.`
                : "Цена временно недоступна. Обновите условия оплаты."
              : "Квота мониторинга и история заказов."}
          </p>
        </div>
      </div>
      {error && <Notice>{error}</Notice>}

      {loadError ? (
        <div className="form-stack">
          <Notice>{loadError}</Notice>
          <button
            type="button"
            className="secondary"
            onClick={() => setLoadAttempt((value) => value + 1)}
          >
            Повторить загрузку оплаты
          </button>
        </div>
      ) : !billing ? (
        <Loading text="Загружаем заказы…" />
      ) : (
        <>
          {entitlement && (
            <section className="panel settings-panel" aria-label="Состав квоты">
              <div className="panel-heading">
                <div>
                  <h2>Доступно мест: {entitlement.quota}</h2>
                  <p>
                    {entitlement.quota > 0
                      ? `Общий лимит действует до ближайшего изменения — ${dateTime(entitlement.activeUntil, { year: true })}.`
                      : "Сейчас действующей квоты нет."}
                  </p>
                </div>
              </div>
              <p>
                Мест от администратора:{" "}
                {entitlement.admin
                  ? `${entitlement.admin.quota} · до ${dateTime(entitlement.admin.activeUntil, { year: true })}${activePart(entitlement.admin) ? "" : " · срок истёк"}`
                  : "нет"}
                .
              </p>
              <p>
                {entitlement.paid
                  ? `${paidSourceLabel(entitlement.paid.source)} · мест`
                  : "Подписка"}
                :{" "}
                {entitlement.paid
                  ? `${entitlement.paid.quota} · до ${dateTime(entitlement.paid.activeUntil, { year: true })}${activePart(entitlement.paid) ? "" : " · срок истёк"}`
                  : "нет"}
                .
              </p>
              {paidRenewalQuota !== null && (
                <p className="small muted">
                  Количество оплаченных мест при продлении: {paidRenewalQuota}.
                  Административная часть в продление не входит.
                </p>
              )}
            </section>
          )}
          {billing.provider !== "yookassa" ? (
            <div className="local-banner">
              <span className="badge">Оплата</span>
              <p>
                {billing.provider === "mock"
                  ? "Доступна демонстрация оформления заказа. Она не является оплаченной подпиской."
                  : "Приём платежей пока недоступен. Реальные списания и автопродление не выполняются."}
              </p>
            </div>
          ) : null}
          {billing.provider === "yookassa" && (
            <YooKassaCheckout
              billing={billing}
              renewalQuota={paidRenewalQuota}
              onOrder={rememberOrder}
              onRefresh={load}
            />
          )}
          {billing.provider === "mock" && !billing.canSimulate && (
            <Notice>
              Для вашего аккаунта применение квоты через демонстрацию
              недоступно.
            </Notice>
          )}
          {billing.provider === "mock" && (
            <section className="panel settings-panel">
              <div className="panel-heading">
                <span className="panel-index">01</span>
                <div>
                  <h2>
                    {billing.canSimulate
                      ? "Оформление заказа"
                      : "Демонстрация оплаты"}
                  </h2>
                  <p>
                    {billing.canSimulate
                      ? "Деньги не списываются. Автопродление отключено."
                      : "Рассчитайте стоимость и посмотрите результат. Демонстрация не выдаёт квоту."}
                  </p>
                </div>
              </div>
              <form className="form-stack" onSubmit={create}>
                <label>
                  Количество мониторов
                  <input
                    type="number"
                    min={1}
                    max={uncertain ? 1000 : maxOrderCount(billing)}
                    required
                    value={
                      uncertain
                        ? count
                        : (pending?.monitorCount ?? paidRenewalQuota ?? count)
                    }
                    onChange={(event) => setCount(Number(event.target.value))}
                    disabled={busy || !!pending || uncertain}
                    readOnly={paidRenewalQuota !== null}
                  />
                  <span className="field-hint">
                    Максимальная сумма заказа — 350 000 рублей. Пока действует
                    оплаченная часть, продление доступно с тем же количеством
                    мониторов. Изменение количества — после её окончания.
                  </span>
                </label>
                <p>
                  Условная сумма:{" "}
                  <strong>
                    {uncertain
                      ? intentAmount === null
                        ? "Сумма исходного заказа уточняется"
                        : money(intentAmount)
                      : pending
                        ? money(pending.amountMinor)
                        : validQuote(billing)
                          ? money(
                              (paidRenewalQuota ?? count) *
                                billing.pricePerMonitorMinor,
                            )
                          : "Цена недоступна"}
                  </strong>
                </p>
                {!validQuote(billing) && (
                  <button
                    type="button"
                    className="quiet"
                    disabled={busy}
                    onClick={() =>
                      void load().catch(() =>
                        notify.error("Не удалось обновить цену."),
                      )
                    }
                  >
                    Обновить условия оплаты
                  </button>
                )}
                <button
                  className="primary"
                  disabled={
                    busy || !!pending || (!uncertain && !validQuote(billing))
                  }
                >
                  {busy
                    ? "Сохраняем…"
                    : uncertain
                      ? "Повторить запрос заказа"
                      : billing.canSimulate
                        ? "Создать заказ демонстрации"
                        : "Показать демо успешного результата"}
                </button>
              </form>
              {!billing.canSimulate && (
                <div className="form-stack">
                  <button
                    className="secondary"
                    onClick={() => setDemo("canceled")}
                  >
                    Показать демо отмены
                  </button>
                  {demo && (
                    <p role="status">
                      {demo === "succeeded"
                        ? "Демо: заказ завершился бы успешно."
                        : "Демо: заказ был бы отменён."}{" "}
                      Деньги не списаны. Квота не выдана, мониторинг не
                      активирован.
                    </p>
                  )}
                </div>
              )}
              {billing.canSimulate && pending && (
                <div className="form-stack">
                  <p>
                    Мониторы: {pending.monitorCount} ·{" "}
                    {money(pending.amountMinor)}. Срок подтверждения:{" "}
                    {dateTime(pending.expiresAt)}. Просроченный заказ можно
                    отменить и создать заново.
                  </p>
                  <button
                    className="primary"
                    disabled={busy}
                    onClick={() => void simulate(pending, "succeeded")}
                  >
                    Имитировать успешную оплату
                  </button>
                  <button
                    className="secondary"
                    disabled={busy}
                    onClick={() => void simulate(pending, "canceled")}
                  >
                    Отменить заказ демонстрации
                  </button>
                </div>
              )}
            </section>
          )}
          <section className="panel monitor-panel">
            <div className="table-toolbar">
              <div>
                <h2>История заказов</h2>
                <p className="small muted">
                  Последние 100 заказов. Статус платежа и применение квоты
                  отображаются отдельно.
                </p>
              </div>
            </div>
            {!billing.orders.length ? (
              <div className="empty-state">
                <h3>Заказов пока нет</h3>
                <p>Здесь появятся ваши заказы и их результаты.</p>
              </div>
            ) : (
              <div className="table-scroll">
                <table
                  className="monitor-table mobile-card-table billing-history"
                  role="table"
                >
                  <thead>
                    <tr>
                      <th>Заказ</th>
                      <th>Мониторы</th>
                      <th>Сумма</th>
                      <th>Результат</th>
                      <th>Квота до</th>
                    </tr>
                  </thead>
                  <tbody>
                    {billing.orders.map((order) => (
                      <tr key={order.id}>
                        <td data-label="Заказ">
                          <strong>
                            {order.provider === "yookassa"
                              ? order.test
                                ? "ЮKassa · тест"
                                : "ЮKassa · оплата"
                              : "Демонстрация"}
                          </strong>
                          <span className="cell-sub">
                            {dateTime(order.createdAt)}
                          </span>
                          <span className="cell-sub mono">{order.id}</span>
                        </td>
                        <td data-label="Мониторы">{order.monitorCount}</td>
                        <td data-label="Сумма">{money(order.amountMinor)}</td>
                        <td data-label="Результат">
                          {orderStatus(order)}
                          {fulfillmentMessage(order) && (
                            <span className="cell-sub">
                              {fulfillmentMessage(order)}
                            </span>
                          )}
                        </td>
                        <td data-label="Квота до">
                          {order.activeUntil
                            ? dateTime(order.activeUntil, { year: true })
                            : "—"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </>
  );
}
