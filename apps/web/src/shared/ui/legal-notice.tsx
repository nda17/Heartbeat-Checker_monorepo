"use client";

import { useSyncExternalStore } from "react";

const firstYear = 2026;
const yearFormatter = new Intl.DateTimeFormat("en", {
  timeZone: "Europe/Moscow",
  year: "numeric",
});

function subscribe(onChange: () => void) {
  const timer = window.setInterval(onChange, 60 * 60 * 1000);
  return () => window.clearInterval(timer);
}

function currentYear() {
  return Number(yearFormatter.format(new Date()));
}

export function FooterLegalNotice() {
  const year = useSyncExternalStore(subscribe, currentYear, () => firstYear);

  return (
    <div className="footer-legal-notice">
      <span>
        {year > firstYear ? `${firstYear}–${year}` : firstYear} © Все права
        защищены
      </span>
    </div>
  );
}
