"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useSession } from "@/entities/session";
import { Monitor, Status } from "@/entities/monitor";
import { AdminMfa, MfaState } from "@/features/admin-mfa";
import { MonitorForm } from "@/features/monitor-form";
import { api, ApiError, errorMessage } from "@/shared/api";
import { dateTime, Loading, Notice } from "@/shared/ui";

type AdminAccount = {
  id: string;
  name: string;
  email: string;
  role: "user" | "admin";
  status: "active" | "blocked";
  version: number;
  createdAt: string;
};
type AdminEntitlement = {
  quota: number;
  activeUntil: string | null;
  source: string;
  monitorCount: number;
  version: number;
};
type AccountDetail = {
  account: AdminAccount;
  entitlement: AdminEntitlement | null;
  sessionsCount: number;
};
type AuditEvent = {
  id: string;
  action: string;
  actorAccountId: string | null;
  targetAccountId: string | null;
  targetId: string | null;
  reason: string;
  createdAt: string;
};

function localDateTime(value: string | null) {
  if (!value) return "";
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16);
}

export function AdminPage() {
  const { profile, beginSessionTransition, endSessionTransition } =
    useSession();
  const router = useRouter();
  const [mfa, setMfa] = useState<MfaState | null>(null);
  const [now, setNow] = useState(0);
  const [query, setQuery] = useState("");
  const [accounts, setAccounts] = useState<AdminAccount[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [detail, setDetail] = useState<AccountDetail | null>(null);
  const [monitors, setMonitors] = useState<Monitor[] | null>(null);
  const [audit, setAudit] = useState<AuditEvent[] | null>(null);
  const [auditCursor, setAuditCursor] = useState<string | null>(null);
  const [monitorForm, setMonitorForm] = useState<Monitor | "new" | null>(null);
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [mfaBusy, setMfaBusy] = useState(false);
  const [detailTab, setDetailTab] = useState("profile");
  const [success, setSuccess] = useState("");
  const [busy, setBusy] = useState("");
  const selectedId = useRef<string | null>(null);
  const detailRequest = useRef(0);
  const accountRequest = useRef(0);
  const auditRequest = useRef(0);
  const mutationBusy = useRef(false);

  const accessError = useRef(false);
  const reportError = useCallback((cause: unknown) => {
    accessError.current =
      cause instanceof ApiError &&
      [
        "unauthorized",
        "invalid_csrf",
        "mfa_required",
        "mfa_step_up_required",
        "mfa_enrollment_required",
      ].includes(cause.code);
    setError(errorMessage(cause, "admin"));
  }, []);
  const accessTransition = useRef(false);
  const onMfaState = useCallback((value: MfaState | null) => {
    setMfa(value);
    setNow(Date.now());
  }, []);
  const onMfaBusy = useCallback(
    (value: boolean) => {
      if (accessTransition.current === value) return;
      accessTransition.current = value;
      ++accountRequest.current;
      ++detailRequest.current;
      ++auditRequest.current;
      if (value) beginSessionTransition();
      else endSessionTransition();
      setMfaBusy(value);
    },
    [beginSessionTransition, endSessionTransition],
  );
  const canRead =
    !!mfa?.enabled &&
    !!mfa?.verifiedUntil &&
    Date.parse(mfa.verifiedUntil) > now;
  const canWrite =
    canRead && !!mfa?.stepUpUntil && Date.parse(mfa.stepUpUntil) > now;
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => {
      window.clearInterval(timer);
      // Invalidate current requests when leaving the administration route.
      // eslint-disable-next-line react-hooks/exhaustive-deps
      ++accountRequest.current;
      // eslint-disable-next-line react-hooks/exhaustive-deps
      ++detailRequest.current;
      // eslint-disable-next-line react-hooks/exhaustive-deps
      ++auditRequest.current;
    };
  }, []);

  const loadAccounts = useCallback(
    async (search: string, cursor = "", append = false, force = false) => {
      if (accessTransition.current && !force) return;
      const request = ++accountRequest.current;
      const params = new URLSearchParams({ limit: "25" });
      if (search) params.set("q", search);
      if (cursor) params.set("cursor", cursor);
      try {
        const result = await api<{
          items: AdminAccount[];
          nextCursor: string | null;
        }>(`/admin/accounts?${params}`);
        if (request !== accountRequest.current) return;
        setAccounts((current) =>
          append ? [...(current ?? []), ...result.items] : result.items,
        );
        setNextCursor(result.nextCursor);
      } catch (cause) {
        if (request === accountRequest.current) throw cause;
      }
    },
    [],
  );
  const loadAudit = useCallback(
    async (accountId: string, cursor = "", append = false) => {
      if (accessTransition.current) return;
      const request = ++auditRequest.current;
      const params = new URLSearchParams({ accountId, limit: "20" });
      if (cursor) params.set("cursor", cursor);
      try {
        const result = await api<{
          items: AuditEvent[];
          nextCursor: string | null;
        }>(`/admin/audit?${params}`);
        if (
          request !== auditRequest.current ||
          selectedId.current !== accountId
        )
          return;
        setAudit((current) =>
          append ? [...(current ?? []), ...result.items] : result.items,
        );
        setAuditCursor(result.nextCursor);
      } catch (cause) {
        if (
          request === auditRequest.current &&
          selectedId.current === accountId
        )
          throw cause;
      }
    },
    [],
  );
  const loadDetail = useCallback(async (id: string, force = false) => {
    if (accessTransition.current && !force) return;
    const request = ++detailRequest.current;
    ++auditRequest.current;
    const prefix = `/admin/accounts/${encodeURIComponent(id)}`;
    const params = new URLSearchParams({ accountId: id, limit: "20" });
    try {
      const [accountResult, monitorResult, auditResult] = await Promise.all([
        api<AccountDetail>(prefix),
        api<{ monitors: Monitor[] }>(`${prefix}/monitors`),
        api<{ items: AuditEvent[]; nextCursor: string | null }>(
          `/admin/audit?${params}`,
        ),
      ]);
      if (request !== detailRequest.current || selectedId.current !== id)
        return;
      setDetail(accountResult);
      setMonitors(monitorResult.monitors);
      setAudit(auditResult.items);
      setAuditCursor(auditResult.nextCursor);
    } catch (cause) {
      if (request === detailRequest.current && selectedId.current === id)
        throw cause;
    }
  }, []);
  const onSessionVerified = useCallback(async () => {
    setNow(Date.now());
    if (accessError.current) {
      setError("");
      accessError.current = false;
    }
    setRefreshError("");
    ++accountRequest.current;
    ++detailRequest.current;
    ++auditRequest.current;
    try {
      await Promise.all([
        loadAccounts(query, "", false, true),
        selectedId.current
          ? loadDetail(selectedId.current, true)
          : Promise.resolve(),
      ]);
    } catch (cause) {
      setRefreshError(errorMessage(cause, "admin"));
      throw cause;
    }
  }, [loadAccounts, loadDetail, query]);
  useEffect(() => {
    if (!canRead || mfaBusy || accounts !== null) return;
    // Bootstrap the MFA-gated list after the asynchronous API request.
    // eslint-disable-next-line react-hooks/set-state-in-effect -- Fetch completes asynchronously; no state changes before the response.
    void loadAccounts(query).catch((cause) => reportError(cause));
  }, [accounts, canRead, mfaBusy, loadAccounts, query, reportError]);

  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (accessTransition.current || mutationBusy.current) return;
    const value = String(
      new FormData(event.currentTarget).get("q") ?? "",
    ).trim();
    setQuery(value);
    setError("");
    ++accountRequest.current;
    setAccounts(null);
  }
  async function select(id: string) {
    if (mutationBusy.current || accessTransition.current) return;
    selectedId.current = id;
    ++detailRequest.current;
    ++auditRequest.current;
    setDetailTab("profile");
    setSuccess("");
    setDetail(null);
    setMonitors(null);
    setAudit(null);
    setMonitorForm(null);
    setError("");
    try {
      await loadDetail(id);
    } catch (cause) {
      if (selectedId.current !== id) return;
      if (cause instanceof ApiError && cause.status === 401) {
        router.replace("/login");
        return;
      }
      reportError(cause);
    }
  }
  async function mutate(
    label: string,
    action: () => Promise<unknown>,
    message: string,
    logout = false,
  ) {
    if (!canWrite) {
      accessError.current = true;
      setError("Для изменения данных повторно подтвердите вход в блоке MFA.");
      return;
    }
    if (mutationBusy.current || accessTransition.current) return;
    mutationBusy.current = true;
    const targetId = detail?.account.id ?? null;
    setBusy(label);
    setError("");
    setSuccess("");
    setRefreshError("");
    try {
      try {
        await action();
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 401)
          router.replace("/login");
        else reportError(cause);
        return;
      }
      setSuccess(message);
      if (logout) {
        router.replace("/login");
        return;
      }
      try {
        await Promise.all([
          targetId && selectedId.current === targetId
            ? loadDetail(targetId)
            : Promise.resolve(),
          loadAccounts(query),
        ]);
      } catch {
        setRefreshError(
          "Изменение сохранено, но данные не обновились. Обновите данные; повторять действие не нужно.",
        );
      }
    } finally {
      mutationBusy.current = false;
      setBusy("");
    }
  }

  function accountPath(suffix = "") {
    return `/admin/accounts/${encodeURIComponent(detail?.account.id ?? "")}${suffix}`;
  }
  async function saveAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!detail) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    await mutate(
      "account",
      () =>
        api(accountPath(), {
          method: "PATCH",
          authenticated: true,
          body: {
            version: detail.account.version,
            name: String(data.get("name") ?? "").trim(),
            role: data.get("role"),
            status: data.get("status"),
            reason: String(data.get("reason") ?? "").trim(),
          },
        }),
      "Профиль обновлён. Сессии пользователя могли быть завершены.",
    );
  }
  async function saveEntitlement(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!detail) return;
    const data = new FormData(event.currentTarget);
    const date = new Date(String(data.get("validUntil") ?? ""));
    // eslint-disable-next-line react-hooks/purity -- Submit-time validation must compare with the current clock.
    if (!Number.isFinite(date.getTime()) || date.getTime() <= Date.now()) {
      accessError.current = false;
      setError("Укажите корректную дату окончания квоты в будущем.");
      return;
    }
    const validUntil = date.toISOString();
    await mutate(
      "entitlement",
      () =>
        api(accountPath("/entitlement"), {
          method: "PUT",
          authenticated: true,
          body: {
            version: detail.entitlement?.version ?? 0,
            quota: Number(data.get("quota")),
            validUntil,
            reason: String(data.get("reason") ?? "").trim(),
          },
        }),
      "Квота обновлена.",
    );
  }
  async function deleteEntitlement(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      !detail?.entitlement ||
      !window.confirm("Отозвать действующую квоту этого аккаунта?")
    )
      return;
    const data = new FormData(event.currentTarget);
    await mutate(
      "delete-entitlement",
      () =>
        api(accountPath("/entitlement"), {
          method: "DELETE",
          authenticated: true,
          body: {
            version: detail.entitlement?.version,
            reason: String(data.get("reason") ?? "").trim(),
          },
        }),
      "Квота отозвана.",
    );
  }
  async function revokeSessions(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!detail || !window.confirm("Завершить все сессии этого аккаунта?"))
      return;
    const data = new FormData(event.currentTarget);
    await mutate(
      "sessions",
      () =>
        api(accountPath("/revoke-sessions"), {
          method: "POST",
          authenticated: true,
          body: { reason: String(data.get("reason") ?? "").trim() },
        }),
      "Сессии завершены.",
      detail.account.id === profile.account.id,
    );
  }
  async function deleteMonitor(monitor: Monitor, reason: string) {
    if (!window.confirm(`Удалить монитор «${monitor.name}»?`)) return;
    await mutate(
      `monitor-${monitor.id}`,
      () =>
        api(accountPath(`/monitors/${encodeURIComponent(monitor.id)}`), {
          method: "DELETE",
          authenticated: true,
          body: { version: monitor.version, reason: reason.trim() },
        }),
      "Монитор удалён.",
    );
  }

  if (profile.account.role !== "admin") {
    return <Notice>Эта страница доступна только администратору.</Notice>;
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">УПРАВЛЕНИЕ СЕРВИСОМ</p>
          <h1>Администрирование</h1>
          <p>Аккаунты, квоты, мониторы и история действий.</p>
        </div>
      </div>
      <AdminMfa
        onState={onMfaState}
        onSessionVerified={onSessionVerified}
        onBusyChange={onMfaBusy}
        disabled={!!busy || !!monitorForm}
      />
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
      {refreshError && (
        <Notice>
          {refreshError}{" "}
          <button
            type="button"
            className="quiet"
            disabled={mfaBusy || !!busy}
            onClick={() => void onSessionVerified().catch(() => undefined)}
          >
            Обновить данные
          </button>
        </Notice>
      )}
      {canRead && (
        <div className="admin-layout">
          <section
            className="panel admin-accounts"
            aria-labelledby="admin-accounts-title"
          >
            <div className="panel-heading">
              <div>
                <h2 id="admin-accounts-title">Аккаунты</h2>
                <p>Поиск по имени и email.</p>
              </div>
            </div>
            <form className="admin-search" onSubmit={search}>
              <label className="sr-only" htmlFor="admin-account-search">
                Поиск аккаунтов
              </label>
              <input
                id="admin-account-search"
                name="q"
                defaultValue={query}
                maxLength={200}
                placeholder="Имя или email"
              />
              <button className="secondary" disabled={!!busy || mfaBusy}>
                Найти
              </button>
            </form>
            {!accounts ? (
              <Loading />
            ) : accounts.length === 0 ? (
              <p className="muted">Аккаунты не найдены.</p>
            ) : (
              <ul className="admin-account-list">
                {accounts.map((account) => (
                  <li key={account.id}>
                    <button
                      type="button"
                      className={
                        detail?.account.id === account.id ? "selected" : ""
                      }
                      disabled={!!busy || mfaBusy}
                      onClick={() => void select(account.id)}
                    >
                      <strong>{account.name}</strong>
                      <span>{account.email}</span>
                      <small>
                        {account.role === "admin"
                          ? "Администратор"
                          : "Пользователь"}{" "}
                        ·{" "}
                        {account.status === "active"
                          ? "Активен"
                          : "Заблокирован"}
                      </small>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {nextCursor && (
              <button
                className="quiet"
                type="button"
                disabled={mfaBusy || !!busy}
                onClick={() =>
                  void loadAccounts(query, nextCursor, true).catch((cause) =>
                    reportError(cause),
                  )
                }
              >
                Показать ещё
              </button>
            )}
          </section>
          <div className="admin-detail">
            {detail && (
              <div
                className="admin-detail-nav"
                role="group"
                aria-label="Разделы аккаунта"
              >
                {[
                  ["profile", "Профиль"],
                  ["quota", "Квота"],
                  ["monitors", "Мониторы"],
                  ["audit", "История"],
                ].map(([key, label]) => (
                  <button
                    type="button"
                    key={key}
                    className="quiet"
                    aria-pressed={detailTab === key}
                    onClick={() => {
                      setDetailTab(key);
                      setSuccess("");
                    }}
                  >
                    {label}
                  </button>
                ))}
              </div>
            )}
            {!detail ? (
              <section className="panel">
                <p className="muted">Выберите аккаунт, чтобы увидеть детали.</p>
              </section>
            ) : (
              <>
                <section
                  className="panel admin-detail-panel"
                  hidden={detailTab !== "profile"}
                >
                  <div className="panel-heading">
                    <div>
                      <h2>{detail.account.name}</h2>
                      <p>{detail.account.email}</p>
                    </div>
                  </div>
                  <p className="small muted">
                    ID: {detail.account.id} · Создан{" "}
                    {dateTime(detail.account.createdAt)} · Сессий:{" "}
                    {detail.sessionsCount}
                  </p>
                  <p className="small muted">
                    Роль:{" "}
                    {detail.account.role === "admin"
                      ? "администратор"
                      : "пользователь"}{" "}
                    · Статус:{" "}
                    {detail.account.status === "active"
                      ? "активен"
                      : "заблокирован"}
                  </p>
                  {canWrite && (
                    <>
                      <form
                        className="form-stack"
                        key={`account-${detail.account.id}-${detail.account.version}`}
                        onSubmit={saveAccount}
                      >
                        <h3>Профиль и права</h3>
                        <label>
                          Имя
                          <input
                            name="name"
                            defaultValue={detail.account.name}
                            required
                            minLength={2}
                            maxLength={100}
                          />
                        </label>
                        <div className="form-row">
                          <label>
                            Роль
                            <select
                              name="role"
                              defaultValue={detail.account.role}
                            >
                              <option value="user">Пользователь</option>
                              <option value="admin">Администратор</option>
                            </select>
                          </label>
                          <label>
                            Статус
                            <select
                              name="status"
                              defaultValue={detail.account.status}
                            >
                              <option value="active">Активен</option>
                              <option value="blocked">Заблокирован</option>
                            </select>
                          </label>
                        </div>
                        <label>
                          Причина
                          <input
                            name="reason"
                            required
                            minLength={1}
                            maxLength={500}
                          />
                        </label>
                        <button
                          className="secondary"
                          disabled={!!busy || mfaBusy}
                        >
                          Сохранить профиль и права
                        </button>
                      </form>
                      <form
                        className="form-stack admin-separated"
                        onSubmit={revokeSessions}
                      >
                        <h3>Доступ к аккаунту</h3>
                        <label>
                          Причина завершения сессий
                          <input
                            name="reason"
                            required
                            minLength={1}
                            maxLength={500}
                          />
                        </label>
                        <button
                          className="secondary"
                          disabled={
                            !!busy || mfaBusy || detail.sessionsCount === 0
                          }
                        >
                          Завершить все сессии
                        </button>
                      </form>
                    </>
                  )}
                </section>
                <section
                  className="panel admin-detail-panel"
                  hidden={detailTab !== "quota"}
                >
                  <div className="panel-heading">
                    <div>
                      <h2>Квота</h2>
                      <p>Доступ к проверкам для этого аккаунта.</p>
                    </div>
                  </div>
                  <p>
                    {detail.entitlement
                      ? `${detail.entitlement.monitorCount} из ${detail.entitlement.quota} мониторов · до ${dateTime(detail.entitlement.activeUntil)}`
                      : "Действующей квоты нет."}
                  </p>
                  {detail.entitlement && (
                    <p className="small muted">
                      Источник:{" "}
                      {detail.entitlement.source === "admin_grant"
                        ? "Выдано администратором"
                        : detail.entitlement.source}
                    </p>
                  )}
                  {canWrite && (
                    <>
                      <form
                        className="form-stack"
                        key={`entitlement-${detail.account.id}-${detail.entitlement?.version ?? 0}`}
                        onSubmit={saveEntitlement}
                      >
                        <div className="form-row">
                          <label>
                            Лимит мониторов
                            <input
                              type="number"
                              name="quota"
                              min={1}
                              max={1000}
                              defaultValue={detail.entitlement?.quota ?? 1}
                              required
                            />
                          </label>
                          <label>
                            Действует до
                            <input
                              type="datetime-local"
                              name="validUntil"
                              defaultValue={localDateTime(
                                detail.entitlement?.activeUntil ?? null,
                              )}
                              required
                            />
                          </label>
                        </div>
                        <label>
                          Причина
                          <input
                            name="reason"
                            required
                            minLength={1}
                            maxLength={500}
                          />
                        </label>
                        <button
                          className="secondary"
                          disabled={!!busy || mfaBusy}
                        >
                          Сохранить квоту
                        </button>
                      </form>
                      {detail.entitlement && (
                        <form
                          className="form-stack admin-separated"
                          onSubmit={deleteEntitlement}
                        >
                          <label>
                            Причина отзыва
                            <input
                              name="reason"
                              required
                              minLength={1}
                              maxLength={500}
                            />
                          </label>
                          <button
                            className="quiet danger-text"
                            disabled={!!busy || mfaBusy}
                          >
                            Отозвать квоту
                          </button>
                        </form>
                      )}
                    </>
                  )}
                </section>
                <section
                  className="panel admin-detail-panel"
                  hidden={detailTab !== "monitors"}
                >
                  <div className="panel-heading">
                    <div>
                      <h2>Мониторы</h2>
                      <p>Настройки проверок выбранного аккаунта.</p>
                    </div>
                    {canWrite && (
                      <button
                        className="secondary"
                        type="button"
                        disabled={mfaBusy || !!busy}
                        onClick={() => {
                          setSuccess("");
                          setMonitorForm("new");
                        }}
                      >
                        Добавить
                      </button>
                    )}
                  </div>
                  {!monitors ? (
                    <Loading />
                  ) : monitors.length === 0 ? (
                    <p className="muted">Мониторов пока нет.</p>
                  ) : (
                    <ul className="admin-monitor-list">
                      {monitors.map((monitor) => (
                        <li key={monitor.id}>
                          <div>
                            <strong>{monitor.name}</strong>
                            <p>{monitor.url}</p>
                            <Status state={monitor.state} />
                          </div>
                          {canWrite && (
                            <div className="admin-monitor-actions">
                              <button
                                className="quiet"
                                type="button"
                                disabled={!!busy || mfaBusy}
                                onClick={() => {
                                  setSuccess("");
                                  setMonitorForm(monitor);
                                }}
                              >
                                Изменить
                              </button>
                              <form
                                onSubmit={(event) => {
                                  event.preventDefault();
                                  void deleteMonitor(
                                    monitor,
                                    String(
                                      new FormData(event.currentTarget).get(
                                        "reason",
                                      ) ?? "",
                                    ),
                                  );
                                }}
                              >
                                <label>
                                  Причина удаления
                                  <input
                                    name="reason"
                                    required
                                    minLength={1}
                                    maxLength={500}
                                  />
                                </label>
                                <button
                                  className="quiet danger-text"
                                  disabled={!!busy || mfaBusy}
                                >
                                  Удалить
                                </button>
                              </form>
                            </div>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
                <section
                  className="panel admin-detail-panel"
                  hidden={detailTab !== "audit"}
                >
                  <div className="panel-heading">
                    <div>
                      <h2>Журнал действий</h2>
                      <p>Изменения, связанные с этим аккаунтом.</p>
                    </div>
                  </div>
                  {!audit ? (
                    <Loading />
                  ) : audit.length === 0 ? (
                    <p className="muted">Событий пока нет.</p>
                  ) : (
                    <ol className="admin-audit-list">
                      {audit.map((event) => (
                        <li key={event.id}>
                          <strong>{event.action}</strong>
                          <span>{dateTime(event.createdAt)}</span>
                          <p>{event.reason || "Без комментария"}</p>
                          <small>
                            Инициатор: {event.actorAccountId ?? "система"} ·
                            Объект:{" "}
                            {event.targetId ?? event.targetAccountId ?? "—"}
                          </small>
                        </li>
                      ))}
                    </ol>
                  )}
                  {auditCursor && (
                    <button
                      className="quiet"
                      type="button"
                      disabled={mfaBusy || !!busy}
                      onClick={() =>
                        void loadAudit(
                          detail.account.id,
                          auditCursor,
                          true,
                        ).catch((cause) => reportError(cause))
                      }
                    >
                      Показать ещё
                    </button>
                  )}
                </section>
              </>
            )}
          </div>
        </div>
      )}
      {canRead && !canWrite && (
        <Notice>
          Для изменения данных повторно подтвердите вход в блоке MFA.
        </Notice>
      )}
      {monitorForm && detail && (
        <MonitorForm
          monitor={monitorForm === "new" ? undefined : monitorForm}
          basePath={accountPath("/monitors")}
          requireReason
          onClose={() => setMonitorForm(null)}
          onSaved={async () => {
            await loadDetail(detail.account.id);
            setSuccess(
              monitorForm === "new" ? "Монитор добавлен." : "Монитор обновлён.",
            );
          }}
        />
      )}
    </>
  );
}
