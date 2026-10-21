import React, { useCallback, useEffect, useRef, useState } from "react";
import { AppState } from "react-native";
import { ApiError, api, errorText, session, sessionEpoch } from "./api";
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
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const money = (minor: number) => `${(minor / 100).toLocaleString("ru-RU")} ₽`;
const date = (value?: string | null) =>
  value ? new Date(value).toLocaleDateString("ru-RU") : "—";
const terminal = (order: PaymentOrder) =>
  ["canceled", "failed"].includes(order.status) ||
  (order.status === "succeeded" && order.fulfillmentStatus === "fulfilled");
const unfinished = (order: PaymentOrder) => !terminal(order);
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
    return "Платёж получен, начисление ещё проверяется. Повторите проверку статуса.";
  return "Заказ завершён без начисления. Проверьте список транзакций.";
}

export function BillingScreen({
  returnOrder,
}: {
  returnOrder?: string | null;
}) {
  const [billing, setBilling] = useState<NativeBilling | null>(null);
  const [sdkCapabilities, setSDKCapabilities] =
    useState<SDKCapabilities | null>(null);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [count, setCount] = useState("1");
  const [kind, setKind] = useState<"add" | "renew">("add");
  const [selected, setSelected] = useState<Subscription | null>(null);
  const [order, setOrder] = useState<PaymentOrder | null>(null);
  const [intent, setIntent] = useState<PaymentIntent | null>(null);
  const [restoring, setRestoring] = useState(true);
  const [busy, setBusy] = useState(false);
  const [rejected, setRejected] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const request = useRef(false);
  const intentRef = useRef<PaymentIntent | null>(null);
  const orderRef = useRef<PaymentOrder | null>(null);
  const rejectedReturn = useRef<string | null>(null);
  const mounted = useRef(true);

  const keepIntent = useCallback(async (value: PaymentIntent | null) => {
    if (value) await native.savePaymentIntent(value);
    else await native.clearPaymentIntent();
    intentRef.current = value;
    if (mounted.current) setIntent(value);
  }, []);

  const inspectOrder = useCallback(
    async (id: string) => {
      if (!uuid.test(id)) throw new Error("Invalid order");
      const result = await api<{ order: PaymentOrder }>(
        `/billing/orders/${id}`,
      );
      if (result.order.id.toLowerCase() !== id.toLowerCase())
        throw new Error("Unexpected order");
      orderRef.current = result.order;
      if (mounted.current) {
        setOrder(result.order);
        setNotice(orderMessage(result.order));
      }
      const current = intentRef.current;
      if (
        current &&
        current.orderId === result.order.id &&
        terminal(result.order)
      )
        await keepIntent(null);
      return result.order;
    },
    [keepIntent],
  );

  const refresh = useCallback(async () => {
    try {
      const [overview, access] = await Promise.all([
        api<NativeBilling>("/billing"),
        api<{ entitlement: Entitlement }>("/entitlement"),
      ]);
      if (!mounted.current) return overview;
      setBilling(overview);
      setEntitlement(access.entitlement);
      setError("");
      const wanted =
        intentRef.current?.orderId ??
        (returnOrder &&
        uuid.test(returnOrder) &&
        rejectedReturn.current !== returnOrder
          ? returnOrder
          : null);
      if (wanted) {
        try {
          await inspectOrder(wanted);
        } catch (cause) {
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
      if (!orderRef.current) {
        const pending = (overview.orders as PaymentOrder[]).find(unfinished);
        if (pending) {
          orderRef.current = pending;
          setOrder(pending);
          setNotice(orderMessage(pending));
        }
      }
      return overview;
    } catch (cause) {
      if (mounted.current) setError(errorText(cause));
      return null;
    }
  }, [inspectOrder, returnOrder]);

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
        if (mounted.current) setError(errorText(cause));
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
      if (state === "active") void refresh();
    });
    return () => {
      mounted.current = false;
      listener.remove();
    };
  }, [refresh]);

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
      orderRef.current = attached.order;
      if (mounted.current) {
        setOrder(attached.order);
        setNotice(orderMessage(attached.order));
      }
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
      .catch((cause) => setError(errorText(cause)))
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
          await inspectOrder(current.id);
          await refresh();
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
          setError(errorText(cause));
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
        setError(errorText(cause)),
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
      await keepIntent({ ...current, orderId: result.order.id });
      if (!currentSession(current.accountId, current.sessionId, epoch)) return;
      orderRef.current = result.order;
      if (mounted.current) {
        setOrder(result.order);
        setNotice(orderMessage(result.order));
      }
      await refresh();
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
              : errorText(cause),
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
      .then(() => refresh())
      .catch((cause) => setError(errorText(cause)))
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
      .catch((cause) => setError(errorText(cause)))
      .finally(() => setBusy(false));
  };

  const current = intent ?? null;
  const openOrder =
    order ??
    (billing?.orders as PaymentOrder[] | undefined)?.find(unfinished) ??
    null;
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
    <Page title="Оплата" refresh={() => void refresh()}>
      <Card>
        <Title>Доступность мониторов</Title>
        <Label>
          Включено: {entitlement?.enabledMonitorCount ?? "—"} · Доступная квота:{" "}
          {entitlement?.quota ?? "—"}
        </Label>
        <Label>Административная квота: {entitlement?.admin?.quota ?? 0}</Label>
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
        {entitlement?.packages.map((item) => (
          <Card key={item.id}>
            <Label>
              {item.quota} монитор(а) · до {date(item.activeUntil)} ·{" "}
              {item.active ? "активна" : "истекла"}
            </Label>
            {["yookassa", "yookassa_test"].includes(item.source) ? (
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
        {!entitlement?.packages.length ? (
          <Label muted>Оплаченных транзакций пока нет.</Label>
        ) : null}
      </Card>
      <Card>
        <Title>Новый заказ</Title>
        {current ? (
          <Label>
            Сохранён запрос:{" "}
            {current.purchaseKind === "renew" ? "продление" : "покупка"}{" "}
            {current.monitorCount} монитор(а). Повтор создаст тот же заказ.
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
          <Label muted>Продление выбранной транзакции сейчас недоступно.</Label>
        ) : null}
        <Button
          label={
            current
              ? canResumeSDK
                ? "Продолжить оплату картой"
                : "Повторить сохранённый запрос"
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
      {openOrder ? (
        <Card>
          <Title>Заказ</Title>
          <Label>№ {openOrder.id}</Label>
          <Label>{orderMessage(openOrder)}</Label>
          <Label>Сумма: {money(openOrder.amountMinor)}</Label>
          <Button
            label="Проверить статус на сервере"
            onPress={checkOrder}
            disabled={busy}
            secondary
          />
          {openOrder.checkoutKind === "android_sdk" &&
          openOrder.sdk?.state === "confirmation_required" ? (
            <Button
              label="Подтвердить в банке"
              onPress={confirmSDK}
              disabled={busy}
              secondary
            />
          ) : null}
          {openOrder.checkoutKind === "android_sdk" &&
          openOrder.sdk?.state === "awaiting_token" ? (
            <Button
              label="Отменить этот заказ"
              onPress={cancelSDK}
              disabled={busy}
              secondary
            />
          ) : null}
          {openOrder.checkoutKind !== "android_sdk" &&
          openOrder.status === "pending" &&
          openOrder.confirmationUrl &&
          validPaymentURL(openOrder.confirmationUrl) ? (
            <Button
              label="Продолжить оплату"
              onPress={() =>
                void native.openCustomTab(openOrder.confirmationUrl!)
              }
              disabled={busy}
              secondary
            />
          ) : null}
        </Card>
      ) : null}
      {billing?.orders.length ? (
        <Card>
          <Title>История заказов</Title>
          {(billing.orders as PaymentOrder[]).map((item) => (
            <Card key={item.id}>
              <Label>
                № {item.id.slice(0, 8)} · {date(item.createdAt)} ·{" "}
                {money(item.amountMinor)}
              </Label>
              <Label>{orderMessage(item)}</Label>
              <Button
                label="Проверить"
                secondary
                onPress={() =>
                  void inspectOrder(item.id).catch((cause) =>
                    setError(errorText(cause)),
                  )
                }
              />
            </Card>
          ))}
        </Card>
      ) : null}
      <ErrorNotice message={error} />
      {notice ? <Label>{notice}</Label> : null}
    </Page>
  );
}
