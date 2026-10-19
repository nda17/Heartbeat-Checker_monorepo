"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useSession } from "@/entities/session";
import { api, ApiError, errorMessage } from "@/shared/api";
import { clearMonitorDraft } from "@/shared/android";
import { Dialog } from "@/shared/dialog";
import { Notice, PasswordInput, Spinner } from "@/shared/ui";

type Target = { id: string; email: string };
type Result = "deleted" | "unknown";

export function AccountDeletion() {
  const { profile, beginSessionTransition, endSessionTransition } =
    useSession();
  const router = useRouter();
  const [target, setTarget] = useState<Target | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const submitting = useRef(false);
  const transition = useRef(false);
  const isAdmin = profile.account.role === "admin";

  useEffect(
    () => () => {
      if (transition.current) {
        transition.current = false;
        endSessionTransition();
      }
    },
    [endSessionTransition],
  );

  function finish(value: Result) {
    setResult(value);
    router.replace(`/login?account=${value}`);
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting.current || result || !target) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    if (data.get("confirm") !== "on") {
      setError("Подтвердите, что понимаете последствия удаления.");
      return;
    }
    const currentPassword = String(data.get("currentPassword") ?? "");
    if (
      !currentPassword ||
      new TextEncoder().encode(currentPassword).length > 1024
    ) {
      setError("Введите текущий пароль длиной не более 1024 байт.");
      return;
    }
    submitting.current = true;
    setBusy(true);
    setError("");
    beginSessionTransition();
    transition.current = true;
    let leaving = false;
    try {
      await api("/me", {
        method: "DELETE",
        authenticated: true,
        body: { currentPassword, expectedAccountId: target.id },
      });
      form.reset();
      clearMonitorDraft();
      leaving = true;
      finish("deleted");
    } catch (cause) {
      form.reset();
      if (cause instanceof ApiError && cause.code === "invalid_credentials") {
        setError(
          "Неверный текущий пароль. Проверьте его и повторите подтверждение.",
        );
      } else if (
        cause instanceof ApiError &&
        cause.code === "admin_transfer_required"
      ) {
        setError(
          "Другой администратор должен сначала снять с вашего аккаунта права администратора. После этого войдите заново.",
        );
      } else if (
        cause instanceof ApiError &&
        cause.code === "password_setup_required"
      ) {
        setError(
          "Сначала установите пароль через email и войдите заново, затем вернитесь к удалению аккаунта.",
        );
      } else if (cause instanceof ApiError && cause.code === "conflict") {
        setError(
          "Данные аккаунта или сессии изменились. Закройте форму и войдите заново, затем подтвердите удаление выбранного аккаунта.",
        );
      } else if (
        cause instanceof ApiError &&
        (cause.status === 0 || cause.status >= 500 || cause.status === 401)
      ) {
        // A lost response cannot establish whether the destructive transaction committed.
        leaving = true;
        finish("unknown");
      } else {
        setError(errorMessage(cause));
      }
    } finally {
      if (!leaving && transition.current) {
        transition.current = false;
        endSessionTransition();
      }
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <>
      <section
        className="panel settings-panel account-deletion-panel"
        aria-labelledby="account-deletion-title"
      >
        <div className="panel-heading">
          <span className="panel-index">06</span>
          <div>
            <h2 id="account-deletion-title">Удаление аккаунта</h2>
            <p>
              Прекратите работу аккаунта и обезличьте данные своего
              пространства. Восстановить их будет нельзя.
            </p>
          </div>
        </div>
        {!profile.hasPassword && (
          <p className="small">
            Для подтверждения удаления сначала{" "}
            <Link className="text-link" href="/forgot-password">
              установите пароль через email
            </Link>{" "}
            и войдите заново.
          </p>
        )}
        {isAdmin && (
          <p className="small">
            Перед удалением другой администратор должен снять с вашего аккаунта
            права администратора. После этого войдите заново.
          </p>
        )}
        <button
          type="button"
          className="quiet danger-text"
          disabled={!profile.hasPassword || isAdmin}
          onClick={() => {
            setTarget({ id: profile.account.id, email: profile.email });
            setError("");
            setResult(null);
          }}
        >
          Удалить аккаунт
        </button>
      </section>
      {target && (
        <Dialog
          title="Удалить аккаунт"
          busy={busy}
          onClose={() => {
            if (result) router.replace(`/login?account=${result}`);
            else setTarget(null);
          }}
        >
          {result ? (
            <div className="form-stack">
              <Notice success={result === "deleted"}>
                {result === "deleted"
                  ? "Аккаунт удалён. Данные пространства обезличены; восстановление недоступно."
                  : "Не удалось подтвердить результат запроса. Удаление могло завершиться. Не повторяйте запрос: перейдите ко входу, чтобы проверить состояние аккаунта."}
              </Notice>
              <Link
                className="button"
                href={`/login?account=${result}`}
                replace
              >
                Перейти ко входу
              </Link>
            </div>
          ) : (
            <form className="form-stack" onSubmit={submit}>
              <p>
                Вы удаляете аккаунт{" "}
                <strong className="account-deletion-email">
                  {target.email}
                </strong>
                .
              </p>
              <ul className="account-deletion-consequences">
                <li>
                  Доступ завершится на всех устройствах. Проверки и новые
                  уведомления прекратятся.
                </li>
                <li>
                  Данные пространства будут обезличены сразу. Восстановить
                  аккаунт, мониторы, историю и квоту будет нельзя.
                </li>
                <li>
                  Новая регистрация создаст отдельный пустой аккаунт без прежних
                  данных и квоты.
                </li>
              </ul>
              <p className="small muted">
                Доставка, начатая до удаления, может завершиться. Переписка
                поддержки и ранее отправленные сообщения обрабатываются
                отдельно. Зашифрованные резервные копии хранятся до 14 дней.
                Локальные черновики в Android-приложении удалите вручную.{" "}
                <Link className="text-link" href="/privacy#account-deletion">
                  Подробнее о данных
                </Link>
                .
              </p>
              {error && <Notice>{error}</Notice>}
              <label>
                Текущий пароль
                <PasswordInput
                  name="currentPassword"
                  autoComplete="current-password"
                  required
                  maxLength={1024}
                />
              </label>
              <label className="checkbox-label">
                <input type="checkbox" name="confirm" required />
                <span>
                  Понимаю последствия: аккаунт и данные пространства нельзя
                  будет восстановить.
                </span>
              </label>
              <div className="dialog-actions">
                <button
                  type="button"
                  className="secondary"
                  disabled={busy}
                  onClick={() => setTarget(null)}
                >
                  Отмена
                </button>
                <button className="danger" disabled={busy}>
                  {busy ? (
                    <>
                      <Spinner />
                      Удаляем…
                    </>
                  ) : (
                    "Удалить без восстановления"
                  )}
                </button>
              </div>
            </form>
          )}
        </Dialog>
      )}
    </>
  );
}
