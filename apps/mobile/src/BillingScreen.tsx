import React, { useCallback, useEffect, useRef, useState } from "react";
import { AppState, View } from "react-native";
import { ApiError, api, errorText, session, sessionEpoch } from "./api";
import { useMonitorDraft } from "./MonitorDraft";
import { native, type PaymentIntent } from "./native";
import type {
  Balance,
  BalanceEntry,
  BalanceHistory,
  Billing,
  Order,
} from "./types";
import {
  Button,
  Card,
  DetailRow,
  ErrorNotice,
  Field,
  Label,
  Page,
  Title,
} from "./ui";

type SDKState =
  | "awaiting_token"
  | "processing"
  | "confirmation_required"
  | "complete"
  | "manual_review";
type PaymentOrder = Order & {
  provider?: string;
  currency?: string;
  checkoutKind?: "android_sdk" | "redirect";
  reconciliationStatus?: string;
  fulfillmentReason?: string;
  sdk?: {
    state: SDKState;
    shopId: string;
    environment: "test" | "live";
    paymentMethodType?: "BANK_CARD";
    draftExpiresAt: string;
    requestExpiresAt: string;
  };
};
type NativeCheckout = {
  enabled: boolean;
  shopId: string;
  environment: "test" | "live";
  paymentMethods: string[];
};
type SDKQuote = {
  shopId: string;
  environment: "test" | "live";
  amountMinor: number;
  currency: string;
  title: string;
  subtitle: string;
  savePaymentMethod: string;
  allowedPaymentMethods: string[];
  draftExpiresAt: string;
};
type SDKCapabilities = {
  configured: boolean;
  shopId: string;
  environment: "test" | "live";
};
type NativeBilling = Billing & {
  nativeCheckout?: NativeCheckout;
};
type SharedOrderRead = {
  key: string;
  controller: AbortController;
  timeout: ReturnType<typeof setTimeout>;
  promise: Promise<PaymentOrder>;
};
class OrderCheckTimeoutError extends Error {}
const billingError = (cause: unknown) =>
  cause instanceof OrderCheckTimeoutError ? cause.message : errorText(cause);
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const minTopUpMinor = 100;
const maxTopUpMinor = 35_000_000;
const quickTopUps = [26, 100, 300, 500];
const money = (minor: number) => `${(minor / 100).toLocaleString("ru-RU")} ₽`;
export function balanceMoney(balance: Balance) {
  return balance.positiveBelowCent ? "< 0,01 ₽" : money(balance.availableMinor);
}
const hasFunds = (balance: Balance) =>
  balance.availableMinor > 0 || balance.positiveBelowCent;
const estimate = (minor: number) =>
  minor > 0 && minor < 1 ? "< 0,01 ₽" : money(Math.round(minor));
function hourlySpend(balance: Balance) {
  const moscowDate = new Date(Date.parse(balance.asOf) + 3 * 60 * 60_000);
  const lastDay = new Date(
    Date.UTC(moscowDate.getUTCFullYear(), moscowDate.getUTCMonth() + 1, 0),
  ).getUTCDate();
  return (
    (balance.pricePerMonitorMinor * balance.enabledMonitors) / (lastDay * 24)
  );
}
function parseRubles(value: string) {
  const match = /^(\d{1,6})(?:[.,](\d{1,2}))?$/.exec(value.trim());
  if (!match) return 0;
  return Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
}
const dateTime = (value: string) =>
  new Date(value).toLocaleString("ru-RU", {
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: "Europe/Moscow",
  }) + " МСК";
const entryLabels: Record<BalanceEntry["kind"], string> = {
  top_up: "Пополнение",
  admin_credit: "Начисление администратора",
  usage: "Работа мониторов",
  refund: "Возврат",
};
function entryAmount(entry: BalanceEntry) {
  const sign = entry.kind === "usage" || entry.amountMinor < 0 ? "−" : "+";
  return entry.positiveBelowCent
    ? `${sign} < 0,01 ₽`
    : `${sign}${money(Math.abs(entry.amountMinor))}`;
}
const isTopUp = (order: PaymentOrder) => order.purchaseKind === "topup";
const terminal = (order: PaymentOrder) =>
  ["canceled", "failed"].includes(order.status) ||
  (order.status === "succeeded" &&
    ["fulfilled", "blocked", "not_required"].includes(order.fulfillmentStatus));
const unfinished = (order: PaymentOrder) => isTopUp(order) && !terminal(order);
const pollable = (order: PaymentOrder) =>
  isTopUp(order) &&
  (order.status === "pending" ||
    (order.status === "succeeded" && order.fulfillmentStatus === "pending")) &&
  !["manual_review", "stopped"].includes(order.reconciliationStatus ?? "");
const paid = (order: PaymentOrder) =>
  order.status === "succeeded" && order.fulfillmentStatus === "fulfilled";
const currentSession = (
  accountId: string,
  sessionId: string,
  epoch: number,
) => {
  const value = session();
  return (
    sessionEpoch() === epoch &&
    value?.account.id === accountId &&
    value.session.id === sessionId
  );
};

function validPaymentURL(value: string) {
  if (
    value.length > 2048 ||
    value.includes("#") ||
    /[\\\u0000-\u0020\u007f]/.test(value)
  )
    return false;
  try {
    const url = new URL(value);
    return (
      ["https://yoomoney.ru", "https://yookassa.ru"].includes(url.origin) &&
      !url.username &&
      !url.password &&
      !url.port
    );
  } catch {
    return false;
  }
}
function validSDKConfirmationURL(value: string) {
  if (
    value.length > 2048 ||
    value.includes("#") ||
    /[\\\u0000-\u0020\u007f]/.test(value)
  )
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === "https:" && !url.username && !url.password && !url.port
    );
  } catch {
    return false;
  }
}
function sdkReady(
  overview: NativeBilling,
  capabilities: SDKCapabilities | null,
) {
  const server = overview.nativeCheckout;
  return (
    !!server?.enabled &&
    !!capabilities?.configured &&
    !!server.shopId &&
    server.shopId === capabilities.shopId &&
    server.environment === capabilities.environment &&
    server.paymentMethods.includes("BANK_CARD")
  );
}
function validSDKQuote(
  quote: SDKQuote,
  order: PaymentOrder,
  capabilities: SDKCapabilities,
) {
  return (
    isTopUp(order) &&
    quote.shopId === capabilities.shopId &&
    quote.environment === capabilities.environment &&
    order.checkoutKind === "android_sdk" &&
    order.sdk?.shopId === quote.shopId &&
    order.sdk.environment === quote.environment &&
    quote.amountMinor === order.amountMinor &&
    quote.currency === "RUB" &&
    order.currency === "RUB" &&
    quote.savePaymentMethod === "off" &&
    quote.allowedPaymentMethods.length === 1 &&
    quote.allowedPaymentMethods[0] === "BANK_CARD" &&
    !!quote.title &&
    !!quote.subtitle &&
    quote.title.length <= 128 &&
    quote.subtitle.length <= 128 &&
    Date.parse(quote.draftExpiresAt) > Date.now()
  );
}

function orderMessage(order: PaymentOrder) {
  const demo = order.test || order.provider === "mock";
  if (!isTopUp(order)) {
    const label = demo
      ? "Тестовый платёж за прежний пакет"
      : "Платёж за прежний пакет";
    if (order.status === "succeeded") return `${label}. Оплата подтверждена.`;
    if (order.status === "canceled" || order.status === "failed")
      return `${label} отменён.`;
    return `${label}. Если деньги списаны, обратитесь в поддержку.`;
  }
  if (order.status === "succeeded" && demo)
    return "Тестовый платёж завершён. Списания с карты не было.";
  if (paid(order)) return "Баланс пополнен.";
  if (order.status === "succeeded" && order.fulfillmentStatus === "blocked")
    return "Платёж получен. Для проверки пополнения сообщите номер платежа в поддержку.";
  if (
    order.status === "succeeded" &&
    order.fulfillmentStatus === "not_required"
  )
    return "Платёж подтверждён. Для проверки пополнения обратитесь в поддержку.";
  if (
    order.reconciliationStatus === "manual_review" ||
    order.reconciliationStatus === "stopped"
  )
    return "Результат проверяется вручную. Новый заказ пока не создавайте; при необходимости обратитесь в поддержку.";
  if (
    order.checkoutKind === "android_sdk" &&
    order.sdk?.state === "awaiting_token"
  )
    return "Оформление не завершено. Продолжите оплату или отмените заказ.";
  if (
    order.checkoutKind === "android_sdk" &&
    order.sdk?.state === "confirmation_required"
  )
    return "Для завершения оплаты требуется подтверждение банка.";
  if (order.checkoutKind === "android_sdk" && order.sdk?.state === "processing")
    return "Банк обрабатывает платёж. Проверьте статус позднее.";
  if (order.status === "pending")
    return "Заказ ожидает оплаты или подтверждения. Проверьте статус позднее.";
  if (order.status === "succeeded")
    return "Платёж получен, начисление ещё проверяется.";
  return "Заказ завершён без начисления. Проверьте историю баланса.";
}

export function BillingScreen({
  returnOrder,
  onContinueMonitor,
  onBalance,
}: {
  onContinueMonitor: () => void;
  onBalance: (balance: Balance) => void;
  returnOrder?: string | null;
}) {
  const retained = useMonitorDraft();
  const [billing, setBilling] = useState<NativeBilling | null>(null);
  const [sdkCapabilities, setSDKCapabilities] =
    useState<SDKCapabilities | null>(null);
  const [balance, setBalance] = useState<Balance | null>(null);
  const [amount, setAmount] = useState(String(quickTopUps[1]));
  const [order, setOrder] = useState<PaymentOrder | null>(null);
  const [history, setHistory] = useState<BalanceHistory | null>(null);
  const [historyPage, setHistoryPage] = useState(1);
  const [historyError, setHistoryError] = useState("");
  const [currentDetails, setCurrentDetails] = useState(false);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [appActive, setAppActive] = useState(
    AppState.currentState !== "background" &&
      AppState.currentState !== "inactive",
  );
  const appActiveRef = useRef(appActive);
  const [intent, setIntent] = useState<PaymentIntent | null>(null);
  const [restoring, setRestoring] = useState(true);
  const [busy, setBusy] = useState(false);
  const [rejected, setRejected] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const request = useRef(false);
  const legacyOrder = useRef<string | null>(null);
  const intentRef = useRef<PaymentIntent | null>(null);
  const orderRef = useRef<PaymentOrder | null>(null);
  const historyPageRef = useRef(1);
  const historyRequest = useRef(0);
  const historyAbort = useRef<AbortController | null>(null);
  const orderRead = useRef<SharedOrderRead | null>(null);
  const refreshRequest = useRef(0);
  const pollGeneration = useRef(0);
  const terminalHandled = useRef(new Set<string>());
  const rejectedReturn = useRef<string | null>(null);
  const mounted = useRef(true);

  const keepIntent = useCallback(async (value: PaymentIntent | null) => {
    const prior = intentRef.current;
    if (value) await native.savePaymentIntent(value);
    else if (prior)
      await native.clearPaymentIntent(
        prior.accountId,
        prior.sessionId,
        prior.idempotencyKey,
      );
    if (intentRef.current === prior) {
      intentRef.current = value;
      if (mounted.current) setIntent(value);
    }
  }, []);

  const applyOrder = useCallback(
    (incoming: PaymentOrder, source: "read" | "write" = "read") => {
      const current = orderRef.current;
      if (current?.id === incoming.id) {
        if (
          terminal(current) ||
          (current.status === "succeeded" && incoming.status === "pending") ||
          (source === "write" &&
            ["manual_review", "stopped"].includes(
              current.reconciliationStatus ?? "",
            ) &&
            !terminal(incoming) &&
            !["manual_review", "stopped"].includes(
              incoming.reconciliationStatus ?? "",
            )) ||
          (current.sdk?.state !== "awaiting_token" &&
            incoming.sdk?.state === "awaiting_token")
        )
          return current;
      } else if (current && unfinished(current)) {
        throw new Error("Another order is active");
      }
      orderRef.current = incoming;
      if (mounted.current) {
        setOrder(incoming);
        setNotice(orderMessage(incoming));
      }
      return incoming;
    },
    [],
  );

  const inspectOrder = useCallback(
    async (id: string, mayApply?: () => boolean) => {
      if (!uuid.test(id)) throw new Error("Invalid order");
      const actor = session();
      if (!actor) throw new Error("Session unavailable");
      const epoch = sessionEpoch();
      const key = `${actor.account.id}:${actor.session.id}:${epoch}:${id}`;
      let shared = orderRead.current;
      if (!shared || shared.key !== key) {
        shared?.controller.abort();
        const controller = new AbortController();
        let timedOut = false;
        const timeout = setTimeout(() => {
          timedOut = true;
          controller.abort();
        }, 10_000);
        const promise = api<{ order: PaymentOrder }>(`/billing/orders/${id}`, {
          signal: controller.signal,
        })
          .then((result) => {
            if (result.order.id.toLowerCase() !== id.toLowerCase())
              throw new Error("Unexpected order");
            return result.order;
          })
          .catch((cause) => {
            if (timedOut)
              throw new OrderCheckTimeoutError(
                "Проверка статуса заняла слишком много времени. Повторите.",
              );
            throw cause;
          });
        shared = { key, controller, timeout, promise };
        const created = shared;
        orderRead.current = created;
        void promise.then(
          () => {
            clearTimeout(timeout);
            if (orderRead.current === created) orderRead.current = null;
          },
          () => {
            clearTimeout(timeout);
            if (orderRead.current === created) orderRead.current = null;
          },
        );
      }
      const latest = await shared.promise;
      if (
        !mounted.current ||
        !currentSession(actor.account.id, actor.session.id, epoch) ||
        (mayApply && !mayApply())
      )
        throw new Error("Order check is no longer active");
      return applyOrder(latest);
    },
    [applyOrder],
  );

  const loadHistory = useCallback(async (page: number) => {
    historyAbort.current?.abort();
    const controller = new AbortController();
    historyAbort.current = controller;
    const read = ++historyRequest.current;
    const actor = session();
    if (!actor) return;
    const epoch = sessionEpoch();
    setHistoryBusy(true);
    setHistoryError("");
    try {
      const result = await api<BalanceHistory>(
        `/billing/ledger?page=${page}&pageSize=10`,
        { signal: controller.signal },
      );
      if (
        controller.signal.aborted ||
        read !== historyRequest.current ||
        !mounted.current ||
        !currentSession(actor.account.id, actor.session.id, epoch)
      )
        return;
      historyPageRef.current = result.pagination.page;
      setHistoryPage(result.pagination.page);
      setHistory(result);
    } catch (cause) {
      if (
        !controller.signal.aborted &&
        read === historyRequest.current &&
        mounted.current &&
        currentSession(actor.account.id, actor.session.id, epoch)
      )
        setHistoryError(billingError(cause));
    } finally {
      if (read === historyRequest.current) setHistoryBusy(false);
      if (historyAbort.current === controller) historyAbort.current = null;
    }
  }, []);

  const refreshAggregates = useCallback(async () => {
    const actor = session();
    if (!actor) return null;
    const epoch = sessionEpoch();
    const read = ++refreshRequest.current;
    try {
      const [overview, currentBalance] = await Promise.all([
        api<NativeBilling>("/billing"),
        api<Balance>("/billing/balance"),
        loadHistory(historyPageRef.current),
      ]);
      if (
        !mounted.current ||
        read !== refreshRequest.current ||
        !currentSession(actor.account.id, actor.session.id, epoch)
      )
        return null;
      setBilling(overview);
      setBalance(currentBalance);
      onBalance(currentBalance);
      setError("");
      return overview;
    } catch (cause) {
      if (
        mounted.current &&
        read === refreshRequest.current &&
        currentSession(actor.account.id, actor.session.id, epoch)
      )
        setError(billingError(cause));
      return null;
    }
  }, [loadHistory, onBalance]);

  const refresh = useCallback(async () => {
    const actor = session();
    if (!actor) return null;
    const epoch = sessionEpoch();
    const stillCurrent = () =>
      mounted.current &&
      currentSession(actor.account.id, actor.session.id, epoch);
    const overview = await refreshAggregates();
    if (!overview || !stillCurrent()) return null;
    try {
      const wanted =
        intentRef.current?.orderId ??
        (orderRef.current && unfinished(orderRef.current)
          ? orderRef.current.id
          : null) ??
        legacyOrder.current ??
        (returnOrder &&
        uuid.test(returnOrder) &&
        rejectedReturn.current !== returnOrder
          ? returnOrder
          : null);
      if (wanted) {
        const legacy = wanted === legacyOrder.current;
        if (legacy) legacyOrder.current = null;
        try {
          const latest = await inspectOrder(wanted);
          if (!isTopUp(latest)) {
            if (wanted === returnOrder) rejectedReturn.current = wanted;
            if (intentRef.current?.orderId === wanted) await keepIntent(null);
          }
        } catch (cause) {
          if (!stillCurrent()) return null;
          if (
            !(cause instanceof ApiError) ||
            ![403, 404].includes(cause.status)
          )
            throw cause;
          if (!legacy && (wanted !== returnOrder || intentRef.current))
            throw cause;
          if (wanted === returnOrder) rejectedReturn.current = wanted;
          setError("Ссылка на заказ недоступна этому аккаунту.");
        }
      }
      if (!stillCurrent()) return null;
      if (
        !orderRef.current ||
        (!unfinished(orderRef.current) && !intentRef.current)
      ) {
        const pending = (overview.orders as PaymentOrder[]).find(
          (item) =>
            unfinished(item) &&
            !(orderRef.current?.id === item.id && terminal(orderRef.current)),
        );
        if (pending) {
          applyOrder(pending);
        }
      }
      return overview;
    } catch (cause) {
      if (stillCurrent()) setError(billingError(cause));
      return null;
    }
  }, [applyOrder, inspectOrder, keepIntent, refreshAggregates, returnOrder]);

  useEffect(() => {
    mounted.current = true;
    let alive = true;
    const actor = session();
    const epoch = sessionEpoch();
    const stillCurrent = () =>
      alive &&
      !!actor &&
      currentSession(actor.account.id, actor.session.id, epoch);
    void (async () => {
      if (!actor) return;
      try {
        const saved = await native.restorePaymentIntent();
        if (!stillCurrent()) return;
        if (saved?.version === 1) {
          await native.clearPaymentIntent(
            actor.account.id,
            actor.session.id,
            saved.orderId,
          );
          if (stillCurrent()) legacyOrder.current = saved.orderId;
        }
        if (saved?.version === 2) {
          intentRef.current = saved;
          setIntent(saved);
          setAmount(String(saved.amountMinor / 100));
        }
      } catch (cause) {
        if (stillCurrent()) setError(billingError(cause));
      }
      try {
        const capabilities = await native.sdkCapabilities();
        if (stillCurrent()) setSDKCapabilities(capabilities);
      } catch {
        if (stillCurrent()) setSDKCapabilities(null);
      } finally {
        if (stillCurrent()) {
          setRestoring(false);
          void refresh();
        }
      }
    })();
    const listener = AppState.addEventListener("change", (state) => {
      const active = state === "active";
      appActiveRef.current = active;
      setAppActive(active);
      if (active) void refresh();
    });
    return () => {
      alive = false;
      mounted.current = false;
      pollGeneration.current++;
      orderRead.current?.controller.abort();
      refreshRequest.current++;
      historyRequest.current++;
      historyAbort.current?.abort();
      listener.remove();
    };
  }, [refresh]);

  useEffect(() => {
    if (
      !appActive ||
      !order ||
      !isTopUp(order) ||
      !terminal(order) ||
      terminalHandled.current.has(order.id)
    )
      return;
    terminalHandled.current.add(order.id);
    const active = session();
    if (!active) return;
    const epoch = sessionEpoch();
    void (async () => {
      try {
        if (intentRef.current?.orderId === order.id) await keepIntent(null);
      } catch (cause) {
        if (
          mounted.current &&
          currentSession(active.account.id, active.session.id, epoch)
        )
          setError(billingError(cause));
      } finally {
        if (!appActiveRef.current) terminalHandled.current.delete(order.id);
        else if (
          mounted.current &&
          currentSession(active.account.id, active.session.id, epoch)
        )
          await refreshAggregates();
      }
    })();
  }, [
    appActive,
    order?.id,
    order?.status,
    order?.purchaseKind,
    order?.fulfillmentStatus,
    keepIntent,
    refreshAggregates,
  ]);

  useEffect(() => {
    const active = session();
    if (!appActive || !active || !order || !pollable(order)) return;
    const id = order.id;
    const epoch = sessionEpoch();
    const generation = ++pollGeneration.current;
    const startedAt = Date.now();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const valid = () =>
      !stopped &&
      mounted.current &&
      pollGeneration.current === generation &&
      orderRef.current?.id === id &&
      currentSession(active.account.id, active.session.id, epoch);
    const poll = async () => {
      if (!valid() || !pollable(orderRef.current!)) return;
      if (Date.now() - startedAt >= 90_000) {
        setNotice("Заказ ещё обрабатывается. Статус можно проверить позже.");
        return;
      }
      try {
        const latest = await inspectOrder(id, valid);
        if (!valid()) return;
        if (!pollable(latest)) return;
      } catch {
        // A failed read never starts another payment; bounded polling may retry.
      }
      if (!valid() || !pollable(orderRef.current!)) return;
      if (Date.now() - startedAt >= 90_000) {
        setNotice("Заказ ещё обрабатывается. Статус можно проверить позже.");
      } else {
        timer = setTimeout(() => void poll(), 3_000);
      }
    };
    void poll();
    return () => {
      stopped = true;
      pollGeneration.current++;
      if (timer) clearTimeout(timer);
    };
  }, [appActive, inspectOrder, order?.id, order ? pollable(order) : false]);

  const tokenize = async (
    created: PaymentOrder,
    quote: SDKQuote,
    current: PaymentIntent,
    epoch: number,
    authEpoch: number,
  ) => {
    if (
      !sdkCapabilities ||
      !sdkCapabilities.configured ||
      !validSDKQuote(quote, created, sdkCapabilities) ||
      !currentSession(current.accountId, current.sessionId, epoch)
    ) {
      setError("Не удалось проверить условия оплаты. Проверьте статус заказа.");
      return;
    }
    let response: Awaited<ReturnType<typeof native.sdkTokenize>>;
    try {
      response = await native.sdkTokenize({
        accountId: current.accountId,
        sessionId: current.sessionId,
        authEpoch,
        orderId: created.id,
        shopId: quote.shopId,
        environment: quote.environment,
        amountMinor: quote.amountMinor,
        currency: "RUB",
        title: quote.title,
        subtitle: quote.subtitle,
        allowedPaymentMethods: ["BANK_CARD"],
        savePaymentMethod: "off",
        draftExpiresAt: quote.draftExpiresAt,
      });
    } catch (cause) {
      if (currentSession(current.accountId, current.sessionId, epoch))
        await inspectOrder(created.id);
      throw cause;
    }
    if (!currentSession(current.accountId, current.sessionId, epoch)) return;
    if (response.status !== "tokenized") {
      await inspectOrder(created.id);
      if (response.status === "canceled")
        setNotice("Оформление прервано. Заказ можно продолжить или отменить.");
      else
        setNotice(
          "Результат формы неизвестен. Проверьте статус заказа перед повтором.",
        );
      return;
    }
    let paymentToken = response.paymentToken;
    if (
      !paymentToken ||
      paymentToken.length > 2048 ||
      !/^[!-~]+$/.test(paymentToken) ||
      response.paymentMethodType !== "BANK_CARD"
    ) {
      setError("Не удалось подготовить оплату. Проверьте статус заказа.");
      return;
    }
    try {
      let attached: { order: PaymentOrder };
      try {
        attached = await api<{ order: PaymentOrder }>(
          `/billing/native/orders/${created.id}/token`,
          {
            method: "POST",
            body: { paymentToken, paymentMethodType: "BANK_CARD" },
          },
        );
      } catch (cause) {
        if (currentSession(current.accountId, current.sessionId, epoch))
          await inspectOrder(created.id);
        throw cause;
      }
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (
        attached.order.id !== created.id ||
        !isTopUp(attached.order) ||
        attached.order.checkoutKind !== "android_sdk"
      )
        throw new Error("Unexpected order");
      applyOrder(attached.order, "write");
      await inspectOrder(created.id);
    } finally {
      // The provider token is never stored in React state, persistent storage or logs.
      paymentToken = undefined;
    }
  };

  const confirmSDK = () => {
    const activeSession = session();
    const current = orderRef.current;
    if (
      !activeSession ||
      !current ||
      !isTopUp(current) ||
      busy ||
      current.checkoutKind !== "android_sdk"
    )
      return;
    const epoch = sessionEpoch();
    setBusy(true);
    setError("");
    void (async () => {
      const authEpoch = await native.authEpoch();
      if (
        !currentSession(
          activeSession.account.id,
          activeSession.session.id,
          epoch,
        )
      )
        return;
      const latest = await inspectOrder(current.id);
      if (
        !currentSession(
          activeSession.account.id,
          activeSession.session.id,
          epoch,
        )
      )
        return;
      if (
        !sdkCapabilities?.configured ||
        !isTopUp(latest) ||
        latest.sdk?.state !== "confirmation_required" ||
        latest.sdk.shopId !== sdkCapabilities.shopId ||
        latest.sdk.environment !== sdkCapabilities.environment ||
        latest.sdk.paymentMethodType !== "BANK_CARD" ||
        !latest.confirmationUrl ||
        !validSDKConfirmationURL(latest.confirmationUrl)
      ) {
        setError("Подтверждение пока недоступно. Проверьте статус заказа.");
        return;
      }
      try {
        await native.sdkConfirm({
          orderId: latest.id,
          accountId: activeSession.account.id,
          sessionId: activeSession.session.id,
          authEpoch,
          shopId: latest.sdk.shopId,
          environment: latest.sdk.environment,
          confirmationUrl: latest.confirmationUrl,
          paymentMethodType: "BANK_CARD",
        });
      } finally {
        if (
          currentSession(
            activeSession.account.id,
            activeSession.session.id,
            epoch,
          )
        )
          await inspectOrder(latest.id);
      }
    })()
      .catch((cause) => setError(billingError(cause)))
      .finally(() => setBusy(false));
  };

  const cancelSDK = () => {
    const activeSession = session();
    const current = orderRef.current;
    if (
      !activeSession ||
      !current ||
      !isTopUp(current) ||
      current.checkoutKind !== "android_sdk" ||
      current.sdk?.state !== "awaiting_token" ||
      busy
    )
      return;
    const epoch = sessionEpoch();
    setBusy(true);
    setError("");
    void api<{ order: PaymentOrder }>(
      `/billing/native/orders/${current.id}/cancel`,
      { method: "POST", body: {} },
    )
      .then(async () => {
        if (
          currentSession(
            activeSession.account.id,
            activeSession.session.id,
            epoch,
          )
        ) {
          const latest = await inspectOrder(current.id);
          if (!terminal(latest)) await refreshAggregates();
        }
      })
      .catch(async (cause) => {
        if (
          currentSession(
            activeSession.account.id,
            activeSession.session.id,
            epoch,
          )
        ) {
          setError(billingError(cause));
          await inspectOrder(current.id).catch(() => undefined);
        }
      })
      .finally(() => setBusy(false));
  };

  const submit = async () => {
    if (
      request.current ||
      restoring ||
      !billing ||
      (!billing.canPay && !intentRef.current)
    )
      return;
    const activeSession = session();
    if (!activeSession) return;
    const saved = intentRef.current;
    if (saved?.orderId && saved.checkoutKind === "redirect") {
      void inspectOrder(saved.orderId).catch((cause) =>
        setError(billingError(cause)),
      );
      return;
    }
    const amountMinor = saved?.amountMinor ?? parseRubles(amount);
    if (amountMinor < minTopUpMinor || amountMinor > maxTopUpMinor) {
      setError(`Введите сумму от 1 до ${money(maxTopUpMinor)}.`);
      return;
    }
    if (
      !intentRef.current &&
      ((billing.orders as PaymentOrder[]).some(unfinished) ||
        (orderRef.current && unfinished(orderRef.current)))
    ) {
      setError("Сначала проверьте статус существующего заказа.");
      return;
    }
    request.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    setRejected(false);
    try {
      let current = intentRef.current;
      const epoch = sessionEpoch();
      const authEpoch = await native.authEpoch();
      if (
        !currentSession(
          activeSession.account.id,
          activeSession.session.id,
          epoch,
        )
      )
        return;
      if (!current) {
        current = {
          version: 2,
          accountId: activeSession.account.id,
          sessionId: activeSession.session.id,
          idempotencyKey: await native.randomUUID(),
          amountMinor,
          createdAt: new Date().toISOString(),
          checkoutKind: sdkReady(billing, sdkCapabilities)
            ? "android_sdk"
            : "redirect",
        };
        if (!currentSession(current.accountId, current.sessionId, epoch))
          return;
        // The exact account-bound request is encrypted before any network write.
        await keepIntent(current);
      }
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (current.orderId) {
        const known = await inspectOrder(current.orderId);
        if (
          !currentSession(current.accountId, current.sessionId, epoch) ||
          !isTopUp(known) ||
          known.sdk?.state !== "awaiting_token"
        )
          return;
      }
      const sdkCheckout = current.checkoutKind === "android_sdk";
      const result = await api<{ order: PaymentOrder; sdk?: SDKQuote }>(
        sdkCheckout ? "/billing/native/topups" : "/billing/topups",
        {
          method: "POST",
          body: {
            amountMinor: current.amountMinor,
            idempotencyKey: current.idempotencyKey,
            ...(sdkCheckout ? {} : { returnTarget: "android" }),
          },
        },
      );
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (
        !uuid.test(result.order.id) ||
        !isTopUp(result.order) ||
        result.order.amountMinor !== current.amountMinor ||
        result.order.checkoutKind !== current.checkoutKind ||
        (current.orderId && current.orderId !== result.order.id)
      )
        throw new Error("Invalid order");
      if (
        orderRef.current?.id === result.order.id &&
        terminal(orderRef.current)
      )
        return;
      await keepIntent({ ...current, orderId: result.order.id });
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (
        orderRef.current?.id === result.order.id &&
        terminal(orderRef.current)
      ) {
        await keepIntent(null);
        return;
      }
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      applyOrder(result.order, "write");
      if (!terminal(orderRef.current!)) await refresh();
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (
        sdkCheckout &&
        result.order.sdk?.state === "awaiting_token" &&
        orderRef.current?.id === result.order.id &&
        orderRef.current.sdk?.state === "awaiting_token" &&
        result.sdk
      )
        await tokenize(
          result.order,
          result.sdk,
          { ...current, orderId: result.order.id },
          epoch,
          authEpoch,
        );
      else if (
        !sdkCheckout &&
        result.order.status === "pending" &&
        result.order.confirmationUrl &&
        validPaymentURL(result.order.confirmationUrl)
      )
        await native.openCustomTab(result.order.confirmationUrl);
    } catch (cause) {
      if (mounted.current) {
        const latest = orderRef.current;
        setError(
          latest &&
            isTopUp(latest) &&
            latest.checkoutKind === "android_sdk" &&
            latest.sdk?.state !== "awaiting_token"
            ? ""
            : intentRef.current
              ? "Ответ заказа не получен. Проверьте статус и повторите только сохранённый запрос."
              : billingError(cause),
        );
        if (
          latest &&
          isTopUp(latest) &&
          latest.checkoutKind === "android_sdk" &&
          latest.sdk?.state !== "awaiting_token"
        )
          setNotice(orderMessage(latest));
        setRejected(
          cause instanceof ApiError &&
            [400, 403, 404, 409, 410, 422].includes(cause.status),
        );
      }
    } finally {
      request.current = false;
      if (mounted.current) setBusy(false);
    }
  };

  const checkOrder = () => {
    const id = orderRef.current?.id ?? intentRef.current?.orderId;
    if (!id) {
      void refresh();
      return;
    }
    setBusy(true);
    setError("");
    void inspectOrder(id)
      .then((latest) => (terminal(latest) ? undefined : refreshAggregates()))
      .catch((cause) => setError(billingError(cause)))
      .finally(() => setBusy(false));
  };

  const cancelRejected = () => {
    if (!rejected || !intentRef.current || busy) return;
    setBusy(true);
    void api<Billing>("/billing")
      .then(async (latest) => {
        if ((latest.orders as PaymentOrder[]).some(unfinished)) {
          setBilling(latest);
          setError(
            "Есть незавершённый заказ. Проверьте его статус перед новым оформлением.",
          );
          return;
        }
        await keepIntent(null);
        setRejected(false);
        setError("");
        setNotice("Попытка отменена. Можно начать новое пополнение.");
        await refresh();
      })
      .catch((cause) => setError(billingError(cause)))
      .finally(() => setBusy(false));
  };

  const current = intent ?? null;
  const openOrder = order && unfinished(order) ? order : null;
  const canResumeSDK =
    current?.checkoutKind === "android_sdk" &&
    current.orderId &&
    openOrder?.id === current.orderId &&
    openOrder.sdk?.state === "awaiting_token";
  const nativeAvailable = current
    ? current.checkoutKind === "android_sdk"
    : !!billing && sdkReady(billing, sdkCapabilities);
  const testPayment = nativeAvailable
    ? sdkCapabilities?.environment === "test"
    : billing?.test;
  const rate = balance ? hourlySpend(balance) : 0;
  return (
    <Page title="Баланс" refresh={refresh}>
      <Card>
        <Title>{balance ? balanceMoney(balance) : "Загружаем баланс…"}</Title>
        {balance ? (
          <>
            <Label muted>
              {money(balance.pricePerMonitorMinor)} за монитор за полный
              календарный месяц. Неполный час оплачивается пропорционально
              времени работы.
            </Label>
            <DetailRow
              label="Включено мониторов"
              value={String(balance.enabledMonitors)}
            />
            {balance.enabledMonitors > 0 &&
            hasFunds(balance) &&
            !balance.suspended ? (
              <>
                <Label muted>
                  Расход при текущем числе включённых мониторов
                </Label>
                <DetailRow label="За час ≈" value={estimate(rate)} />
                <DetailRow label="За сутки ≈" value={estimate(rate * 24)} />
                {balance.fundedUntil ? (
                  <Label>
                    Хватит примерно до {dateTime(balance.fundedUntil)}
                  </Label>
                ) : null}
              </>
            ) : (
              <Label muted>
                {hasFunds(balance)
                  ? "Нет расхода на мониторы."
                  : "Для проверок пополните баланс."}
              </Label>
            )}
          </>
        ) : null}
      </Card>
      {openOrder ? (
        <Card>
          <Title>
            {openOrder.test || openOrder.provider === "mock"
              ? "Тестовый платёж"
              : "Текущий платёж"}
          </Title>
          <Label>{orderMessage(openOrder)}</Label>
          <Label>Сумма: {money(openOrder.amountMinor)}</Label>
          <Button
            label={currentDetails ? "Скрыть детали" : "Детали заказа"}
            onPress={() => setCurrentDetails((value) => !value)}
            secondary
          />
          {currentDetails ? (
            <Label>ID для поддержки: {openOrder.id}</Label>
          ) : null}
          {unfinished(openOrder) ? (
            <Button
              label="Проверить статус"
              onPress={checkOrder}
              disabled={busy}
              secondary
            />
          ) : null}
          {!native.DEV_CLIENT &&
          openOrder.checkoutKind === "android_sdk" &&
          openOrder.sdk?.state === "confirmation_required" ? (
            <Button
              label="Подтвердить в банке"
              onPress={confirmSDK}
              disabled={busy}
              secondary
            />
          ) : null}
          {!native.DEV_CLIENT &&
          openOrder.checkoutKind === "android_sdk" &&
          openOrder.sdk?.state === "awaiting_token" ? (
            <Button
              label="Отменить этот заказ"
              onPress={cancelSDK}
              disabled={busy}
              secondary
            />
          ) : null}
          {!native.DEV_CLIENT &&
          openOrder.checkoutKind !== "android_sdk" &&
          openOrder.status === "pending" &&
          openOrder.confirmationUrl &&
          validPaymentURL(openOrder.confirmationUrl) ? (
            <Button
              label="Продолжить оплату"
              onPress={() =>
                void native
                  .openCustomTab(openOrder.confirmationUrl!)
                  .catch((cause) => setError(billingError(cause)))
              }
              disabled={busy}
              secondary
            />
          ) : null}
        </Card>
      ) : null}
      {retained.draft ? (
        <Card>
          <Title>Черновик монитора сохранён</Title>
          <Label>
            Вернитесь, чтобы сохранить настройки и выбрать, будет ли монитор
            включён.
          </Label>
          <Button
            label="Вернуться к настройке монитора"
            iconName="arrow-left"
            secondary
            onPress={onContinueMonitor}
          />
        </Card>
      ) : null}
      <Card>
        <Title>Пополнить баланс</Title>
        {native.DEV_CLIENT || (!billing?.canPay && !current) ? (
          <Label muted>Пополнение сейчас недоступно.</Label>
        ) : (
          <>
            <Label muted>
              {testPayment
                ? "Тестовый платёж без списания с карты."
                : "Пополнение через ЮKassa. Автоматических списаний с карты нет."}
            </Label>
            {current ? (
              <Label>Сохранённый запрос на {money(current.amountMinor)}.</Label>
            ) : null}
            {!current && !openOrder ? (
              <>
                <View
                  style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}
                >
                  {quickTopUps.map((rubles) => (
                    <Button
                      key={rubles}
                      label={money(rubles * 100)}
                      secondary={parseRubles(amount) !== rubles * 100}
                      onPress={() => setAmount(String(rubles))}
                      disabled={busy || restoring}
                    />
                  ))}
                </View>
                <Field
                  label="Сумма, ₽"
                  value={amount}
                  onChangeText={setAmount}
                  keyboardType="numeric"
                  maxLength={9}
                />
              </>
            ) : null}
            {(!openOrder || current) && (!current?.orderId || canResumeSDK) ? (
              <Button
                label={
                  current
                    ? "Продолжить оформление"
                    : testPayment
                      ? "Начать тестовый платёж"
                      : nativeAvailable
                        ? "Пополнить картой"
                        : "Перейти к пополнению"
                }
                disabled={restoring || busy}
                onPress={() => void submit()}
              />
            ) : null}
            {rejected && current ? (
              <Button
                label="Отменить отклонённую попытку"
                secondary
                disabled={busy}
                onPress={cancelRejected}
              />
            ) : null}
          </>
        )}
      </Card>
      {order && !unfinished(order) ? (
        <Card>
          <Label>{orderMessage(order)}</Label>
          <Button
            label={currentDetails ? "Скрыть детали" : "Детали платежа"}
            onPress={() => setCurrentDetails((value) => !value)}
            secondary
          />
          {currentDetails ? (
            <Label muted>Номер для поддержки {order.id}</Label>
          ) : null}
        </Card>
      ) : null}
      <Card>
        <Title>История баланса</Title>
        <ErrorNotice message={historyError} />
        {history?.entries.map((entry) => (
          <View
            key={entry.id}
            style={{
              flexDirection: "row",
              justifyContent: "space-between",
              alignItems: "center",
              gap: 8,
            }}
          >
            <View style={{ flex: 1 }}>
              <Label>
                {entry.test && entry.kind === "top_up"
                  ? "Тестовое пополнение"
                  : entryLabels[entry.kind]}
              </Label>
              <Label muted>{dateTime(entry.createdAt)}</Label>
            </View>
            <Label>{entryAmount(entry)}</Label>
          </View>
        ))}
        {history && !history.entries.length ? (
          <Label muted>Операций пока нет.</Label>
        ) : null}
        {!history && historyBusy ? (
          <Label muted>Загружаем историю…</Label>
        ) : null}
        {history && history.pagination.totalPages > 1 ? (
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "center",
              gap: 8,
            }}
          >
            <Button
              label="Назад"
              secondary
              disabled={historyBusy || historyPage <= 1}
              onPress={() => void loadHistory(historyPage - 1)}
            />
            <Label>
              {historyPage} из {history.pagination.totalPages}
            </Label>
            <Button
              label="Далее"
              secondary
              disabled={
                historyBusy || historyPage >= history.pagination.totalPages
              }
              onPress={() => void loadHistory(historyPage + 1)}
            />
          </View>
        ) : null}
      </Card>
      <ErrorNotice message={error} />
      {notice && (!order || notice !== orderMessage(order)) ? (
        <Label>{notice}</Label>
      ) : null}
    </Page>
  );
}
