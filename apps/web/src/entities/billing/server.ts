import "server-only";
import { isPricing, Pricing } from "./index";

export async function readPublicPricing(): Promise<Pricing | null> {
  const origin = process.env.GO_API_ORIGIN;
  if (!origin) return null;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 2_000);
  try {
    const response = await fetch(`${origin}/api/v1/billing/pricing`, {
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) return null;
    const data: unknown = await response.json();
    if (
      controller.signal.aborted ||
      !data ||
      typeof data !== "object" ||
      !("pricing" in data)
    )
      return null;
    return isPricing(data.pricing) ? data.pricing : null;
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}
