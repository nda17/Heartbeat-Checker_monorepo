"use client";

import { useEffect, useState } from "react";

export type EmailCodeChallenge = {
  id: string;
  email: string;
  expiresAt: string;
  resendAvailableAt: string;
  serverNow: string;
  status?: "pending" | "expired" | "locked" | "completed";
};
export type EmailCodeState = {
  challenge: EmailCodeChallenge;
  receivedAt: number;
};
export function captureEmailCode(value: unknown): EmailCodeState {
  if (!value || typeof value !== "object")
    throw new Error("Invalid email challenge");
  const c = value as Partial<EmailCodeChallenge>;
  if (
    typeof c.id !== "string" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      c.id,
    ) ||
    typeof c.email !== "string" ||
    c.email.length > 254 ||
    ![c.expiresAt, c.resendAvailableAt, c.serverNow].every(
      (date) => typeof date === "string" && Number.isFinite(Date.parse(date)),
    ) ||
    (c.status !== undefined &&
      !["pending", "expired", "locked", "completed"].includes(c.status))
  )
    throw new Error("Invalid email challenge");
  return { challenge: c as EmailCodeChallenge, receivedAt: Date.now() };
}
export function normalizeEmailCode(value: string) {
  const code = value.replace(/\s/g, "");
  return /^[0-9]{6}$/.test(code) ? code : null;
}
// Advance from the server snapshot, rather than comparing a server deadline to
// the browser's wall clock. A refresh captures a new authoritative snapshot.
export function useEmailCodeClock(state: EmailCodeState | null) {
  const [now, setNow] = useState(0);
  useEffect(() => {
    const tick = () => setNow(Date.now());
    tick();
    const timer = window.setInterval(tick, 1000);
    return () => window.clearInterval(timer);
  }, [state]);
  const elapsed = state
    ? Math.max(0, (now || state.receivedAt) - state.receivedAt)
    : 0;
  const serverNow = state ? Date.parse(state.challenge.serverNow) + elapsed : 0;
  return {
    now,
    resendSeconds: state
      ? Math.max(
          0,
          Math.ceil(
            (Date.parse(state.challenge.resendAvailableAt) - serverNow) / 1000,
          ),
        )
      : 0,
  };
}
