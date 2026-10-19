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
