"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { MonitorForm } from "@/features/monitor-form";
import { MonitorNotifications } from "@/features/monitor-notifications";
import {
  CheckResult,
  Entitlement,
  Incident,
  Monitor,
  Status,
  checkTiming,
  checkTypeLabel,
  monitorCheckType,
  monitorTarget,
  resultErrorMessage,
} from "@/entities/monitor";
import { api, ApiError, errorMessage, safeAppReturnTo } from "@/shared/api";
import { Dialog } from "@/shared/dialog";
import { useSession } from "@/entities/session";
import {
  clearMonitorDraft,
  draftChanged,
  MonitorDraft,
  readMonitorDraft,
} from "@/shared/android";
import { dateTime, Loading, Notice } from "@/shared/ui";
type Detail = {
  monitor: Monitor;
  results: CheckResult[];
  incidents: Incident[];
  targetIncidentId?: string;
};
const incidentCloseReasons: Record<string, string> = {
  recovered: "Сервис восстановился",
  config_changed: "Настройки изменены",
  paused: "Монитор приостановлен",
  deleted: "Монитор удалён",
};
export function MonitorsPage() {
  const { profile } = useSession();
  const [sharedDraft, setSharedDraft] = useState<MonitorDraft | null>(null);
  const [formDraft, setFormDraft] = useState<
    (MonitorDraft & { accountId: string }) | null
  >(null);
  const [monitors, setMonitors] = useState<Monitor[] | null>(null);
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const [editor, setEditor] = useState<Monitor | "new" | null>(null);
  const [deleteError, setDeleteError] = useState("");
  const [deleting, setDeleting] = useState<Monitor | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailError, setDetailError] = useState("");
  const [notifications, setNotifications] = useState<Monitor | null>(null);
  const [filter, setFilter] = useState("");
  const openedDeepLink = useRef("");
  const detailRequest = useRef(0);
  const listRequest = useRef(0);
  const router = useRouter();
  const searchParams = useSearchParams();
  const deepLink = safeAppReturnTo(`/app?${searchParams.toString()}`);
  const load = useCallback(async () => {
    const request = ++listRequest.current;
    try {
      const [list, access] = await Promise.all([
        api<{ monitors: Monitor[] }>("/monitors"),
        api<{ entitlement: Entitlement }>("/entitlement"),
      ]);
      if (request !== listRequest.current) return;
      setMonitors(list.monitors ?? []);
      setEntitlement(access.entitlement);
      setUpdatedAt(new Date());
      setError("");
    } catch (e) {
      if (request !== listRequest.current) return;
      if (e instanceof ApiError && e.status === 401) {
        const returnTo = safeAppReturnTo(
          window.location.pathname + window.location.search,
        );
        router.replace(
          returnTo
            ? `/login?returnTo=${encodeURIComponent(returnTo)}`
            : "/login",
        );
      } else setError(errorMessage(e));
      throw e;
    }
  }, [router]);
  // Bootstrap browser-only session data; state changes after asynchronous API requests.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load().catch(() => undefined);
    const timer = setInterval(() => {
      if (document.visibilityState === "visible")
        void load().catch(() => undefined);
    }, 15000);
    return () => {
      clearInterval(timer);
      // eslint-disable-next-line react-hooks/exhaustive-deps -- Fence the latest read when leaving this route.
      ++listRequest.current;
      ++detailRequest.current;
    };
  }, [load]);
  useEffect(() => {
    const update = () => setSharedDraft(readMonitorDraft(profile.account.id));
    update();
    window.addEventListener(draftChanged, update);
    return () => window.removeEventListener(draftChanged, update);
  }, [profile.account.id]);
  async function toggle(monitor: Monitor) {
    setBusy(monitor.id);
    setError("");
    try {
      await api(`/monitors/${encodeURIComponent(monitor.id)}`, {
        method: "PATCH",
        authenticated: true,
        body: { enabled: !monitor.enabled, version: monitor.version },
      });
      await load().catch(() =>
        setError(
          "Изменение сохранено, но список не обновился. Обновите мониторы; повторять действие не нужно.",
        ),
      );
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }
  async function remove() {
    if (!deleting) return;
    setBusy(deleting.id);
    setDeleteError("");
    try {
      await api(`/monitors/${encodeURIComponent(deleting.id)}`, {
        method: "DELETE",
        authenticated: true,
      });
      setDeleting(null);
      await load().catch(() =>
        setError(
          "Монитор удалён, но список не обновился. Обновите мониторы; повторять удаление не нужно.",
        ),
      );
    } catch (e) {
      setDeleteError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }
  const showDetail = useCallback(
    async (monitor: Monitor, targetIncidentId?: string) => {
      const requestId = ++detailRequest.current;
      setBusy(`detail-${monitor.id}`);
      setError("");
      setDetailError("");
      try {
        const [results, incidents, target] = await Promise.all([
          api<{ results: CheckResult[] }>(
            `/monitors/${encodeURIComponent(monitor.id)}/results?limit=50`,
          ),
          api<{ incidents: Incident[] }>(
            `/monitors/${encodeURIComponent(monitor.id)}/incidents`,
          ),
          targetIncidentId
            ? api<{ incident: Incident }>(
                `/monitors/${encodeURIComponent(monitor.id)}/incidents/${encodeURIComponent(targetIncidentId)}`,
              )
            : Promise.resolve(null),
        ]);
        const listed = incidents.incidents ?? [];
        const targetIncident = target?.incident;
        if (requestId !== detailRequest.current) return;
        setDetail({
          monitor,
          results: results.results ?? [],
          incidents:
            targetIncident &&
            !listed.some((item) => item.id === targetIncident.id)
              ? [targetIncident, ...listed]
              : listed,
          targetIncidentId,
        });
      } catch (e) {
        if (requestId !== detailRequest.current) return;
        setError(
          targetIncidentId && e instanceof ApiError && e.status === 404
            ? "Монитор или инцидент не найден или больше недоступен."
            : errorMessage(e),
        );
      } finally {
        if (requestId === detailRequest.current) setBusy("");
      }
    },
    [],
  );
  useEffect(() => {
    if (!deepLink) {
      if (openedDeepLink.current) detailRequest.current += 1;
      openedDeepLink.current = "";
      return;
    }
    if (!monitors || openedDeepLink.current === deepLink) return;
    detailRequest.current += 1;
    const params = new URLSearchParams(deepLink.split("?")[1]);
    const monitor = monitors.find((item) => item.id === params.get("monitor"));
    const timer = window.setTimeout(() => {
      openedDeepLink.current = deepLink;
      if (monitor)
        void showDetail(monitor, params.get("incident") ?? undefined);
      else setError("Монитор или инцидент не найден или больше недоступен.");
    }, 0);
    return () => window.clearTimeout(timer);
    // React state refreshes every 15 seconds; each URL is opened only once.
  }, [deepLink, monitors, showDetail]);
  useEffect(() => {
    if (!detail?.targetIncidentId) return;
    const frame = requestAnimationFrame(() => {
      document
        .getElementById(`incident-${detail.targetIncidentId}`)
        ?.scrollIntoView({ block: "center" });
    });
    return () => cancelAnimationFrame(frame);
  }, [detail]);
  async function olderResults() {
    if (!detail || !detail.results.length) return;
    const request = ++detailRequest.current;
    setBusy("history");
    setDetailError("");
    try {
      const cursor = detail.results[detail.results.length - 1].checkedAt;
      const response = await api<{ results: CheckResult[] }>(
        `/monitors/${encodeURIComponent(detail.monitor.id)}/results?limit=50&before=${encodeURIComponent(cursor)}`,
      );
      if (request !== detailRequest.current) return;
      setDetail({
        ...detail,
        results: [...detail.results, ...(response.results ?? [])],
      });
      if (!response.results?.length)
        setDetailError("Более ранних результатов нет.");
    } catch (e) {
      if (request === detailRequest.current) setDetailError(errorMessage(e));
    } finally {
      if (request === detailRequest.current) setBusy("");
    }
  }
  const up = monitors?.filter((m) => m.state === "UP").length ?? 0;
  const down = monitors?.filter((m) => m.state === "DOWN").length ?? 0;
  const visible =
    monitors?.filter((m) =>
      `${m.name} ${monitorTarget(m)}`
        .toLowerCase()
        .includes(filter.toLowerCase()),
    ) ?? [];
  const quotaActive =
    entitlement &&
    entitlement.quota > 0 &&
    entitlement.activeUntil &&
    new Date(entitlement.activeUntil) > new Date();
  const canCreate =
    !!quotaActive &&
    (entitlement?.monitorCount ?? 0) < (entitlement?.quota ?? 0);
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">ВАШИ СЕРВИСЫ В ПОЛЕ ЗРЕНИЯ</p>
          <h1>
            Обзор мониторов<span className="heading-dot">.</span>
          </h1>
          <p>Проверки сайтов, портов и серверов, их состояние и история.</p>
        </div>
        <button
          className="primary"
          disabled={!canCreate || !!busy}
          onClick={() => setEditor("new")}
        >
          <span aria-hidden="true">＋</span> Добавить монитор
        </button>
      </div>
      {error && <Notice>{error}</Notice>}
      <div className="stats-grid">
        <div className="stat">
          <span>Всего мониторов</span>
          <strong>{monitors?.length ?? "—"}</strong>
          <small>В вашем пространстве</small>
        </div>
        <div className="stat">
          <span>
            <i className="stat-dot green" />
            Доступны
          </span>
          <strong>{monitors ? up : "—"}</strong>
          <small>Успешная последняя проверка</small>
        </div>
        <div className="stat">
          <span>
            <i className="stat-dot red" />
            Недоступны
          </span>
          <strong>{monitors ? down : "—"}</strong>
          <small>Неуспешная последняя проверка</small>
        </div>
        <div className="stat quota-stat">
          <span>Ваша квота</span>
          <strong>
            {entitlement ? (
              <>
                {entitlement.monitorCount}
                <em> / {entitlement.quota}</em>
              </>
            ) : (
              "—"
            )}
          </strong>
          <small>
            {quotaActive
              ? `До ${dateTime(entitlement?.activeUntil)}`
              : "Нет действующей квоты"}
          </small>
        </div>
      </div>
      {entitlement?.source === "local_test" && (
        <div className="local-banner">
          <span className="badge">Локальный тест</span>
          <p>
            Вам выдана тестовая квота для локальной проверки. Это не оплаченная
            подписка.
          </p>
        </div>
      )}
      {entitlement?.source === "mock_test" && (
        <div className="local-banner">
          <span className="badge">Квота</span>
          <p>
            Квота после демонстрации оплаты. Деньги не списывались. Срок
            действия: {dateTime(entitlement.activeUntil)}.
          </p>
        </div>
      )}
      {entitlement?.source === "admin_grant" && (
        <div className="local-banner">
          <span className="badge">Квота</span>
          <p>
            Доступ к проверкам выдан администратором. Лимит: {entitlement.quota}
            , срок действия: {dateTime(entitlement.activeUntil)}.
          </p>
        </div>
      )}
      {entitlement && !quotaActive && (
        <div className="quota-banner">
          <div>
            <h2>Проверки ждут действующую квоту</h2>
            <p>
              Кабинет и сохранённая история доступны. Для запуска проверок нужна
              действующая квота.
            </p>
            <Link href="/app/billing" className="text-link">
              Перейти к оплате →
            </Link>
          </div>
          <span className="quota-symbol" aria-hidden="true">
            Ⅱ
          </span>
        </div>
      )}
      {quotaActive && !canCreate && (
        <div className="local-banner">
          <p>
            Квота заполнена. Чтобы добавить новый адрес, измените существующий
            монитор или удалите ненужный. Мониторы на паузе также занимают
            квоту.
          </p>
        </div>
      )}
      <section className="panel monitor-panel">
        <div className="table-toolbar">
          <div>
            <h2>
              Мониторы <span className="count">{monitors?.length ?? "—"}</span>
            </h2>
            <p className="small muted">
              {updatedAt
                ? `Обновлено в ${updatedAt.toLocaleTimeString("ru", { hour: "2-digit", minute: "2-digit" })} · Автоматически каждые 15 секунд`
                : "Загружаем состояние"}
            </p>
          </div>
          <div className="toolbar-actions">
            <input
              className="search"
              type="search"
              placeholder="Найти монитор…"
              aria-label="Поиск мониторов"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
            <button
              className="icon-button"
              title="Обновить"
              aria-label="Обновить мониторы"
              onClick={() => void load().catch(() => undefined)}
            >
              ↻
            </button>
          </div>
        </div>
        {!monitors ? (
          <Loading text="Загружаем мониторы…" />
        ) : !monitors.length ? (
          <div className="empty-state">
            <div className="empty-icon" aria-hidden="true">
              ⌁
            </div>
            <h3>Всё начинается с первого адреса</h3>
            <p>
              {canCreate
                ? "Добавьте сайт или API, за которым хотите наблюдать. Результаты появятся после первой проверки."
                : "Здесь будут ваши сайты и API. Для создания монитора нужна действующая квота."}
            </p>
            {canCreate && (
              <button className="secondary" onClick={() => setEditor("new")}>
                ＋ Добавить первый монитор
              </button>
            )}
          </div>
        ) : !visible.length ? (
          <div className="empty-state">
            <h3>Совпадений нет</h3>
            <p>Попробуйте другое название или URL.</p>
          </div>
        ) : (
          <div className="table-scroll">
            <table className="monitor-table mobile-card-table" role="table">
              <thead>
                <tr>
                  <th>Монитор / адрес</th>
                  <th>Состояние</th>
                  <th>Результат и время</th>
                  <th>Последняя проверка</th>
                  <th>
                    <span className="sr-only">Действия</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {visible.map((m) => (
                  <tr key={m.id}>
                    <td data-label="Монитор / адрес">
                      <button
                        className="monitor-name"
                        disabled={!!busy}
                        onClick={() => void showDetail(m)}
                      >
                        {m.name}
                        <span aria-hidden="true">↗</span>
                      </button>
                      <span className="monitor-url" title={monitorTarget(m)}>
                        {monitorCheckType(m.checkType) === "http"
                          ? m.method
                          : checkTypeLabel(m.checkType)}{" "}
                        <span>{monitorTarget(m)}</span>
                      </span>
                      <span className="cell-sub">
                        Интервал: {m.intervalMinutes} мин · Порог:{" "}
                        {m.failureThreshold}
                      </span>
                    </td>
                    <td data-label="Состояние">
                      <Status state={m.state} />
                    </td>
                    <td data-label="Результат и время">
                      <span className="mono">
                        {monitorCheckType(m.checkType) === "http"
                          ? `HTTP ${m.lastHttpStatus ?? "—"}`
                          : checkTypeLabel(m.checkType)}
                      </span>
                      <span className="cell-sub">
                        {checkTiming(
                          m.checkType,
                          m.state,
                          m.lastLatencyMs,
                          m.lastRoundTripMs,
                        )}
                      </span>
                    </td>
                    <td data-label="Последняя проверка">
                      {dateTime(m.lastCheckedAt)}
                      <span className="cell-sub">
                        {m.nextCheckAt
                          ? `Следующая: ${dateTime(m.nextCheckAt)}`
                          : m.enabled
                            ? "Ожидает расписания"
                            : "Проверки приостановлены"}
                      </span>
                    </td>
                    <td data-label="Действия">
                      <div className="row-actions">
                        <button
                          className="icon-button"
                          title={m.enabled ? "Приостановить" : "Включить"}
                          aria-label={`${m.enabled ? "Приостановить" : "Включить"} ${m.name}`}
                          disabled={!!busy}
                          onClick={() => void toggle(m)}
                        >
                          {busy === m.id ? "…" : m.enabled ? "Ⅱ" : "▷"}
                        </button>
                        <button
                          className="icon-button"
                          title="Настройки"
                          aria-label={`Настройки ${m.name}`}
                          disabled={!!busy}
                          onClick={() => setEditor(m)}
                        >
                          ⚙
                        </button>
                        <button
                          className="icon-button"
                          title="Уведомления"
                          aria-label={`Уведомления ${m.name}`}
                          disabled={!!busy}
                          onClick={() => setNotifications(m)}
                        >
                          ◔
                        </button>
                        <button
                          className="icon-button danger-text"
                          title="Удалить"
                          aria-label={`Удалить ${m.name}`}
                          disabled={!!busy}
                          onClick={() => {
                            setDeleteError("");
                            setDeleting(m);
                          }}
                        >
                          ×
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="table-footnote">
          <span>HTTP/HTTPS · TCP · ICMP · Интервал 1–10 мин</span>
          <span>
            Неизвестное состояние означает отсутствие свежего результата.
          </span>
        </div>
      </section>
      <div className="explanation">
        <span aria-hidden="true">ⓘ</span>
        <p>
          Инцидент открывается после заданного для монитора числа неудачных
          проверок подряд и закрывается при первом успешном ответе. Принятый
          неизвестный результат проверки (UNKNOWN) сбрасывает серию сбоев;
          отсутствие свежих данных само по себе её не сбрасывает. Пауза и
          отсутствие квоты отображаются отдельно.
        </p>
      </div>
      {sharedDraft && (
        <Notice success>
          Черновик из Android: {sharedDraft.name || sharedDraft.url}. Проверьте
          данные перед сохранением.
          <div className="dialog-actions">
            <button
              className="secondary"
              disabled={!!editor}
              onClick={() => {
                const current = readMonitorDraft(profile.account.id);
                if (!current) {
                  setSharedDraft(null);
                  return;
                }
                setFormDraft({ ...current, accountId: profile.account.id });
                clearMonitorDraft();
                setSharedDraft(null);
                setEditor("new");
              }}
            >
              Открыть черновик
            </button>
            <button
              className="quiet"
              onClick={() => {
                clearMonitorDraft();
                setSharedDraft(null);
              }}
            >
              Удалить черновик
            </button>
          </div>
        </Notice>
      )}
      {editor && (
        <MonitorForm
          monitor={editor === "new" ? undefined : editor}
          draft={editor === "new" ? (formDraft ?? undefined) : undefined}
          onClose={() => {
            setEditor(null);
            setFormDraft(null);
          }}
          onSaved={load}
        />
      )}{" "}
      {notifications && (
        <MonitorNotifications
          monitor={notifications}
          onClose={() => setNotifications(null)}
        />
      )}
      {deleting && (
        <Dialog
          title="Удалить монитор?"
          onClose={() => setDeleting(null)}
          busy={!!busy}
        >
          {deleteError && <Notice>{deleteError}</Notice>}
          <p className="dialog-description">
            Монитор «{deleting.name}» будет удалён. Его проверки прекратятся.
            Это действие нельзя отменить.
          </p>
          <div className="dialog-actions">
            <button
              className="secondary"
              disabled={!!busy}
              onClick={() => setDeleting(null)}
            >
              Отмена
            </button>
            <button
              className="danger"
              disabled={!!busy}
              onClick={() => void remove()}
            >
              {busy ? "Удаляем…" : "Удалить монитор"}
            </button>
          </div>
        </Dialog>
      )}
      {detail && (
        <Dialog
          title={detail.monitor.name}
          wide
          onClose={() => {
            detailRequest.current += 1;
            setDetail(null);
          }}
          busy={!!busy}
        >
          <div className="detail-summary">
            <Status state={detail.monitor.state} />
            <span className="mono">
              {checkTypeLabel(detail.monitor.checkType)}
              {monitorCheckType(detail.monitor.checkType) === "http"
                ? ` · ${detail.monitor.method}`
                : ""}
            </span>
            <span className="detail-url">{monitorTarget(detail.monitor)}</span>
          </div>
          <p className="small muted">
            Интервал: {detail.monitor.intervalMinutes} мин · Сбоев подряд до
            инцидента: {detail.monitor.failureThreshold}
          </p>
          <h3 className="detail-section-title">
            Инциденты <span className="count">{detail.incidents.length}</span>
          </h3>
          {detail.incidents.length ? (
            <div className="table-scroll">
              <table className="data-table mobile-card-table" role="table">
                <thead>
                  <tr>
                    <th>Номер</th>
                    <th>Начало</th>
                    <th>Подтверждён</th>
                    <th>Завершение</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.incidents.map((i) => (
                    <tr
                      key={i.id}
                      id={`incident-${i.id}`}
                      className={
                        i.id === detail.targetIncidentId
                          ? "incident-target"
                          : undefined
                      }
                      aria-label={
                        i.id === detail.targetIncidentId
                          ? "Инцидент из ссылки"
                          : undefined
                      }
                    >
                      <td data-label="Номер">
                        <span className="incident-number">№{i.number}</span>
                        {i.id === detail.targetIncidentId && (
                          <span className="cell-sub">Из уведомления</span>
                        )}
                      </td>
                      <td data-label="Начало">{dateTime(i.openedAt)}</td>
                      <td data-label="Подтверждён">
                        {dateTime(i.confirmedAt)}
                      </td>
                      <td data-label="Завершение">
                        {i.closedAt ? (
                          <>
                            {dateTime(i.closedAt)}
                            <span className="cell-sub">
                              {incidentCloseReasons[i.closeReason ?? ""] ??
                                "Причина не указана"}
                            </span>
                          </>
                        ) : (
                          <span className="badge down">Открыт</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="detail-empty">Подтверждённых инцидентов пока нет.</p>
          )}
          <h3 className="detail-section-title">Результаты проверок</h3>
          <p className="small muted">Подробные результаты хранятся 7 дней.</p>
          {detail.results.length ? (
            <>
              <div className="table-scroll">
                <table className="data-table mobile-card-table" role="table">
                  <thead>
                    <tr>
                      <th>Время</th>
                      <th>Результат</th>
                      <th>Тип проверки</th>
                      <th>Время проверки</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.results.map((r) => (
                      <tr key={r.id}>
                        <td data-label="Время">{dateTime(r.checkedAt)}</td>
                        <td data-label="Результат">
                          <span
                            className={`badge ${r.outcome === "UP" || r.outcome === "SUCCESS" ? "up" : r.outcome === "UNKNOWN" ? "unknown" : "down"}`}
                          >
                            {r.outcome === "UP" || r.outcome === "SUCCESS"
                              ? "Успех"
                              : r.outcome === "UNKNOWN"
                                ? "Ошибка исполнителя"
                                : "Ошибка"}
                          </span>
                          {monitorCheckType(r.checkType) === "http" && (
                            <span className="cell-sub">
                              HTTP {r.httpStatus ?? "—"}
                            </span>
                          )}
                          {r.errorCode && (
                            <span className="cell-sub">
                              {resultErrorMessage(r.errorCode)}
                            </span>
                          )}
                        </td>
                        <td className="mono" data-label="Тип проверки">
                          {checkTypeLabel(r.checkType)}
                        </td>
                        <td data-label="Время проверки">
                          {checkTiming(
                            r.checkType,
                            r.outcome,
                            r.latencyMs,
                            r.roundTripMs,
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {detail.results.length >= 50 && (
                <button
                  className="secondary history-button"
                  disabled={!!busy}
                  onClick={() => void olderResults()}
                >
                  {busy === "history"
                    ? "Загружаем…"
                    : "Более ранние результаты"}
                </button>
              )}
            </>
          ) : (
            <p className="detail-empty">
              Результатов ещё нет. Они появятся после выполнения проверок.
            </p>
          )}
          {detailError && (
            <p className="small" role="status">
              {detailError}
            </p>
          )}
        </Dialog>
      )}
    </>
  );
}
