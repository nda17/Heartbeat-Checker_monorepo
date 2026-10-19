"use client";
import { createContext, useContext } from "react";
export type Profile = {
  account: {
    id: string;
    name: string;
    role: string;
    status: string;
    createdAt: string;
  };
  email: string;
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
