"use client";
import Link from "next/link";
import { useEffect, useState } from "react";
import { api, ApiError } from "@/shared/api";
import { Arrow } from "@/shared/ui";

export function PublicAuthLinks() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  useEffect(() => {
    let pending: AbortController | null = null;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    const refresh = () => {
      if (pending) return;
      const controller = new AbortController();
      pending = controller;
      const timer = setTimeout(() => {
        controller.abort();
        if (pending === controller) pending = null;
      }, 5000);
      timeout = timer;
      void api("/me", { signal: controller.signal })
        .then(() => {
          if (pending === controller && !controller.signal.aborted)
            setAuthenticated(true);
        })
        .catch((failure) => {
          if (
            pending === controller &&
            !controller.signal.aborted &&
            failure instanceof ApiError &&
            failure.status === 401
          )
            setAuthenticated(false);
        })
        .finally(() => {
          clearTimeout(timer);
          if (pending === controller) pending = null;
        });
    };
    const visible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    const restored = (event: PageTransitionEvent) => {
      if (event.persisted) refresh();
    };
    refresh();
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("pageshow", restored);
    return () => {
      pending?.abort();
      pending = null;
      clearTimeout(timeout);
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("pageshow", restored);
    };
  }, []);
  return authenticated === false ? (
    <>
      <Link href="/login">Войти</Link>
      <Link className="button small-button" href="/register">
        Создать аккаунт <Arrow />
      </Link>
    </>
  ) : (
    <Link href="/app">Личный кабинет</Link>
  );
}
