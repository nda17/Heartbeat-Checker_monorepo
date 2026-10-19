"use client";
import { InputHTMLAttributes, useState } from "react";
import { Icon } from "./icon";

export function PasswordInput(
  props: Omit<InputHTMLAttributes<HTMLInputElement>, "type">,
) {
  const [visible, setVisible] = useState(false);
  return (
    <span className="password-field">
      <input {...props} type={visible ? "text" : "password"} />
      <button
        type="button"
        className="password-toggle"
        aria-label={visible ? "Скрыть пароль" : "Показать пароль"}
        aria-pressed={visible}
        onClick={() => setVisible((value) => !value)}
      >
        <Icon name={visible ? "eye" : "eye-off"} />
      </button>
    </span>
  );
}
