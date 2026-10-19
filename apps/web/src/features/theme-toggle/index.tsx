"use client";
import { Icon } from "@/shared/ui";
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
      <Icon name="moon" className="theme-moon" />
      <Icon name="sun" className="theme-sun" />
    </button>
  );
}
