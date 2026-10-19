"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { isPricing, priceMoney, Pricing } from "@/entities/billing";
import { api } from "@/shared/api";
import { Arrow } from "@/shared/ui";

export function PublicPricing({
  initialPricing,
  variant = "home",
  landingMode = false,
}: {
  initialPricing: Pricing | null;
  variant?: "home" | "terms";
  landingMode?: boolean;
}) {
  const [pricing, setPricing] = useState(initialPricing);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let alive = true;
    let current: AbortController | null = null;
    let timeout: number | undefined;
    async function refresh() {
      if (!alive || current || document.visibilityState === "hidden") return;
      const controller = new AbortController();
      current = controller;
      timeout = window.setTimeout(() => {
        if (current !== controller) return;
        current = null;
        controller.abort();
        if (alive) setPricing(null);
      }, 5_000);
      const timer = timeout;
      try {
        const data = await api<{ pricing: unknown }>("/billing/pricing", {
          signal: controller.signal,
        });
        if (!alive || current !== controller || controller.signal.aborted)
          return;
        setPricing(isPricing(data.pricing) ? data.pricing : null);
      } catch {
        if (alive && current === controller && !controller.signal.aborted)
          setPricing(null);
      } finally {
        window.clearTimeout(timer);
        if (current === controller) current = null;
      }
    }
    const update = () => {
      void refresh();
    };
    update();
    window.addEventListener("focus", update);
    window.addEventListener("pageshow", update);
    document.addEventListener("visibilitychange", update);
    const interval = window.setInterval(update, 60_000);
    return () => {
      alive = false;
      current?.abort();
      current = null;
      window.clearTimeout(timeout);
      window.clearInterval(interval);
      window.removeEventListener("focus", update);
      window.removeEventListener("pageshow", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, [attempt]);
  const unavailable = (
    <span>
      Цена временно недоступна.{" "}
      <button
        type="button"
        className="quiet"
        onClick={() => setAttempt((value) => value + 1)}
      >
        Обновить цену
      </button>
    </span>
  );
  if (variant === "terms")
    return (
      <p>
        {pricing ? (
          <>
            Цена — {priceMoney(pricing.pricePerMonitorMinor)} за один монитор за
            один календарный месяц. Квота из N мониторов стоит{" "}
            {priceMoney(pricing.pricePerMonitorMinor)} × N за полный месяц.
            Например, 3 монитора —{" "}
            {priceMoney(3 * pricing.pricePerMonitorMinor)}, 10 мониторов —{" "}
            {priceMoney(10 * pricing.pricePerMonitorMinor)}.{" "}
          </>
        ) : (
          <>{unavailable} </>
        )}
        Пробного периода нет. Итоговая сумма к оплате в рублях показывается до
        подтверждения заказа.
      </p>
    );
  return (
    <section id="pricing" className="landing-pricing landing-section">
      <div className="landing-pricing-copy">
        <p className="eyebrow">ПОНЯТНАЯ ЕДИНИЦА СТОИМОСТИ</p>
        <h2>
          Платить за нужные адреса.
          <br />
          Менять их по мере роста.
        </h2>
        <p>
          Один монитор регулярно проверяет один выбранный адрес — например,
          главную страницу, страницу входа или API. Проверки по расписанию
          входят в стоимость.
        </p>
      </div>
      <div className="landing-price-card">
        {pricing ? (
          <>
            <div className="landing-price">
              <strong>{priceMoney(pricing.pricePerMonitorMinor)}</strong>
              <p>
                за монитор
                <br />в месяц
              </p>
            </div>
            <div className="landing-price-examples">
              <div>
                <span>Главная + вход + API · 3 монитора</span>
                <strong>
                  {priceMoney(3 * pricing.pricePerMonitorMinor)}{" "}
                  <small>/ мес.</small>
                </strong>
              </div>
              <div>
                <span>10 мониторов</span>
                <strong>
                  {priceMoney(10 * pricing.pricePerMonitorMinor)}{" "}
                  <small>/ мес.</small>
                </strong>
              </div>
            </div>
          </>
        ) : (
          <p role="status">{unavailable}</p>
        )}
        <p className="landing-price-note">
          Пробного периода нет. Итоговая сумма и условия заказа показываются до
          оплаты.
        </p>
        <Link href={landingMode ? "#how" : "/register"} className="button">
          {landingMode ? "Как это работает" : "Создать аккаунт"} <Arrow />
        </Link>
      </div>
    </section>
  );
}
