export type Pricing = {
  pricePerMonitorMinor: number;
  currency: "RUB";
  period: "month";
  version: number;
  updatedAt: string;
};
export function isPricing(value: unknown): value is Pricing {
  if (!value || typeof value !== "object") return false;
  const p = value as Partial<Pricing>;
  return (
    Number.isSafeInteger(p.pricePerMonitorMinor) &&
    p.pricePerMonitorMinor! >= 100 &&
    p.pricePerMonitorMinor! <= 1_000_000 &&
    Number.isSafeInteger(p.version) &&
    p.version! > 0 &&
    p.currency === "RUB" &&
    p.period === "month" &&
    typeof p.updatedAt === "string" &&
    Number.isFinite(Date.parse(p.updatedAt))
  );
}
export function priceMoney(minor: number) {
  return new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency: "RUB",
    minimumFractionDigits: minor % 100 === 0 ? 0 : 2,
    maximumFractionDigits: 2,
  }).format(minor / 100);
}

export type Balance = {
  availableMinor: number;
  positiveBelowCent: boolean;
  currency: "RUB";
  enabledMonitors: number;
  asOf: string;
  fundedUntil: string | null;
  suspended: boolean;
  pricePerMonitorMinor: number;
  priceVersion: number;
};

export type BalanceEntry = {
  id: string;
  kind: "top_up" | "admin_credit" | "usage" | "refund";
  amountMinor: number;
  positiveBelowCent?: boolean;
  test?: boolean;
  createdAt: string;
  periodStart?: string;
  periodEnd?: string;
};

export type BalanceHistory = {
  entries: BalanceEntry[];
  pagination: {
    page: number;
    pageSize: number;
    total: number;
    totalPages: number;
  };
};

export const minTopUpMinor = 100;
export const maxTopUpMinor = 35_000_000;
export const quickTopUps = [26, 100, 300, 500];

export function parseRubles(value: string) {
  const match = /^(\d{1,6})(?:[.,](\d{1,2}))?$/.exec(value.trim());
  if (!match) return 0;
  return Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
}
export function balanceMoney(balance: Balance) {
  return balance.positiveBelowCent
    ? "< 0,01 ₽"
    : priceMoney(balance.availableMinor);
}
export function hasFunds(balance: Balance) {
  return balance.availableMinor > 0 || balance.positiveBelowCent;
}
export function spendingEstimate(minor: number) {
  return minor > 0 && minor < 1 ? "< 0,01 ₽" : priceMoney(Math.round(minor));
}
export function hourlySpend(balance: Balance) {
  const moscowDate = new Date(Date.parse(balance.asOf) + 3 * 60 * 60_000);
  const lastDay = new Date(
    Date.UTC(moscowDate.getUTCFullYear(), moscowDate.getUTCMonth() + 1, 0),
  ).getUTCDate();
  return (
    (balance.pricePerMonitorMinor * balance.enabledMonitors) / (lastDay * 24)
  );
}

export const balanceEntryLabels: Record<BalanceEntry["kind"], string> = {
  top_up: "Пополнение",
  admin_credit: "Начисление администратора",
  usage: "Работа мониторов",
  refund: "Возврат",
};
export const balanceInvalidatedEvent = "heartbeat-balance-invalidated";

export function invalidateBalance() {
  window.dispatchEvent(new Event(balanceInvalidatedEvent));
}
