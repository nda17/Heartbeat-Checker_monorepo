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
  const { profile } = useSession();
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
  const [success, setSuccess] = useState("");
  const [busy, setBusy] = useState("");
  const selectedId = useRef<string | null>(null);
  const detailRequest = useRef(0);
  const accountRequest = useRef(0);
  const auditRequest = useRef(0);
  const mutationBusy = useRef(false);

  const onMfaState = useCallback((value: MfaState | null) => setMfa(value), []);
  const canRead = !!mfa?.verifiedUntil && Date.parse(mfa.verifiedUntil) > now;
  const canWrite =
    canRead && !!mfa?.stepUpUntil && Date.parse(mfa.stepUpUntil) > now;
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 15_000);
    return () => window.clearInterval(timer);
  }, []);

  const loadAccounts = useCallback(
    async (search: string, cursor = "", append = false) => {
      const request = ++accountRequest.current;
      const params = new URLSearchParams({ limit: "25" });
      if (search) params.set("q", search);
      if (cursor) params.set("cursor", cursor);
      const result = await api<{
        items: AdminAccount[];
        nextCursor: string | null;
      }>(`/admin/accounts?${params}`);
      if (request !== accountRequest.current) return;
      setAccounts((current) =>
        append ? [...(current ?? []), ...result.items] : result.items,
      );
      setNextCursor(result.nextCursor);
    },
    [],
  );
  const loadAudit = useCallback(
    async (accountId: string, cursor = "", append = false) => {
      const request = ++auditRequest.current;
      const params = new URLSearchParams({ accountId, limit: "20" });
      if (cursor) params.set("cursor", cursor);
      const result = await api<{
        items: AuditEvent[];
        nextCursor: string | null;
      }>(`/admin/audit?${params}`);
      if (request !== auditRequest.current || selectedId.current !== accountId)
        return;
      setAudit((current) =>
        append ? [...(current ?? []), ...result.items] : result.items,
      );
      setAuditCursor(result.nextCursor);
    },
    [],
  );
  const loadDetail = useCallback(async (id: string) => {
    const request = ++detailRequest.current;
    ++auditRequest.current;
    const prefix = `/admin/accounts/${encodeURIComponent(id)}`;
    const params = new URLSearchParams({ accountId: id, limit: "20" });
    const [accountResult, monitorResult, auditResult] = await Promise.all([
      api<AccountDetail>(prefix),
      api<{ monitors: Monitor[] }>(`${prefix}/monitors`),
      api<{ items: AuditEvent[]; nextCursor: string | null }>(
        `/admin/audit?${params}`,
      ),
    ]);
    if (request !== detailRequest.current || selectedId.current !== id) return;
    setDetail(accountResult);
    setMonitors(monitorResult.monitors);
    setAudit(auditResult.items);
    setAuditCursor(auditResult.nextCursor);
  }, []);
  useEffect(() => {
    if (!canRead || accounts !== null) return;
    // Bootstrap the MFA-gated list after the asynchronous API request.
    void loadAccounts(query).catch((cause) => setError(errorMessage(cause)));
  }, [accounts, canRead, loadAccounts, query]);

  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = String(
      new FormData(event.currentTarget).get("q") ?? "",
    ).trim();
    setQuery(value);
    setError("");
    ++accountRequest.current;
    setAccounts(null);
  }
  async function select(id: string) {
    if (mutationBusy.current) return;
    selectedId.current = id;
    ++detailRequest.current;
    ++auditRequest.current;
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
      setError(errorMessage(cause));
    }
  }
  async function mutate(
    label: string,
    action: () => Promise<unknown>,
    message: string,
  ) {
    if (!canWrite) {
      setError("Для изменения данных повторно подтвердите вход в блоке MFA.");
      return;
    }
    if (mutationBusy.current) return;
    mutationBusy.current = true;
    const targetId = detail?.account.id ?? null;
    setBusy(label);
    setError("");
    setSuccess("");
    try {
      await action();
      if (targetId && selectedId.current === targetId)
        await loadDetail(targetId);
      await loadAccounts(query);
      setSuccess(message);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 401) {
        router.replace("/login");
        return;
      }
      setError(errorMessage(cause));
      if (targetId && selectedId.current === targetId)
        void loadDetail(targetId).catch(() => undefined);
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
    const validUntil = new Date(String(data.get("validUntil"))).toISOString();
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
      <AdminMfa onState={onMfaState} />
      {error && <Notice>{error}</Notice>}
      {success && <Notice success>{success}</Notice>}
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
              <label className="visually-hidden" htmlFor="admin-account-search">
                Поиск аккаунтов
              </label>
              <input
                id="admin-account-search"
                name="q"
                defaultValue={query}
                maxLength={200}
                placeholder="Имя или email"
              />
              <button className="secondary" disabled={!!busy}>
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
                      disabled={!!busy}
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
                onClick={() =>
                  void loadAccounts(query, nextCursor, true).catch((cause) =>
                    setError(errorMessage(cause)),
                  )
                }
              >
                Показать ещё
              </button>
            )}
          </section>
          <div className="admin-detail">
            {!detail ? (
              <section className="panel">
                <p className="muted">Выберите аккаунт, чтобы увидеть детали.</p>
              </section>
            ) : (
              <>
                <section className="panel admin-detail-panel">
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
                        <button className="secondary" disabled={!!busy}>
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
                          disabled={!!busy || detail.sessionsCount === 0}
                        >
                          Завершить все сессии
                        </button>
                      </form>
                    </>
                  )}
                </section>
                <section className="panel admin-detail-panel">
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
                        <button className="secondary" disabled={!!busy}>
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
                            disabled={!!busy}
                          >
                            Отозвать квоту
                          </button>
                        </form>
                      )}
                    </>
                  )}
                </section>
                <section className="panel admin-detail-panel">
                  <div className="panel-heading">
                    <div>
                      <h2>Мониторы</h2>
                      <p>Настройки проверок выбранного аккаунта.</p>
                    </div>
                    {canWrite && (
                      <button
                        className="secondary"
                        type="button"
                        onClick={() => setMonitorForm("new")}
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
                                disabled={!!busy}
                                onClick={() => setMonitorForm(monitor)}
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
                                  disabled={!!busy}
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
                <section className="panel admin-detail-panel">
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
                      onClick={() =>
                        void loadAudit(
                          detail.account.id,
                          auditCursor,
                          true,
                        ).catch((cause) => setError(errorMessage(cause)))
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
      {monitorForm && detail && canWrite && (
        <MonitorForm
          monitor={monitorForm === "new" ? undefined : monitorForm}
          basePath={accountPath("/monitors")}
          requireReason
          onClose={() => setMonitorForm(null)}
          onSaved={async () => {
            await loadDetail(detail.account.id);
          }}
        />
      )}
    </>
  );
}
