"use client";
import { useCallback, useEffect, useState } from "react";
import {
  Destination,
  DestinationKind,
  MessengerLink,
  Provider,
  destinationKindName,
  providerName,
} from "@/entities/messenger";
import { api, errorMessage } from "@/shared/api";
import { Dialog } from "@/shared/dialog";
import { dateTime, Loading, Notice } from "@/shared/ui";

type LinkStart = {
  link: MessengerLink;
  startUrl: string | null;
  command: string | null;
};
const linkOptions: {
  provider: Provider;
  kind: DestinationKind;
  title: string;
}[] = [
  { provider: "telegram", kind: "private", title: "Telegram · личный чат" },
  { provider: "telegram", kind: "group", title: "Telegram · группа" },
  { provider: "telegram", kind: "topic", title: "Telegram · топик группы" },
  { provider: "max", kind: "private", title: "MAX · личный чат" },
];
function safeBotUrl(value: string | null) {
  if (!value) return null;
  try {
    const url = new URL(value);
    return url.protocol === "https:" &&
      ["t.me", "max.ru"].includes(url.hostname) &&
      !url.username &&
      !url.password
      ? url.href
      : null;
  } catch {
    return null;
  }
}

export function MessengerLinks({ email }: { email?: string }) {
  const [enabled, setEnabled] = useState(true);
  const [emailEnabled, setEmailEnabled] = useState(false);
  const [destinations, setDestinations] = useState<Destination[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [started, setStarted] = useState<LinkStart | null>(null);
  const [link, setLink] = useState<MessengerLink | null>(null);
  const [removing, setRemoving] = useState<Destination | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const loadDestinations = useCallback(async (signal?: AbortSignal) => {
    const response = await api<{
      enabled?: boolean;
      emailEnabled?: boolean;
      destinations: Destination[];
    }>("/messenger-destinations", { signal });
    if (signal?.aborted) return;
    setEnabled(response.enabled !== false);
    setEmailEnabled(response.emailEnabled === true);
    setDestinations(response.destinations ?? []);
    setError("");
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoadError("");
    const timer = window.setTimeout(() => {
      controller.abort();
      setLoadError(
        "Загрузка чатов заняла слишком много времени. Повторите попытку.",
      );
    }, 10_000);
    void loadDestinations(controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(cause));
      })
      .finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [loadDestinations, loadAttempt]);
  useEffect(() => {
    if (!link || (link.status !== "pending" && link.status !== "observed"))
      return;
    let active = true;
    let timer: number;
    let requestTimeout: number;
    let request: AbortController | null = null;
    const poll = async () => {
      if (document.visibilityState === "visible") {
        request = new AbortController();
        requestTimeout = window.setTimeout(() => request?.abort(), 10000);
        try {
          const response = await api<{ link: MessengerLink }>(
            `/messenger-links/${encodeURIComponent(link.id)}`,
            { signal: request.signal },
          );
          if (active) {
            setLink(response.link);
            setError("");
          }
        } catch (e) {
          if (active) setError(errorMessage(e));
        } finally {
          window.clearTimeout(requestTimeout);
          request = null;
        }
      }
      if (active) timer = window.setTimeout(() => void poll(), 3000);
    };
    timer = window.setTimeout(() => void poll(), 3000);
    return () => {
      active = false;
      window.clearTimeout(timer);
      window.clearTimeout(requestTimeout);
      request?.abort();
    };
  }, [link]);
  useEffect(() => {
    if (!link || !["pending", "observed", "candidate"].includes(link.status))
      return;
    const remaining = Date.parse(link.expiresAt) - Date.now();
    const timer = window.setTimeout(
      () => {
        setLink((value) =>
          value?.id === link.id &&
          ["pending", "observed", "candidate"].includes(value.status)
            ? { ...value, status: "expired" }
            : value,
        );
      },
      Math.max(0, remaining),
    );
    return () => window.clearTimeout(timer);
  }, [link]);
  async function begin(provider: Provider, kind: DestinationKind) {
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await api<LinkStart>("/messenger-links", {
        method: "POST",
        authenticated: true,
        body: { provider, kind },
      });
      setStarted(response);
      setLink(response.link);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function confirm() {
    if (!link || link.status !== "candidate") return;
    if (Date.parse(link.expiresAt) <= Date.now()) {
      setLink({ ...link, status: "expired" });
      return;
    }
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await api<{ destination: Destination }>(
        `/messenger-links/${encodeURIComponent(link.id)}/confirm`,
        { method: "POST", authenticated: true },
      );
      setDestinations((current) => [
        ...(current ?? []).filter(
          (item) => item.id !== response.destination.id,
        ),
        response.destination,
      ]);
      setStarted(null);
      setLink(null);
      setSuccess("Чат подтверждён. Теперь его можно выбрать для монитора.");
      try {
        await loadDestinations();
      } catch (e) {
        setError(`Чат подтверждён, но список не обновился: ${errorMessage(e)}`);
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function disconnect() {
    if (!removing) return;
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      await api(`/messenger-destinations/${encodeURIComponent(removing.id)}`, {
        method: "DELETE",
        authenticated: true,
      });
      setDestinations(
        (current) => current?.filter((item) => item.id !== removing.id) ?? null,
      );
      setRemoving(null);
      setSuccess(
        removing.provider === "email"
          ? "Email отключён. Новые попытки доставки остановлены."
          : "Чат отключён. Уведомления для него остановлены.",
      );
      try {
        await loadDestinations();
      } catch (e) {
        setError(`Чат отключён, но список не обновился: ${errorMessage(e)}`);
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function connectEmail() {
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const { destination } = await api<{ destination: Destination }>(
        "/messenger-destinations/email",
        {
          method: "POST",
          authenticated: true,
          body: {},
        },
      );
      setDestinations((current) => [
        ...(current ?? []).filter((item) => item.provider !== "email"),
        destination,
      ]);
      setSuccess(
        "Email подключён. Включите его в настройках уведомлений нужных мониторов.",
      );
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }
  const emailDestination = destinations?.find(
    (item) => item.provider === "email" && item.ready,
  );
  const chats = destinations?.filter((item) => item.provider !== "email") ?? [];
  const botUrl = safeBotUrl(started?.startUrl ?? null);
  return (
    <section
      className="panel messenger-panel"
      aria-labelledby="messenger-heading"
    >
      <div className="panel-heading">
        <span className="panel-index">05</span>
        <div>
          <h2 id="messenger-heading">Каналы уведомлений</h2>
          <p>
            Подключите email аккаунта или подтвердите чат через Informer, затем
            включите канал в настройках нужного монитора.
          </p>
        </div>
      </div>
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      {loadError ? (
        <div className="form-stack">
          <Notice>{loadError}</Notice>
          <button
            type="button"
            className="secondary"
            onClick={() => setLoadAttempt((value) => value + 1)}
          >
            Повторить загрузку чатов
          </button>
        </div>
      ) : !destinations ? (
        <Loading text="Загружаем чаты…" />
      ) : !enabled && !emailEnabled ? (
        <p className="small muted">Подключение каналов сейчас недоступно.</p>
      ) : (
        <>
          {emailEnabled && (
            <div className="messenger-route">
              <h3>Email</h3>
              <strong>
                {emailDestination?.email ??
                  emailDestination?.label ??
                  email ??
                  "Email аккаунта"}
              </strong>
              <p className="small muted">
                {emailDestination
                  ? "Подтверждённый email. Уведомления включаются отдельно для каждого монитора."
                  : "Используется только подтверждённый email аккаунта. Другой адрес указать нельзя."}
              </p>
              {emailDestination ? (
                <button
                  className="quiet danger-text"
                  disabled={busy}
                  onClick={() => setRemoving(emailDestination)}
                >
                  Отключить email
                </button>
              ) : (
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => void connectEmail()}
                >
                  {busy ? "Подключаем…" : "Подключить email аккаунта"}
                </button>
              )}
            </div>
          )}
          {enabled && (
            <>
              <div className="messenger-options">
                {linkOptions.map((option) => (
                  <button
                    key={`${option.provider}-${option.kind}`}
                    className="secondary"
                    disabled={busy}
                    onClick={() => void begin(option.provider, option.kind)}
                  >
                    ＋ {option.title}
                  </button>
                ))}
              </div>
              {chats.length ? (
                <ul className="messenger-destinations">
                  {chats.map((destination) => (
                    <li key={destination.id}>
                      <div>
                        <strong>{destination.label}</strong>
                        <span className="small muted">
                          {providerName[destination.provider]} ·{" "}
                          {destinationKindName[destination.kind]}
                          {!destination.ready &&
                            " · бот недоступен в этом чате"}
                        </span>
                      </div>
                      <button
                        className="quiet danger-text"
                        disabled={busy}
                        onClick={() => setRemoving(destination)}
                      >
                        Отключить
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="small muted">Пока нет подтверждённых чатов.</p>
              )}
            </>
          )}
        </>
      )}
      {link && started && (
        <Dialog
          title="Подключить чат"
          onClose={() => {
            setLink(null);
            setStarted(null);
            setError("");
          }}
          busy={busy}
        >
          <p className="dialog-description">
            {providerName[link.provider]} · {destinationKindName[link.kind]}.
            Ссылка действует до {dateTime(link.expiresAt)}.
          </p>
          {error && <Notice>{error}</Notice>}
          {(link.status === "pending" || link.status === "observed") && (
            <>
              {botUrl ? (
                <a
                  className="button secondary"
                  href={botUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Открыть Informer в {providerName[link.provider]} ↗
                </a>
              ) : started.command ? (
                <div className="messenger-command">
                  <p>
                    Сначала подключите личный Telegram того же администратора.
                    Убедитесь, что администратор и Informer имеют доступ к
                    группе
                    {link.kind === "topic" ? " и нужному топику" : ""}.
                  </p>
                  <p>
                    Добавьте Informer в{" "}
                    {link.kind === "topic"
                      ? "группу и отправьте команду в нужном топике"
                      : "группу и отправьте команду в ней"}
                    :
                  </p>
                  <code>{started.command}</code>
                </div>
              ) : (
                <Notice>
                  Ссылка бота пока не настроена. Подключение недоступно.
                </Notice>
              )}
              <p className="small muted" role="status">
                {link.status === "observed"
                  ? "Сообщение получено. Бот проверяет доступ к чату; подтверждение появится после проверки."
                  : "Ожидаем сообщение боту. После проверки вы увидите найденный чат."}
              </p>
            </>
          )}
          {link.status === "candidate" && link.candidate && (
            <>
              <p>Informer обнаружил чат:</p>
              <div className="messenger-candidate">
                <strong>{link.candidate.label}</strong>
                <span className="small muted">
                  {providerName[link.provider]} ·{" "}
                  {destinationKindName[link.kind]}
                </span>
              </div>
              <p className="small muted">
                Проверьте название чата перед подтверждением. Уведомления ещё не
                включены.
              </p>
              <div className="dialog-actions">
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => void confirm()}
                >
                  {busy ? "Подтверждаем…" : "Подтвердить этот чат"}
                </button>
              </div>
            </>
          )}
          {link.status === "expired" && (
            <Notice>Срок привязки истёк. Начните подключение заново.</Notice>
          )}
          {link.status === "confirmed" && (
            <>
              <Notice success>Чат уже подтверждён в кабинете.</Notice>
              <button
                className="secondary"
                onClick={() => {
                  void loadDestinations().catch((e) =>
                    setError(errorMessage(e)),
                  );
                  setLink(null);
                  setStarted(null);
                }}
              >
                Обновить список чатов
              </button>
            </>
          )}
        </Dialog>
      )}
      {removing && (
        <Dialog
          title={
            removing.provider === "email"
              ? "Отключить email?"
              : "Отключить чат?"
          }
          onClose={() => setRemoving(null)}
          busy={busy}
        >
          {error && <Notice>{error}</Notice>}
          <p className="dialog-description">
            Уведомления мониторов в «{removing.label}» прекратятся.
          </p>
          <div className="dialog-actions">
            <button
              className="secondary"
              disabled={busy}
              onClick={() => setRemoving(null)}
            >
              Отмена
            </button>
            <button
              className="danger"
              disabled={busy}
              onClick={() => void disconnect()}
            >
              {busy ? "Отключаем…" : "Отключить"}
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
