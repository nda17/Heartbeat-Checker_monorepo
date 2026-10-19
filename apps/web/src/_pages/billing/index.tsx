"use client";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { api, errorMessage } from "@/shared/api";
import { Entitlement } from "@/entities/monitor";
import { dateTime, Loading, Notice } from "@/shared/ui";
type Order = {
  id: string;
  provider: "mock";
  status: "pending" | "succeeded" | "canceled";
  monitorCount: number;
  amountMinor: number;
  currency: "RUB";
  createdAt: string;
  expiresAt: string;
  completedAt: string | null;
  activeUntil: string | null;
};
type Billing = {
  provider: "disabled" | "mock";
  canSimulate: boolean;
  pricePerMonitorMinor: number;
  orders: Order[];
};
const statuses = {
  pending: "Ожидает тестового результата",
  succeeded: "Тест успешно завершён",
  canceled: "Тест отменён",
};
function money(amountMinor: number) {
  return new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency: "RUB",
  }).format(amountMinor / 100);
}
export function BillingPage() {
  const [billing, setBilling] = useState<Billing | null>(null);
  const [loadError, setLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [count, setCount] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [demo, setDemo] = useState<"succeeded" | "canceled" | null>(null);
  const request = useRef<{ count: number; key: string } | null>(null);
  const load = useCallback(async (signal?: AbortSignal) => {
    const [result, access] = await Promise.all([
      api<Billing>("/billing", { signal }),
      api<{ entitlement: Entitlement }>("/entitlement", { signal }),
    ]);
    if (signal?.aborted) return;
    setBilling(result);
    setEntitlement(access.entitlement);
  }, []);
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
    setSuccess("");
    if (!request.current || request.current.count !== count)
      request.current = { count, key: crypto.randomUUID() };
    try {
      const result = await api<{ order: Order }>("/billing/orders", {
        method: "POST",
        authenticated: true,
        body: { monitorCount: count, idempotencyKey: request.current.key },
      });
      request.current = null;
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
      setSuccess(
        "Тестовый заказ создан. Выберите результат ниже. Деньги не списываются.",
      );
      try {
        await load();
      } catch (e) {
        setError(`Заказ создан, но список не обновился: ${errorMessage(e)}`);
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function simulate(order: Order, outcome: "succeeded" | "canceled") {
    setBusy(true);
    setError("");
    setSuccess("");
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
      setSuccess(
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
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const pending = billing?.orders.find((order) => order.status === "pending");
  const adminGrant =
    entitlement?.source === "admin_grant" &&
    !!entitlement.activeUntil &&
    new Date(entitlement.activeUntil) > new Date();
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">ПОДПИСКА И ЗАКАЗЫ</p>
          <h1>Оплата</h1>
          <p>29 рублей за один монитор в месяц.</p>
        </div>
      </div>
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
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
          {adminGrant ? (
            <div className="local-banner">
              <span className="badge">Квота</span>
              <p>
                Доступ к {entitlement.quota} мониторам выдан администратором до{" "}
                {dateTime(entitlement.activeUntil)}. Платежей по этой квоте нет.
              </p>
            </div>
          ) : (
            <div className="local-banner">
              <span className="badge">TEST</span>
              <p>
                {billing.provider === "mock"
                  ? "Оплата работает в тестовом режиме. Все суммы условные: деньги не списываются. Тестовая квота не является оплаченной подпиской."
                  : "Приём платежей пока недоступен. Реальные списания и автопродление не выполняются."}
              </p>
            </div>
          )}
          {billing.provider === "mock" &&
            !billing.canSimulate &&
            !adminGrant && (
              <Notice>
                Тестовая оплата доступна только согласованным тестовым
                аккаунтам. Для вашего аккаунта выдача тестовой квоты недоступна.
              </Notice>
            )}
          {billing.provider === "mock" && !adminGrant && (
            <section className="panel settings-panel">
              <div className="panel-heading">
                <span className="panel-index">01</span>
                <div>
                  <h2>
                    {billing.canSimulate
                      ? "Тестовый заказ"
                      : "Демонстрация оплаты"}
                  </h2>
                  <p>
                    {billing.canSimulate
                      ? "Тестовая квота на один месяц. Автопродление отключено."
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
                    max={1000}
                    required
                    value={count}
                    onChange={(event) => setCount(Number(event.target.value))}
                    disabled={busy || !!pending}
                  />
                  <span className="field-hint">
                    Пока действует квота, продление доступно с тем же
                    количеством мониторов. Изменение количества — после её
                    окончания.
                  </span>
                </label>
                <p>
                  Условная сумма:{" "}
                  <strong>
                    {money(count * billing.pricePerMonitorMinor)} · TEST
                  </strong>
                </p>
                <button className="primary" disabled={busy || !!pending}>
                  {busy
                    ? "Сохраняем…"
                    : billing.canSimulate
                      ? "Создать тестовый заказ"
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
                    Заказ на {pending.monitorCount} мониторов ·{" "}
                    {money(pending.amountMinor)} · TEST. Срок подтверждения:{" "}
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
                    Отменить тестовый заказ
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
                  Последние 100 заказов. Пометка TEST означает, что деньги не
                  списывались.
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
                <table className="monitor-table mobile-card-table" role="table">
                  <thead>
                    <tr>
                      <th>Заказ</th>
                      <th>Мониторы</th>
                      <th>Условная сумма</th>
                      <th>Результат</th>
                      <th>Квота до</th>
                    </tr>
                  </thead>
                  <tbody>
                    {billing.orders.map((order) => (
                      <tr key={order.id}>
                        <td data-label="Заказ">
                          <strong>TEST</strong>
                          <span className="cell-sub">
                            {dateTime(order.createdAt)}
                          </span>
                          <span className="cell-sub mono">{order.id}</span>
                        </td>
                        <td data-label="Мониторы">{order.monitorCount}</td>
                        <td data-label="Условная сумма">
                          {money(order.amountMinor)}
                          <span className="cell-sub">Деньги не списаны</span>
                        </td>
                        <td data-label="Результат">{statuses[order.status]}</td>
                        <td data-label="Квота до">
                          {order.activeUntil
                            ? dateTime(order.activeUntil)
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
