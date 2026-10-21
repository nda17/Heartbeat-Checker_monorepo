"use client";
import { createContext, useContext } from "react";

export const sessionTransitionStorageKey = "heartbeat-session-transition";
export const sessionTransitionEvent = "heartbeat-session-transition";

let transitionSequence = 0;
export function signalSessionTransition(phase: "begin" | "end") {
  const value = `${phase}:${Date.now()}:${++transitionSequence}`;
  try {
    window.localStorage.setItem(sessionTransitionStorageKey, value);
  } catch {
    // Same-tab invalidation still works when browser storage is unavailable.
  }
  window.dispatchEvent(
    new window.CustomEvent(sessionTransitionEvent, { detail: value }),
  );
}
export type Profile = {
  account: {
    id: string;
    name: string;
    role: string;
    status: string;
    createdAt: string;
  };
  email: string;
  emailVerified?: boolean;
  hasPassword: boolean;
};
export type Session = {
  id: string;
  createdAt: string;
  lastUsedAt: string;
  expiresAt: string;
  ip: string;
  userAgent: string;
  current: boolean;
};
export const SessionContext = createContext<{
  profile: Profile;
  refresh: () => Promise<void>;
  beginSessionTransition: () => void;
  endSessionTransition: () => void;
} | null>(null);
export function useSession() {
  const session = useContext(SessionContext);
  if (!session) throw new Error("Session provider required");
  return session;
}
