"use client";
import { notify } from "@/shared/notifications";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { isPricing, priceMoney, Pricing } from "@/entities/billing";
import { api, ApiError, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice } from "@/shared/ui";

// Parse the decimal input as integer kopeks; no floating-point rounding of money.
export function parsePrice(value: string): number | null {
  const match = /^(\d{1,5})(?:[.,](\d{1,2}))?$/.exec(value.trim());
  if (!match) return null;
  const minor =
    Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
  return minor >= 100 && minor <= 1_000_000 ? minor : null;
}
export function AdminPricing({
  canRead,
  canWrite,
  blocked,
  sessionRevision,
  onBusyChange,
}: {
  canRead: boolean;
  canWrite: boolean;
  blocked: boolean;
  sessionRevision: number;
  onBusyChange: (busy: boolean) => void;
}) {
  const [pricing, setPricing] = useState<Pricing | null>(null);
  const [value, setValue] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [outcome, setOutcome] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const read = useRef<AbortController | null>(null);
  const write = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const dirty = useRef(false);
  const accept = useCallback((next: unknown, resetDraft = false) => {
    if (!isPricing(next)) throw new Error("Invalid pricing response");
    // A background read must not replace an edit or silently rebase its CAS version.
    if (dirty.current && !resetDraft) return false;
    dirty.current = false;
    setPricing(next);
    setValue(
      `${Math.floor(next.pricePerMonitorMinor / 100)}.${String(next.pricePerMonitorMinor % 100).padStart(2, "0")}`,
    );
    return true;
  }, []);
  const load = useCallback(
    async (resetDraft = false) => {
      read.current?.abort();
      const controller = new AbortController();
      read.current = controller;
      setLoading(true);
      setError("");
      const timer = window.setTimeout(() => {
        if (read.current !== controller) return;
        read.current = null;
        controller.abort();
        if (mounted.current) {
          setLoading(false);
          setPricing(null);
          setError("Не удалось загрузить цену вовремя. Повторите обновление.");
        }
      }, 10_000);
      try {
        const result = await api<{ pricing: unknown }>(
          "/admin/billing/pricing",
          {
            signal: controller.signal,
          },
        );
        if (
          !mounted.current ||
          read.current !== controller ||
          controller.signal.aborted
        )
          return;
        if (accept(result.pricing, resetDraft)) setUncertain(false);
      } catch (cause) {
        if (
          mounted.current &&
          read.current === controller &&
          !controller.signal.aborted
        ) {
          setPricing(null);
          setError(errorMessage(cause, "admin"));
        }
      } finally {
        window.clearTimeout(timer);
        if (read.current === controller) {
          read.current = null;
          if (mounted.current) setLoading(false);
        }
      }
    },
    [accept],
  );
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      read.current?.abort();
      write.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (canRead && !blocked) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- The request is populated asynchronously.
      void load();
    }
    return () => {
      read.current?.abort();
      read.current = null;
    };
  }, [canRead, blocked, sessionRevision, load]);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      !canWrite ||
      blocked ||
      loading ||
      write.current ||
      !pricing ||
      uncertain
    )
      return;
    const minor = parsePrice(value);
    if (minor === pricing.pricePerMonitorMinor) return;
    if (minor === null || !reason.trim()) {
      setError(
        "Укажите цену от 1 до 10 000 рублей, не более двух знаков после запятой, и причину изменения.",
      );
      return;
    }
    read.current?.abort();
    read.current = null;
    const controller = new AbortController();
    write.current = controller;
    setSaving(true);
    setError("");
    setOutcome("");
    onBusyChange(true);
    const timer = window.setTimeout(() => {
      if (!mounted.current || write.current !== controller) return;
      write.current = null;
      controller.abort();
      setSaving(false);
      onBusyChange(false);
      setUncertain(true);
      setOutcome(
        "Ответ на изменение цены не получен. Сверьте текущую цену перед повторным сохранением. Запрос не повторяется автоматически.",
      );
      void load(true);
    }, 10_000);
    try {
      const result = await api<{ pricing: unknown }>("/admin/billing/pricing", {
        method: "PUT",
        authenticated: true,
        signal: controller.signal,
        body: {
          pricePerMonitorMinor: minor,
          version: pricing.version,
          reason: reason.trim(),
        },
      });
      if (
        !mounted.current ||
        write.current !== controller ||
        controller.signal.aborted
      )
        return;
      accept(result.pricing, true);
      setReason("");
      setUncertain(false);
      notify.success(
        "Цена сохранена. Новые заказы используют её; суммы ранее созданных заказов не меняются.",
      );
    } catch (cause) {
      if (!mounted.current || write.current !== controller) return;
      const uncertainResult =
        !(cause instanceof ApiError) ||
        cause.status === 0 ||
        cause.status >= 500;
      if (uncertainResult) {
        setUncertain(true);
        setOutcome(
          "Ответ на изменение цены не получен. Обновите текущую цену перед повторным сохранением. Запрос не повторяется автоматически.",
        );
      } else {
        notify.error(errorMessage(cause, "admin"));
      }
      // Only reconcile by reading. Never retry a potentially committed PUT.
      // A known rejection preserves the draft until an explicit refresh.
      void load(uncertainResult);
    } finally {
      window.clearTimeout(timer);
      if (write.current === controller) {
        write.current = null;
        onBusyChange(false);
        if (mounted.current) setSaving(false);
      }
    }
  }
  const unchanged =
    !!pricing && parsePrice(value) === pricing.pricePerMonitorMinor;
  if (!canRead) return null;
  return (
    <section
      className="panel settings-panel admin-pricing"
      aria-labelledby="admin-pricing-title"
    >
      <div className="panel-heading">
        <div>
          <h2 id="admin-pricing-title">Стоимость мониторинга</h2>
          <p>Цена за один монитор за календарный месяц. Валюта — рубли.</p>
        </div>
      </div>
      {pricing && (
        <p>
          Текущая цена:{" "}
          <strong>{priceMoney(pricing.pricePerMonitorMinor)}</strong> ·
          обновлена {dateTime(pricing.updatedAt, { year: true })}.
        </p>
      )}
      {loading && <Loading text="Загружаем цену…" />}
      <form className="form-stack" onSubmit={save}>
        <div className="form-row">
          <label>
            Цена за монитор, ₽
            <input
              name="price"
              inputMode="decimal"
              value={value}
              onChange={(event) => {
                dirty.current = true;
                setValue(event.target.value);
              }}
              required
              maxLength={8}
              disabled={saving || blocked || loading || !pricing}
            />
          </label>
          <label>
            Причина изменения
            <input
              name="reason"
              value={reason}
              onChange={(event) => {
                dirty.current = true;
                setReason(event.target.value);
              }}
              required
              maxLength={500}
              disabled={saving || blocked || loading || !pricing}
            />
          </label>
        </div>
        <p className="field-hint">
          Новая цена применяется к новым заказам. Уже созданные заказы и
          оплаченные периоды сохраняют свои суммы.
        </p>
        {unchanged && <p className="small muted">Цена не изменилась.</p>}
        {!canWrite && (
          <p className="small">
            Для сохранения повторно подтвердите вход в блоке защиты выше.
          </p>
        )}
        {error && <Notice>{error}</Notice>}
        {outcome && <Notice>{outcome}</Notice>}
        <div className="admin-form-actions">
          <button
            type="submit"
            className="secondary"
            disabled={
              !canWrite ||
              blocked ||
              saving ||
              loading ||
              !pricing ||
              uncertain ||
              unchanged
            }
          >
            {saving ? "Сохраняем цену…" : "Сохранить цену"}
          </button>
          <button
            type="button"
            className="quiet"
            disabled={blocked || saving || loading}
            onClick={() => void load(true)}
          >
            Обновить текущую цену
          </button>
        </div>
      </form>
    </section>
  );
}
