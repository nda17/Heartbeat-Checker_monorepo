"use client";
import Link from "next/link";
import { useSyncExternalStore } from "react";

const storageKey = "heartbeat-cookie-notice";
const version = "v1";
const changeEvent = "heartbeat-cookie-notice-change";
let acknowledgedForPage = false;

function subscribe(onChange: () => void) {
  const storage = (event: StorageEvent) => {
    if (event.key !== storageKey && event.key !== null) return;
    acknowledgedForPage = false;
    onChange();
  };
  window.addEventListener(changeEvent, onChange);
  window.addEventListener("storage", storage);
  return () => {
    window.removeEventListener(changeEvent, onChange);
    window.removeEventListener("storage", storage);
  };
}

function acknowledged() {
  if (acknowledgedForPage) return true;
  try {
    return localStorage.getItem(storageKey) === version;
  } catch {
    return false;
  }
}

export function CookieNotice() {
  const hidden = useSyncExternalStore(subscribe, acknowledged, () => true);
  if (hidden) return null;
  return (
    <section className="cookie-notice" aria-label="Использование cookie">
      <p>
        Используем cookie для входа и безопасной работы сайта.{" "}
        <Link href="/cookies">Подробнее</Link>
      </p>
      <button
        type="button"
        className="primary"
        onClick={() => {
          acknowledgedForPage = true;
          try {
            localStorage.setItem(storageKey, version);
          } catch {
            // Dismiss for this page even if browser storage is disabled.
          }
          window.dispatchEvent(new Event(changeEvent));
        }}
      >
        Понятно
      </button>
    </section>
  );
}
