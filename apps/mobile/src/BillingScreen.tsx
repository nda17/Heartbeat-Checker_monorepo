import React, { useCallback, useEffect, useRef, useState } from "react";
import { AppState, Pressable, View } from "react-native";
import { ApiError, api, errorText, session, sessionEpoch } from "./api";
import { useMonitorDraft } from "./MonitorDraft";
import { native, type PaymentIntent } from "./native";
import type { Billing, Entitlement, Order, Subscription } from "./types";
import { Button, Card, ErrorNotice, Field, Label, Page, Title } from "./ui";

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
type OrderPage = {
  orders: PaymentOrder[];
  pagination: {
    page: number;
    pageSize: number;
    total: number;
    totalPages: number;
  };
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
const money = (minor: number) => `${(minor / 100).toLocaleString("ru-RU")} ₽`;
const date = (value?: string | null) =>
  value ? new Date(value).toLocaleDateString("ru-RU") : "—";
const dateTime = (value?: string | null) =>
  value
    ? new Date(value).toLocaleString("ru-RU", {
        day: "2-digit",
        month: "2-digit",
        year: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
const terminal = (order: PaymentOrder) =>
  ["canceled", "failed"].includes(order.status) ||
  (order.status === "succeeded" &&
    ["fulfilled", "blocked", "not_required"].includes(order.fulfillmentStatus));
const unfinished = (order: PaymentOrder) => !terminal(order);
const pollable = (order: PaymentOrder) =>
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
function checkoutRoute(
  overview: NativeBilling,
  capabilities: SDKCapabilities | null,
  kind: "add" | "renew",
  selected: Subscription | null,
): PaymentIntent["checkoutKind"] | null {
  const available = sdkReady(overview, capabilities);
  if (kind === "add") return available ? "android_sdk" : "redirect";
  if (selected?.source === "yookassa")
    return available && overview.nativeCheckout?.environment === "live"
      ? "android_sdk"
      : "redirect";
  if (selected?.source === "yookassa_test")
    return available && overview.nativeCheckout?.environment === "test"
      ? "android_sdk"
      : null;
  return null;
}
function validSDKQuote(
  quote: SDKQuote,
  order: PaymentOrder,
  capabilities: SDKCapabilities,
) {
  return (
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
  if (paid(order)) return "Оплата и начисление подтверждены сервером.";
  if (order.status === "succeeded" && order.fulfillmentStatus === "blocked")
    return "Платёж получен, но квота не начислена. Обратитесь в поддержку с ID заказа; повторная оплата не исправит этот заказ.";
  if (
    order.status === "succeeded" &&
    order.fulfillmentStatus === "not_required"
  )
    return "Платёж подтверждён. По этому заказу новая квота не начисляется.";
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
  return "Заказ завершён без начисления. Проверьте список транзакций.";
}

function statusLabel(order: PaymentOrder) {
  if (paid(order)) return "Оплачен · квота начислена";
  if (order.status === "succeeded" && order.fulfillmentStatus === "blocked")
    return "Оплачен · квота не начислена";
  if (
    order.status === "succeeded" &&
    order.fulfillmentStatus === "not_required"
  )
    return "Оплачен · без новой квоты";
  if (["manual_review", "stopped"].includes(order.reconciliationStatus ?? ""))
    return "Нужна проверка";
  if (order.status === "succeeded") return "Оплачен · начисление ожидается";
  if (order.status === "canceled") return "Отменён";
  if (order.status === "failed") return "Не прошёл";
  return "Обрабатывается";
}

export function BillingScreen({
  returnOrder,
  onContinueMonitor,
}: {
  onContinueMonitor: () => void;
  returnOrder?: string | null;
}) {
  const retained = useMonitorDraft();
  const [billing, setBilling] = useState<NativeBilling | null>(null);
  const [sdkCapabilities, setSDKCapabilities] =
    useState<SDKCapabilities | null>(null);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [count, setCount] = useState("1");
  const [kind, setKind] = useState<"add" | "renew">("add");
  const [selected, setSelected] = useState<Subscription | null>(null);
  const [order, setOrder] = useState<PaymentOrder | null>(null);
  const [history, setHistory] = useState<OrderPage | null>(null);
  const [historyPage, setHistoryPage] = useState(1);
  const [packagePage, setPackagePage] = useState(1);
  const [expandedHistoryId, setExpandedHistoryId] = useState<string | null>(
    null,
  );
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
    if (value) await native.savePaymentIntent(value);
    else await native.clearPaymentIntent();
    intentRef.current = value;
    if (mounted.current) setIntent(value);
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
    try {
      const result = await api<OrderPage>(
        `/billing/orders?page=${page}&pageSize=10`,
        { signal: controller.signal },
      );
      if (
        controller.signal.aborted ||
        read !== historyRequest.current ||
        !mounted.current ||
        !currentSession(actor.account.id, actor.session.id, epoch)
      )
        return;
      if (result.pagination.page !== historyPageRef.current)
        setExpandedHistoryId(null);
      historyPageRef.current = result.pagination.page;
      setHistoryPage(result.pagination.page);
      setHistory(result);
    } catch (cause) {
      if (!controller.signal.aborted) throw cause;
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
      const [overview, access] = await Promise.all([
        api<NativeBilling>("/billing"),
        api<{ entitlement: Entitlement }>("/entitlement"),
        loadHistory(historyPageRef.current),
      ]);
      if (
        !mounted.current ||
        read !== refreshRequest.current ||
        !currentSession(actor.account.id, actor.session.id, epoch)
      )
        return null;
      setBilling(overview);
      setEntitlement(access.entitlement);
      setSelected((current) =>
        current
          ? (access.entitlement.packages.find(
              (item) => item.id === current.id,
            ) ?? null)
          : null,
      );
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
  }, [loadHistory]);

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
        (returnOrder &&
        uuid.test(returnOrder) &&
        rejectedReturn.current !== returnOrder
          ? returnOrder
          : null);
      if (wanted) {
        try {
          await inspectOrder(wanted);
        } catch (cause) {
          if (!stillCurrent()) return null;
          if (
            wanted !== returnOrder ||
            intentRef.current ||
            !(cause instanceof ApiError) ||
            ![403, 404].includes(cause.status)
          )
            throw cause;
          rejectedReturn.current = returnOrder;
          setError("Ссылка на заказ недоступна этому аккаунту.");
        }
      }
      if (!stillCurrent()) return null;
      if (
        !orderRef.current ||
        (terminal(orderRef.current) && !intentRef.current)
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
  }, [applyOrder, inspectOrder, refreshAggregates, returnOrder]);

  useEffect(() => {
    mounted.current = true;
    void (async () => {
      try {
        const saved = await native.restorePaymentIntent();
        if (!mounted.current) return;
        intentRef.current = saved;
        setIntent(saved);
        if (saved) {
          setKind(saved.purchaseKind);
          setCount(String(saved.monitorCount));
        }
      } catch (cause) {
        if (mounted.current) setError(billingError(cause));
      }
      try {
        const capabilities = await native.sdkCapabilities();
        if (mounted.current) setSDKCapabilities(capabilities);
      } catch {
        if (mounted.current) setSDKCapabilities(null);
      } finally {
        if (mounted.current) {
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
    const quantity =
      saved?.monitorCount ??
      (kind === "renew" ? selected?.quota : Number(count));
    const packageId =
      saved?.packageId ?? (kind === "renew" ? (selected?.id ?? "") : "");
    if (
      !quantity ||
      !Number.isInteger(quantity) ||
      quantity < 1 ||
      quantity > 1000 ||
      ((saved?.purchaseKind ?? kind) === "renew" && !uuid.test(packageId))
    ) {
      setError(
        "Укажите количество от 1 до 1000 и выберите транзакцию для продления.",
      );
      return;
    }
    if (
      !intentRef.current &&
      (!Number.isSafeInteger(billing.priceVersion) ||
        billing.priceVersion < 1 ||
        !Number.isSafeInteger(billing.pricePerMonitorMinor) ||
        billing.pricePerMonitorMinor < 100 ||
        billing.pricePerMonitorMinor > 1_000_000 ||
        quantity * billing.pricePerMonitorMinor > 35_000_000)
    ) {
      setError(
        "Цена недоступна или сумма заказа превышает 350 000 ₽. Обновите условия.",
      );
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
        const route = checkoutRoute(billing, sdkCapabilities, kind, selected);
        if (!route) {
          setError("Продление этой транзакции сейчас недоступно.");
          return;
        }
        current = {
          accountId: activeSession.account.id,
          sessionId: activeSession.session.id,
          idempotencyKey: await native.randomUUID(),
          purchaseKind: kind,
          packageId,
          monitorCount: quantity,
          priceVersion: billing.priceVersion,
          createdAt: new Date().toISOString(),
          checkoutKind: route,
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
          known.sdk?.state !== "awaiting_token"
        )
          return;
      }
      const sdkCheckout = current.checkoutKind === "android_sdk";
      const result = await api<{ order: PaymentOrder; sdk?: SDKQuote }>(
        sdkCheckout ? "/billing/native/orders" : "/billing/orders",
        {
          method: "POST",
          body: {
            monitorCount: current.monitorCount,
            idempotencyKey: current.idempotencyKey,
            priceVersion: current.priceVersion,
            purchaseKind: current.purchaseKind === "add" ? "purchase" : "renew",
            packageId: current.packageId,
            ...(sdkCheckout ? {} : { returnTarget: "android" }),
          },
        },
      );
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      if (
        !uuid.test(result.order.id) ||
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
          latest?.checkoutKind === "android_sdk" &&
            latest.sdk?.state !== "awaiting_token"
            ? ""
            : intentRef.current
              ? "Ответ заказа не получен. Проверьте статус и повторите только сохранённый запрос."
              : billingError(cause),
        );
        if (
          latest?.checkoutKind === "android_sdk" &&
          latest.sdk?.state !== "awaiting_token"
        )
          setNotice(orderMessage(latest));
        setRejected(
          cause instanceof ApiError && [400, 403, 409].includes(cause.status),
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
        setNotice("Попытка отклонена. Обновите цену перед новым заказом.");
        await refresh();
      })
      .catch((cause) => setError(billingError(cause)))
      .finally(() => setBusy(false));
  };

  const current = intent ?? null;
  const openOrder = order && unfinished(order) ? order : null;
  const packages = entitlement?.packages ?? [];
  const packagePages = Math.max(1, Math.ceil(packages.length / 10));
  const shownPackagePage = Math.min(packagePage, packagePages);
  const visiblePackages = packages.slice(
    (shownPackagePage - 1) * 10,
    shownPackagePage * 10,
  );
  const historyRows =
    history?.orders
      .filter((item) => item.id !== openOrder?.id)
      .map((item) =>
        item.id === order?.id && order && terminal(order) ? order : item,
      ) ?? [];
  const validQuote =
    !!billing &&
    Number.isSafeInteger(billing.pricePerMonitorMinor) &&
    billing.pricePerMonitorMinor >= 100 &&
    billing.pricePerMonitorMinor <= 1_000_000 &&
    Number.isSafeInteger(billing.priceVersion) &&
    billing.priceVersion > 0;
  const quantity =
    current?.monitorCount ??
    (kind === "renew" ? (selected?.quota ?? 0) : Number(count) || 0);
  const canResumeSDK =
    current?.checkoutKind === "android_sdk" &&
    current.orderId &&
    openOrder?.id === current.orderId &&
    openOrder.sdk?.state === "awaiting_token";
  const route = billing
    ? checkoutRoute(billing, sdkCapabilities, kind, selected)
    : null;
  const nativeAvailable = (current?.checkoutKind ?? route) === "android_sdk";
  return (
    <Page title="Оплата" refresh={refresh}>
      {openOrder ? (
        <Card>
          <Title>Текущий заказ</Title>
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
            {entitlement && entitlement.quota > entitlement.enabledMonitorCount
              ? "Квота доступна. Вернитесь к настройкам и нажмите «Запустить монитор»."
              : "Завершите покупку, затем вернитесь к настройке. Монитор не запустится автоматически."}
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
        <Title>Доступность мониторов</Title>
        <Label>
          Включено: {entitlement?.enabledMonitorCount ?? "—"} · Доступная квота:{" "}
          {entitlement?.quota ?? "—"}
        </Label>
        {session()?.account.role === "admin" && entitlement?.admin ? (
          <Label>Административная квота: {entitlement.admin.quota}</Label>
        ) : null}
        <Label>
          Цена:{" "}
          {validQuote
            ? money(billing!.pricePerMonitorMinor)
            : "обновите условия"}{" "}
          за монитор на календарный месяц.
        </Label>
        <Label>
          Каждая покупка создаёт отдельную транзакцию. Продление меняет срок
          только выбранной транзакции.
        </Label>
      </Card>
      <Card>
        <Title>Транзакции</Title>
        {visiblePackages.map((item) => (
          <Card key={item.id}>
            <Label>
              {item.quota} монитор(а) · до {date(item.activeUntil)} ·{" "}
              {item.active ? "активна" : "истекла"}
            </Label>
            {!native.DEV_CLIENT &&
            ["yookassa", "yookassa_test"].includes(item.source) ? (
              <Button
                label={
                  selected?.id === item.id
                    ? "Выбрана для продления"
                    : "Продлить эту транзакцию"
                }
                onPress={() => {
                  setKind("renew");
                  setSelected(item);
                }}
                disabled={!!current || busy}
                secondary={selected?.id !== item.id}
              />
            ) : null}
          </Card>
        ))}
        {!packages.length ? (
          <Label muted>Оплаченных транзакций пока нет.</Label>
        ) : null}
        {packagePages > 1 ? (
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
              disabled={shownPackagePage <= 1}
              onPress={() => setPackagePage(shownPackagePage - 1)}
            />
            <Label>
              {shownPackagePage} из {packagePages}
            </Label>
            <Button
              label="Далее"
              secondary
              disabled={shownPackagePage >= packagePages}
              onPress={() => setPackagePage(shownPackagePage + 1)}
            />
          </View>
        ) : null}
      </Card>
      {native.DEV_CLIENT ? (
        <Card>
          <Title>Новый заказ</Title>
          <Label muted>
            Оплата в dev-сборке отключена. Данные и история доступны для
            просмотра.
          </Label>
        </Card>
      ) : (
        <Card>
          <Title>Новый заказ</Title>
          {current ? (
            <Label>
              Оформление сохранено:{" "}
              {current.purchaseKind === "renew" ? "продление" : "покупка"}{" "}
              {current.monitorCount} монитор(а). Продолжение не создаст второй
              заказ.
            </Label>
          ) : null}
          <Button
            label="Добавить мониторы"
            secondary={kind !== "add"}
            disabled={!!current || busy}
            onPress={() => {
              setKind("add");
              setSelected(null);
            }}
          />
          <Button
            label="Продлить выбранную транзакцию"
            secondary={kind !== "renew"}
            disabled={!selected || !!current || busy}
            onPress={() => setKind("renew")}
          />
          {kind === "add" && !current ? (
            <Field
              label="Количество мониторов"
              value={count}
              onChangeText={setCount}
              keyboardType="number-pad"
            />
          ) : (
            <Label>
              {selected
                ? `${selected.quota} монитор(а) · до ${date(selected.activeUntil)}`
                : current
                  ? `${current.monitorCount} монитор(а)`
                  : "Выберите транзакцию выше."}
            </Label>
          )}
          <Label>
            Сумма:{" "}
            {validQuote && quantity > 0
              ? money(billing!.pricePerMonitorMinor * quantity)
              : "—"}
          </Label>
          {!billing?.canPay ? (
            <Label muted>Оплата сейчас недоступна для этого аккаунта.</Label>
          ) : null}
          {kind === "renew" && selected && !current && !route ? (
            <Label muted>
              Продление выбранной транзакции сейчас недоступно.
            </Label>
          ) : null}
          <Button
            label={
              current
                ? canResumeSDK
                  ? "Продолжить оплату картой"
                  : "Продолжить оформление"
                : nativeAvailable
                  ? "Оплатить картой"
                  : "Перейти к оплате ЮKassa"
            }
            disabled={
              restoring ||
              busy ||
              (!billing?.canPay && !current) ||
              (!current && !route) ||
              (!!current?.orderId && !canResumeSDK) ||
              (!!openOrder && unfinished(openOrder) && !current) ||
              (!current && !validQuote)
            }
            onPress={() => void submit()}
          />
          {rejected && current ? (
            <Button
              label="Отменить отклонённую попытку"
              secondary
              disabled={busy}
              onPress={cancelRejected}
            />
          ) : null}
        </Card>
      )}
      {order && terminal(order) ? (
        <Label>
          {orderMessage(order)} Детали заказа доступны в истории заказов.
        </Label>
      ) : null}
      {history ? (
        <Card>
          <Title>История заказов</Title>
          {historyRows.map((item) => (
            <Card key={item.id} style={{ padding: 10, gap: 5 }}>
              <Pressable
                accessibilityRole="button"
                accessibilityState={{ expanded: expandedHistoryId === item.id }}
                accessibilityLabel={`${dateTime(item.createdAt)}, ${money(item.amountMinor)}, ${statusLabel(item)}. ${expandedHistoryId === item.id ? "Скрыть детали" : "Подробнее"}`}
                style={{ minHeight: 48, justifyContent: "center", gap: 2 }}
                onPress={() =>
                  setExpandedHistoryId((current) =>
                    current === item.id ? null : item.id,
                  )
                }
              >
                <Label>
                  {dateTime(item.createdAt)} · {money(item.amountMinor)}
                </Label>
                <Label muted>{statusLabel(item)}</Label>
              </Pressable>
              {expandedHistoryId === item.id ? (
                <>
                  <Label>ID для поддержки: {item.id}</Label>
                  <Label>
                    {item.purchaseKind === "renew" ? "Продление" : "Покупка"} ·{" "}
                    {item.monitorCount} монитор(а)
                  </Label>
                  <Label>{orderMessage(item)}</Label>
                </>
              ) : null}
            </Card>
          ))}
          {!historyRows.length ? (
            <Label muted>
              {history.pagination.total
                ? "Текущий заказ показан выше."
                : "Заказов пока нет."}
            </Label>
          ) : null}
          {history.pagination.totalPages > 1 ? (
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
                onPress={() =>
                  void loadHistory(historyPage - 1).catch((cause) =>
                    setError(billingError(cause)),
                  )
                }
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
                onPress={() =>
                  void loadHistory(historyPage + 1).catch((cause) =>
                    setError(billingError(cause)),
                  )
                }
              />
            </View>
          ) : null}
        </Card>
      ) : null}
      <ErrorNotice message={error} />
      {notice && (!order || notice !== orderMessage(order)) ? (
        <Label>{notice}</Label>
      ) : null}
    </Page>
  );
}
