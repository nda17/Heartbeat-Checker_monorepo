"use client";

import { useEffect, useState } from "react";
import {
  sessionTransitionEvent,
  sessionTransitionStorageKey,
} from "@/entities/session";

type DevRelease = {
  packageName: string;
  versionName: string;
  sizeBytes: number;
  sha256: string;
};

const downloadPath = "/api/v1/admin/android-dev/apk";

export function AndroidDevDownload() {
  const [release, setRelease] = useState<DevRelease | null>(null);

  useEffect(() => {
    let request: AbortController | null = null;
    let active = true;
    let transitioning = false;
    try {
      const last = window.localStorage.getItem(sessionTransitionStorageKey);
      const startedAt = Number(last?.split(":")[1]);
      transitioning =
        !!last?.startsWith("begin:") &&
        Number.isSafeInteger(startedAt) &&
        Date.now() - startedAt < 30_000;
    } catch {
      // Authorization is still checked by the metadata request.
    }
    const refresh = async () => {
      request?.abort();
      const controller = new AbortController();
      request = controller;
      setRelease(null);
      if (transitioning || document.visibilityState === "hidden") return;
      try {
        const response = await fetch("/api/v1/admin/android-dev", {
          credentials: "same-origin",
          cache: "no-store",
          signal: controller.signal,
        });
        if (!response.ok) return;
        const body = (await response.json()) as {
          release?: DevRelease;
          downloadPath?: string;
        };
        if (
          active &&
          request === controller &&
          !controller.signal.aborted &&
          !transitioning &&
          document.visibilityState === "visible" &&
          body.downloadPath === downloadPath &&
          body.release?.packageName === "ru.heartbeatchecker.app.dev" &&
          Number.isSafeInteger(body.release.sizeBytes) &&
          body.release.sizeBytes > 0 &&
          /^[a-f0-9]{64}$/.test(body.release.sha256)
        ) {
          setRelease(body.release);
        }
      } catch {
        // Missing session, MFA, network, or artifact all keep the control hidden.
      }
    };
    const onVisible = () => void refresh();
    const onSessionTransition = (value: string | null) => {
      request?.abort();
      setRelease(null);
      transitioning = value?.startsWith("begin:") ?? false;
      if (!transitioning) void refresh();
    };
    const onSameTabTransition = (event: Event) =>
      onSessionTransition((event as CustomEvent<string>).detail);
    const onOtherTabTransition = (event: StorageEvent) => {
      if (event.key === sessionTransitionStorageKey)
        onSessionTransition(event.newValue);
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") void refresh();
      else {
        request?.abort();
        setRelease(null);
      }
    };
    void refresh();
    window.addEventListener("focus", onVisible);
    window.addEventListener("pageshow", onVisible);
    window.addEventListener(sessionTransitionEvent, onSameTabTransition);
    window.addEventListener("storage", onOtherTabTransition);
    document.addEventListener("visibilitychange", onVisibility);
    const timer = window.setInterval(onVisible, 30_000);
    return () => {
      active = false;
      request?.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", onVisible);
      window.removeEventListener("pageshow", onVisible);
      window.removeEventListener(sessionTransitionEvent, onSameTabTransition);
      window.removeEventListener("storage", onOtherTabTransition);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);

  if (!release) return null;
  return (
    <div className="android-dev-download">
      <a className="button android-apk-button" href={downloadPath}>
        Скачать dev APK
      </a>
      <p>Отдельная сборка для разработки через Metro по Wi-Fi.</p>
      <p>
        Версия {release.versionName} · Размер{" "}
        {(release.sizeBytes / 1024 / 1024).toFixed(1)} МБ
      </p>
    </div>
  );
}
