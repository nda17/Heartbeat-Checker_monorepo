"use client";
import { useSyncExternalStore } from "react";

const storageKey = "heartbeat-theme";
const changeEvent = "heartbeat-theme-change";

function subscribe(onChange: () => void) {
  function onStorage(event: StorageEvent) {
    if (event.key !== storageKey && event.key !== null) return;
    document.documentElement.dataset.theme =
      event.newValue === "dark" ? "dark" : "light";
    onChange();
  }
  window.addEventListener(changeEvent, onChange);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(changeEvent, onChange);
    window.removeEventListener("storage", onStorage);
  };
}

export function useTheme() {
  const dark = useSyncExternalStore(
    subscribe,
    () => document.documentElement.dataset.theme === "dark",
    () => false,
  );
  return dark ? "dark" : "light";
}

export function toggleTheme() {
  const theme =
    document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(storageKey, theme);
  } catch {
    // The choice still works for this page when browser storage is disabled.
  }
  window.dispatchEvent(new Event(changeEvent));
}
