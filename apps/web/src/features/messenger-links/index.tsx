"use client";
import { useCallback, useEffect, useRef, useState } from "react";
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
import { dateTime, Loading, Notice, Icon } from "@/shared/ui";

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

const linkFailureReasons: Record<string, string> = {
  telegram_private_required:
    "Сначала подключите личный Telegram администратора, который отправит команду. Используйте тот же профиль, без анонимной отправки и отправки от имени группы.",
  telegram_bot_admin_required:
    "Назначьте Informer администратором группы, затем начните подключение заново.",
  telegram_user_admin_required:
    "Команду должен отправить администратор группы с того же Telegram-профиля, который подключён в личном чате. Проверьте права и начните заново.",
  telegram_target_mismatch:
    "Тип чата не совпадает с выбранной привязкой. Для обычной группы выберите «Telegram · группа», для группы с темами — «Telegram · топик группы» и отправьте команду в нужном топике, кроме «Общие».",
  telegram_chat_unavailable:
    "Telegram не разрешил проверить чат. Убедитесь, что Informer добавлен в группу и назначен администратором, затем начните заново.",
  verification_unavailable:
    "Не удалось проверить доступ к чату. Попробуйте начать заново немного позже.",
  verification_failed:
    "Не удалось проверить эту привязку. Проверьте выбранный чат и начните заново.",
};
function linkFailureReason(link: MessengerLink) {
  if (link.status === "expired")
    return link.errorCode === "link_expired"
      ? "Срок действия ссылки истёк. Начните подключение заново."
      : "Эта попытка привязки недоступна. Начните заново.";
  return (
    linkFailureReasons[link.errorCode ?? ""] ??
    "Эта попытка привязки недоступна. Начните заново."
  );
}
async function linkRequest<T>(
  controller: AbortController,
  request: (signal: AbortSignal) => Promise<T>,
) {
  let rejectAbort: () => void = () => {};
  const aborted = new Promise<never>((_, reject) => {
    rejectAbort = () =>
      reject(new DOMException("Request aborted", "AbortError"));
    controller.signal.addEventListener("abort", rejectAbort, { once: true });
  });
  const timer = window.setTimeout(() => controller.abort(), 10_000);
  try {
    if (controller.signal.aborted)
      throw new DOMException("Request aborted", "AbortError");
    const response = await Promise.race([request(controller.signal), aborted]);
    if (controller.signal.aborted)
      throw new DOMException("Request aborted", "AbortError");
    return response;
  } finally {
    window.clearTimeout(timer);
    controller.signal.removeEventListener("abort", rejectAbort);
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
  const [pollError, setPollError] = useState("");
  const alive = useRef(true);
  const generation = useRef(0);
  const linkID = useRef<string | null>(null);
  const pollRequest = useRef<AbortController | null>(null);
  const linkAction = useRef<AbortController | null>(null);
  const destinationRefresh = useRef<AbortController | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      generation.current += 1;
      pollRequest.current?.abort();
      linkAction.current?.abort();
      destinationRefresh.current?.abort();
    };
  }, []);
  function replaceLink(value: MessengerLink | null) {
    linkID.current = value?.id ?? null;
    setLink(value);
  }
  function cancelDestinationRefresh() {
    const controller = destinationRefresh.current;
    destinationRefresh.current = null;
    controller?.abort();
  }
  function pauseLinkRequests() {
    ++generation.current;
    pollRequest.current?.abort();
    cancelDestinationRefresh();
    setPollError("");
    return generation.current;
  }
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
    if (
      !link ||
      busy ||
      !["pending", "observed", "candidate"].includes(link.status)
    )
      return;
    const id = link.id;
    const view = generation.current;
    let active = true;
    let timer: number;
    let controller: AbortController | null = null;
    const current = () =>
      active &&
      alive.current &&
      view === generation.current &&
      linkID.current === id &&
      !linkAction.current;
    const poll = async () => {
      if (!current()) return;
      if (document.visibilityState === "visible") {
        controller = new AbortController();
        pollRequest.current = controller;
        try {
          const response = await linkRequest(controller, (signal) =>
            api<{ link: MessengerLink }>(
              `/messenger-links/${encodeURIComponent(id)}`,
              { signal },
            ),
          );
          if (current() && response.link.id === id) {
            replaceLink(response.link);
            setPollError("");
          }
        } catch (e) {
          if (current())
            setPollError(
              e instanceof DOMException && e.name === "AbortError"
                ? "Проверка статуса заняла слишком много времени. Повторим запрос автоматически."
                : errorMessage(e),
            );
        } finally {
          if (pollRequest.current === controller) pollRequest.current = null;
        }
      }
      if (current()) timer = window.setTimeout(() => void poll(), 3000);
    };
    timer = window.setTimeout(() => void poll(), 3000);
    return () => {
      active = false;
      window.clearTimeout(timer);
      controller?.abort();
      if (pollRequest.current === controller) pollRequest.current = null;
    };
  }, [link, busy]);
  async function begin(provider: Provider, kind: DestinationKind) {
    if (linkAction.current || busy) return;
    const view = pauseLinkRequests();
    const controller = new AbortController();
    linkAction.current = controller;
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await linkRequest(controller, (signal) =>
        api<LinkStart>("/messenger-links", {
          method: "POST",
          authenticated: true,
          body: { provider, kind },
          signal,
        }),
      );
      if (!alive.current || view !== generation.current) return;
      setStarted(response);
      replaceLink(response.link);
    } catch (e) {
      if (alive.current && view === generation.current)
        setError(
          e instanceof DOMException && e.name === "AbortError"
            ? "Не удалось получить новую ссылку вовремя. Начните подключение заново."
            : errorMessage(e),
        );
    } finally {
      if (linkAction.current === controller) linkAction.current = null;
      if (alive.current && view === generation.current) setBusy(false);
    }
  }
  function confirmed(view: number) {
    setStarted(null);
    replaceLink(null);
    setError("");
    setSuccess("Чат подтверждён. Теперь его можно выбрать для монитора.");
    const controller = new AbortController();
    destinationRefresh.current = controller;
    void linkRequest(controller, (signal) => loadDestinations(signal))
      .catch((e) => {
        if (
          alive.current &&
          view === generation.current &&
          destinationRefresh.current === controller
        )
          setError(
            `Чат подтверждён, но список не обновился: ${errorMessage(e)}`,
          );
      })
      .finally(() => {
        if (destinationRefresh.current === controller)
          destinationRefresh.current = null;
      });
  }
  async function confirm() {
    if (!link || link.status !== "candidate" || linkAction.current || busy)
      return;
    const id = link.id;
    const view = pauseLinkRequests();
    let controller = new AbortController();
    linkAction.current = controller;
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const response = await linkRequest(controller, (signal) =>
        api<{ destination: Destination }>(
          `/messenger-links/${encodeURIComponent(id)}/confirm`,
          { method: "POST", authenticated: true, signal },
        ),
      );
      if (
        !alive.current ||
        view !== generation.current ||
        linkID.current !== id
      )
        return;
      setDestinations((current) => [
        ...(current ?? []).filter(
          (item) => item.id !== response.destination.id,
        ),
        response.destination,
      ]);
      confirmed(view);
    } catch (e) {
      if (
        !alive.current ||
        view !== generation.current ||
        linkID.current !== id
      )
        return;
      setError(
        `${errorMessage(e)} Проверяем состояние привязки; повторное подтверждение автоматически не отправляется.`,
      );
      controller = new AbortController();
      linkAction.current = controller;
      try {
        const response = await linkRequest(controller, (signal) =>
          api<{ link: MessengerLink }>(
            `/messenger-links/${encodeURIComponent(id)}`,
            { signal },
          ),
        );
        if (
          !alive.current ||
          view !== generation.current ||
          linkID.current !== id ||
          response.link.id !== id
        )
          return;
        if (response.link.status === "confirmed") confirmed(view);
        else replaceLink(response.link);
      } catch {
        if (
          alive.current &&
          view === generation.current &&
          linkID.current === id
        )
          setPollError(
            "Не удалось обновить состояние привязки. Проверим его снова автоматически.",
          );
      }
    } finally {
      if (linkAction.current === controller) linkAction.current = null;
      if (alive.current && view === generation.current) setBusy(false);
    }
  }
  async function disconnect() {
    if (!removing) return;
    cancelDestinationRefresh();
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
    cancelDestinationRefresh();
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
                    <Icon name="plus" /> {option.title}
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
            pauseLinkRequests();
            linkAction.current?.abort();
            replaceLink(null);
            setStarted(null);
            setError("");
          }}
          busy={busy}
        >
          <p className="dialog-description">
            {providerName[link.provider]} · {destinationKindName[link.kind]}.
            {["pending", "observed", "candidate"].includes(link.status) && (
              <> Ссылка действует до {dateTime(link.expiresAt)}.</>
            )}
          </p>
          {error && <Notice>{error}</Notice>}
          {pollError && <Notice>{pollError}</Notice>}
          {(link.status === "pending" || link.status === "observed") && (
            <>
              {botUrl ? (
                <a
                  className="button secondary"
                  href={botUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Открыть Informer в {providerName[link.provider]}{" "}
                  <Icon name="arrow-up-right" />
                </a>
              ) : started.command ? (
                <div className="messenger-command">
                  <p>
                    Informer и отправитель команды должны быть администраторами
                    группы. Отправляйте команду с того же Telegram-профиля,
                    который подключён в личном чате, без анонимной отправки и
                    отправки от имени группы.
                  </p>
                  <p>
                    {link.kind === "topic"
                      ? "Для группы с темами откройте нужный топик, кроме «Общие», и отправьте команду в нём. Для каждого топика нужна отдельная привязка:"
                      : "Для обычной группы без тем добавьте Informer и отправьте команду прямо в группе:"}
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
          {(link.status === "expired" || link.status === "failed") && (
            <>
              <Notice>{linkFailureReason(link)}</Notice>
              <button
                className="secondary"
                disabled={busy}
                onClick={() => void begin(link.provider, link.kind)}
              >
                {busy ? "Создаём ссылку…" : "Начать заново"}
              </button>
            </>
          )}
          {link.status === "confirmed" && (
            <>
              <Notice success>Чат уже подтверждён в кабинете.</Notice>
              <button
                className="secondary"
                onClick={() => {
                  const view = pauseLinkRequests();
                  confirmed(view);
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
