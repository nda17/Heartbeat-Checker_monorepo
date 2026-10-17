"use client";
import { toggleTheme, useTheme } from "@/shared/theme";

export function ThemeToggle() {
  const theme = useTheme();
  return (
    <button
      type="button"
      className="theme-toggle icon-button"
      aria-label="Тёмная тема"
      aria-pressed={theme === "dark"}
      title={
        theme === "dark" ? "Включить светлую тему" : "Включить тёмную тему"
      }
      onClick={toggleTheme}
    >
      <svg
        className="theme-moon"
        viewBox="0 0 24 24"
        fill="none"
        aria-hidden="true"
      >
        <path
          d="M20 14a8.5 8.5 0 0 1-10-10 8.5 8.5 0 1 0 10 10Z"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
      <svg
        className="theme-sun"
        viewBox="0 0 24 24"
        fill="none"
        aria-hidden="true"
      >
        <circle cx="12" cy="12" r="4" stroke="currentColor" strokeWidth="1.5" />
        <path
          d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
        />
      </svg>
    </button>
  );
}
