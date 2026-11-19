"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  Balance,
  BalanceEntry,
  BalanceHistory,
  balanceEntryLabels,
  balanceMoney,
  hasFunds,
  hourlySpend,
  invalidateBalance,
  maxTopUpMinor,
  minTopUpMinor,
  parseRubles,
  priceMoney,
  quickTopUps,
  spendingEstimate,
} from "@/entities/billing";
import { useSession } from "@/entities/session";
import {
  api,
  ApiError,
  boundedRequest,
  errorMessage,
  paymentConfirmationUrl,
} from "@/shared/api";
import { dateTime, Loading, Notice } from "@/shared/ui";

type Order = {
  id: string;
  purchaseKind: string;
  provider: "mock" | "yookassa";
  status: string;
  reconciliationStatus?: "pending" | "complete" | "manual_review" | "stopped";
  fulfillmentStatus?: "pending" | "fulfilled" | "blocked" | "not_required";
  amountMinor: number;
  confirmationUrl?: string;
  test?: boolean;
};
type Billing = {
  provider: "disabled" | "mock" | "yookassa";
  canPay?: boolean;
  test?: boolean;
  orders: Order[];
};
type Intent = { amountMinor: number; idempotencyKey: string };

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const isTopUp = (order: Order) => order.purchaseKind === "topup";
const unfinished = (order: Order) =>
  isTopUp(order) &&
  (order.status === "pending" ||
    (order.status === "succeeded" && order.fulfillmentStatus === "pending"));
const needsReview = (order: Order) =>
  order.reconciliationStatus === "manual_review" ||
  order.reconciliationStatus === "stopped";

function orderMessage(order: Order) {
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
  if (needsReview(order))
    return "Результат платежа проверяется. При необходимости обратитесь в поддержку.";
  if (order.status === "canceled" || order.status === "failed")
    return demo
      ? "Тестовый платёж отменён."
      : "Платёж не прошёл. Баланс не пополнен.";
  if (order.status === "succeeded") {
    if (order.fulfillmentStatus === "pending")
      return "Платёж подтверждён. Ожидаем пополнение.";
    if (demo) return "Тестовый платёж завершён. Списания с карты не было.";
    if (order.fulfillmentStatus === "fulfilled") return "Баланс пополнен.";
    return "Платёж подтверждён. Для проверки пополнения обратитесь в поддержку.";
  }
  return demo
    ? "Ожидаем результат тестового платежа."
    : "Ожидаем подтверждение платежа.";
}

function entryAmount(entry: BalanceEntry) {
  const sign = entry.kind === "usage" || entry.amountMinor < 0 ? "−" : "+";
  return entry.positiveBelowCent
    ? `${sign} < 0,01 ₽`
    : `${sign}${priceMoney(Math.abs(entry.amountMinor))}`;
}

export function BillingPage() {
  const { profile } = useSession();
  const storageKey = `heartbeat-wallet-topup:${profile.account.id}`;
  const [billing, setBilling] = useState<Billing | null>(null);
  const [balance, setBalance] = useState<Balance | null>(null);
  const [history, setHistory] = useState<BalanceHistory | null>(null);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [historyError, setHistoryError] = useState("");
  const [amount, setAmount] = useState(String(quickTopUps[1]));
  const [intent, setIntent] = useState<Intent | null>(null);
  const [order, setOrder] = useState<Order | null>(null);
  const [orderId, setOrderId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [statusBusy, setStatusBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const creating = useRef<AbortController | null>(null);
  const ledgerRead = useRef<AbortController | null>(null);
  const intentRef = useRef<Intent | null>(null);
  const overviewRead = useRef<AbortController | null>(null);
  const historyPage = useRef(1);
  const orderRef = useRef<Order | null>(null);

  const applyOrder = useCallback((latest: Order) => {
    const current = orderRef.current;
    if (
      current?.id === latest.id &&
      (!unfinished(current) ||
        (current.status === "succeeded" && latest.status === "pending"))
    )
      return;
    orderRef.current = latest;
    setOrder(latest);
    setOrderId(latest.id);
  }, []);

  const loadHistory = useCallback(async (page: number) => {
    ledgerRead.current?.abort();
    const request = new AbortController();
    ledgerRead.current = request;
    setHistoryBusy(true);
    setHistoryError("");
    try {
      const result = await boundedRequest(request, (signal) =>
        api<BalanceHistory>(`/billing/ledger?page=${page}&pageSize=10`, {
          signal,
        }),
      );
      if (ledgerRead.current !== request || request.signal.aborted) return;
      historyPage.current = result.pagination.page;
      setHistory(result);
    } catch (cause) {
      if (ledgerRead.current === request) setHistoryError(errorMessage(cause));
    } finally {
      if (ledgerRead.current === request) {
        ledgerRead.current = null;
        setHistoryBusy(false);
      }
    }
  }, []);

  const refresh = useCallback(async () => {
    overviewRead.current?.abort();
    const request = new AbortController();
    overviewRead.current = request;
    try {
      const [current, overview] = await boundedRequest(request, (signal) =>
        Promise.all([
          api<Balance>("/billing/balance", { signal }),
          api<Billing>("/billing", { signal }),
        ]),
      );
      if (overviewRead.current !== request || request.signal.aborted) return;
      setBilling(overview);
      setBalance(current);
      return overview;
    } catch (cause) {
      if (overviewRead.current === request) setError(errorMessage(cause));
    } finally {
      if (overviewRead.current === request) overviewRead.current = null;
    }
  }, []);

  useEffect(() => {
    let active = true;
    try {
      window.sessionStorage.removeItem(
        `heartbeat-billing-request:${profile.account.id}`,
      );
      const saved: Intent = JSON.parse(
        window.sessionStorage.getItem(storageKey) ?? "null",
      );
      if (
        saved &&
        uuid.test(saved.idempotencyKey) &&
        Number.isSafeInteger(saved.amountMinor) &&
        saved.amountMinor >= minTopUpMinor &&
        saved.amountMinor <= maxTopUpMinor
      ) {
        intentRef.current = saved;
        // eslint-disable-next-line react-hooks/set-state-in-effect -- Recover the immutable request before allowing checkout.
        setIntent(saved);
        setAmount(String(saved.amountMinor / 100));
      }
    } catch {
      // Retry remains available in memory when tab storage is unavailable.
    }
    void refresh().then((overview) => {
      if (!active) return;
      const rawId = new URLSearchParams(window.location.search).get("order");
      const pending = overview?.orders.find(unfinished);
      if (pending) applyOrder(pending);
      else if (rawId && uuid.test(rawId)) setOrderId(rawId.toLowerCase());
      else if (rawId) setStatusError("Неверный номер заказа в ссылке.");
      setReady(true);
    });
    void loadHistory(1);
    const resume = () => {
      if (document.visibilityState === "hidden") return;
      void refresh().then((overview) => {
        if (
          !active ||
          creating.current ||
          (orderRef.current && unfinished(orderRef.current))
        )
          return;
        const pending = overview?.orders.find(unfinished);
        if (pending) applyOrder(pending);
      });
      setAttempt((value) => value + 1);
    };
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible" && !overviewRead.current)
        void refresh();
    }, 30_000);
    window.addEventListener("focus", resume);
    window.addEventListener("pageshow", resume);
    document.addEventListener("visibilitychange", resume);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", resume);
      window.removeEventListener("pageshow", resume);
      document.removeEventListener("visibilitychange", resume);
      const controllers = [
        creating.current,
        overviewRead.current,
        ledgerRead.current,
      ];
      creating.current = overviewRead.current = ledgerRead.current = null;
      controllers.forEach((controller) => controller?.abort());
    };
  }, [profile.account.id, storageKey, applyOrder, loadHistory, refresh]);

  useEffect(() => {
    if (
      !orderId ||
      (orderRef.current?.id === orderId && !isTopUp(orderRef.current))
    )
      return;
    let active = true;
    let controller: AbortController | undefined;
    let timer: number | undefined;
    let attempts = 0;
    async function inspect() {
      if (!active || document.visibilityState === "hidden") return;
      controller = new AbortController();
      setStatusBusy(true);
      setStatusError("");
      try {
        const { order: latest } = await boundedRequest(controller, (signal) =>
          api<{ order: Order }>(`/billing/orders/${orderId}`, { signal }),
        );
        if (!active || (orderRef.current && orderRef.current.id !== orderId))
          return;
        if (latest.id !== orderId)
          throw new Error("Номер платежа не совпал. Повторите проверку.");
        applyOrder(latest);
        if (!isTopUp(latest)) return;
        if (!unfinished(latest)) {
          invalidateBalance();
          await Promise.all([refresh(), loadHistory(historyPage.current)]);
        } else if (!needsReview(latest)) {
          if (++attempts < 10)
            timer = window.setTimeout(() => void inspect(), 3000);
          else
            setStatusError(
              "Платёж ещё обрабатывается. Статус можно проверить позже.",
            );
        }
      } catch (cause) {
        if (!active) return;
        if (
          cause instanceof ApiError &&
          [403, 404].includes(cause.status) &&
          orderRef.current?.id !== orderId
        ) {
          setOrderId(null);
          setStatusError("Ссылка на заказ недоступна этому аккаунту.");
        } else {
          setStatusError(
            controller.signal.aborted
              ? "Проверка платежа заняла слишком много времени. Повторите."
              : errorMessage(cause),
          );
        }
      } finally {
        if (active) setStatusBusy(false);
      }
    }
    void inspect();
    return () => {
      active = false;
      controller?.abort();
      window.clearTimeout(timer);
    };
  }, [orderId, attempt, applyOrder, loadHistory, refresh]);

  function keepIntent(value: Intent | null) {
    intentRef.current = value;
    setIntent(value);
    try {
      if (value)
        window.sessionStorage.setItem(storageKey, JSON.stringify(value));
      else window.sessionStorage.removeItem(storageKey);
    } catch {
      // Use the in-memory intent when storage is blocked.
    }
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      creating.current ||
      !ready ||
      !billing ||
      !billing.canPay ||
      (!intentRef.current && orderId && (!order || unfinished(order)))
    )
      return;
    const amountMinor = intentRef.current?.amountMinor ?? parseRubles(amount);
    if (amountMinor < minTopUpMinor || amountMinor > maxTopUpMinor) {
      setError(`Введите сумму от 1 до ${priceMoney(maxTopUpMinor)}.`);
      return;
    }

    const saved = intentRef.current ?? {
      amountMinor,
      idempotencyKey: crypto.randomUUID(),
    };
    const request = new AbortController();
    creating.current = request;
    keepIntent(saved);
    setBusy(true);
    setError("");
    try {
      const result = await boundedRequest(request, (signal) =>
        api<{ order: Order }>("/billing/topups", {
          method: "POST",
          authenticated: true,
          signal,
          body: { ...saved, returnTarget: "web" },
        }),
      );
      if (creating.current !== request || request.signal.aborted) return;
      if (
        !uuid.test(result.order.id) ||
        !isTopUp(result.order) ||
        result.order.amountMinor !== saved.amountMinor
      )
        throw new Error("Unexpected order");
      keepIntent(null);
      applyOrder(result.order);
      setAttempt((value) => value + 1);
    } catch (cause) {
      if (creating.current !== request) return;
      if (
        cause instanceof ApiError &&
        [400, 403, 409, 422].includes(cause.status) &&
        !request.signal.aborted
      ) {
        keepIntent(null);
        const latest = await refresh();
        if (creating.current !== request) return;
        const pending = latest?.orders.find(unfinished);
        if (pending) applyOrder(pending);
        setError(errorMessage(cause));
      } else {
        setError(
          "Ответ на создание платежа не получен. Повторите сохранённый запрос с той же суммой.",
        );
      }
    } finally {
      if (creating.current === request) {
        creating.current = null;
        setBusy(false);
      }
    }
  }

  const pending = !!order && unfinished(order);
  const awaitingOrder = !!orderId && !order;
  const confirmation =
    pending && order?.provider === "yookassa" && !needsReview(order)
      ? paymentConfirmationUrl(order.confirmationUrl)
      : null;
  const rate = balance ? hourlySpend(balance) : 0;
  const fundedUntil = balance?.fundedUntil
    ? dateTime(balance.fundedUntil, { year: true, timeZone: "Europe/Moscow" })
    : null;
  const testPayment = billing?.provider === "mock" || billing?.test;
  const canPay = billing?.canPay;
  return (
    <>
      <header className="page-heading">
        <div>
          <h1>
            Баланс<span className="heading-dot">.</span>
          </h1>
          <p>Пополняйте вручную и оплачивайте время работы мониторов.</p>
        </div>
        <button
          type="button"
          className="secondary"
          onClick={() => {
            setError("");
            void refresh();
            void loadHistory(historyPage.current);
          }}
        >
          Обновить
        </button>
      </header>
      {error && <Notice>{error}</Notice>}
      {!balance && !error && <Loading text="Загружаем баланс…" />}
      {balance && (
        <section
          className="panel settings-panel wallet-summary"
          aria-label="Состояние баланса"
        >
          <div className="wallet-balance">
            <span>Доступно</span>
            <strong>{balanceMoney(balance)}</strong>
            <p className="small muted">
              {priceMoney(balance.pricePerMonitorMinor)} за монитор за полный
              календарный месяц. Неполный час оплачивается пропорционально
              времени работы.
            </p>
          </div>
          <div className="wallet-forecast">
            <p>
              Включено мониторов <strong>{balance.enabledMonitors}</strong>
            </p>
            {balance.enabledMonitors > 0 &&
            hasFunds(balance) &&
            !balance.suspended ? (
              <>
                <p className="small muted">
                  Расход при текущем числе включённых мониторов
                </p>
                <p>
                  За час ≈ <strong>{spendingEstimate(rate)}</strong>
                </p>
                <p>
                  За сутки ≈ <strong>{spendingEstimate(rate * 24)}</strong>
                </p>
                {fundedUntil && (
                  <p>
                    Хватит примерно до <strong>{fundedUntil} МСК</strong>
                  </p>
                )}
              </>
            ) : (
              <p>
                {hasFunds(balance)
                  ? "Нет расхода на мониторы."
                  : "Для проверок пополните баланс."}
              </p>
            )}
          </div>
        </section>
      )}

      <section
        className="panel settings-panel billing-checkout"
        aria-labelledby="checkout-title"
      >
        <div className="panel-heading">
          <div>
            <h2 id="checkout-title">Пополнить баланс</h2>
            <p>
              {testPayment
                ? "Тестовый платёж без списания с карты."
                : "Пополнение через ЮKassa. Автоматических списаний с карты нет."}
            </p>
          </div>
        </div>
        {!canPay && ready && <p>Пополнение сейчас недоступно.</p>}
        {canPay && !pending && !awaitingOrder && (
          <form className="form-stack" onSubmit={create}>
            {intent ? (
              <p>Сохранённый запрос на {priceMoney(intent.amountMinor)}.</p>
            ) : (
              <>
                <div
                  className="wallet-amounts"
                  role="group"
                  aria-label="Быстрый выбор суммы"
                >
                  {quickTopUps.map((rubles) => (
                    <button
                      type="button"
                      className="secondary"
                      key={rubles}
                      aria-pressed={parseRubles(amount) === rubles * 100}
                      onClick={() => setAmount(String(rubles))}
                    >
                      {priceMoney(rubles * 100)}
                    </button>
                  ))}
                </div>
                <label>
                  Сумма, ₽
                  <input
                    name="amount"
                    inputMode="decimal"
                    maxLength={9}
                    value={amount}
                    onChange={(event) => setAmount(event.target.value)}
                    required
                  />
                </label>
              </>
            )}
            <button className="primary" disabled={busy || !ready}>
              {busy
                ? "Оформляем…"
                : intent
                  ? "Повторить запрос"
                  : testPayment
                    ? "Начать тестовый платёж"
                    : "Перейти к пополнению"}
            </button>
          </form>
        )}
        {order && (
          <div className="form-stack billing-order-status" aria-live="polite">
            <strong>{orderMessage(order)}</strong>
            <p>{priceMoney(order.amountMinor)}</p>
            {confirmation && (
              <a className="button" href={confirmation}>
                Перейти на страницу ЮKassa
              </a>
            )}
            <details className="small">
              <summary>Детали платежа</summary>
              <p>Номер для поддержки {order.id}</p>
            </details>
          </div>
        )}
        {statusError && <Notice>{statusError}</Notice>}
        {orderId && (!order || isTopUp(order)) && (
          <button
            type="button"
            className="secondary"
            disabled={statusBusy || busy}
            onClick={() => setAttempt((value) => value + 1)}
          >
            {statusBusy ? "Проверяем платёж…" : "Проверить статус платежа"}
          </button>
        )}
      </section>

      <section
        className="panel settings-panel"
        aria-labelledby="wallet-history-title"
        aria-busy={historyBusy}
      >
        <div className="panel-heading">
          <h2 id="wallet-history-title">История баланса</h2>
        </div>
        {historyError && <Notice>{historyError}</Notice>}
        {!history && historyBusy && <Loading text="Загружаем историю…" />}
        {history && (
          <>
            {history.entries.length ? (
              <ul className="wallet-ledger">
                {history.entries.map((entry) => (
                  <li key={entry.id}>
                    <div>
                      <strong>
                        {entry.test && entry.kind === "top_up"
                          ? "Тестовое пополнение"
                          : balanceEntryLabels[entry.kind]}
                      </strong>
                      <time className="small muted" dateTime={entry.createdAt}>
                        {dateTime(entry.createdAt, {
                          timeZone: "Europe/Moscow",
                        })}{" "}
                        МСК
                      </time>
                    </div>
                    <strong>{entryAmount(entry)}</strong>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="muted">Операций пока нет.</p>
            )}
            {history.pagination.totalPages > 1 && (
              <div className="wallet-amounts wallet-pagination">
                <button
                  type="button"
                  className="secondary"
                  disabled={historyBusy || history.pagination.page <= 1}
                  onClick={() => void loadHistory(history.pagination.page - 1)}
                >
                  Назад
                </button>
                <span>
                  {history.pagination.page} из {history.pagination.totalPages}
                </span>
                <button
                  type="button"
                  className="secondary"
                  disabled={
                    historyBusy ||
                    history.pagination.page >= history.pagination.totalPages
                  }
                  onClick={() => void loadHistory(history.pagination.page + 1)}
                >
                  Далее
                </button>
              </div>
            )}
          </>
        )}
      </section>
    </>
  );
}
