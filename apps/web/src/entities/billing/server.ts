import "server-only";
import { isIP } from "node:net";
import { headers } from "next/headers";
import { isPricing, Pricing } from "./index";

export async function readPublicPricing(): Promise<Pricing | null> {
  const origin = process.env.GO_API_ORIGIN;
  if (!origin) return null;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 2_000);
  try {
    // The public frontend edge overwrites this dedicated header, and web:3000
    // is private. Never infer identity from browser-supplied forwarding headers.
    const clientIP = (await headers()).get("X-Heartbeat-Client-IP");
    if (
      !clientIP ||
      clientIP.trim() !== clientIP ||
      /[,%\[\]]/.test(clientIP) ||
      !isIP(clientIP) ||
      clientIP === "0.0.0.0" ||
      clientIP === "::"
    )
      return null;
    const response = await fetch(`${origin}/api/v1/billing/pricing`, {
      cache: "no-store",
      headers: { "X-Heartbeat-Client-IP": clientIP },
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
