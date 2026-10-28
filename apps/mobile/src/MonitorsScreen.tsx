import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  AppState,
  Pressable,
  Alert,
  BackHandler,
  ScrollView,
  Switch,
  View,
} from "react-native";
import { api, errorText, session, sessionEpoch } from "./api";
import type { Entitlement, Incident, Monitor, Result } from "./types";
import {
  Button,
  Card,
  ErrorNotice,
  Field,
  Icon,
  type IconName,
  Label,
  Page,
  Title,
  usePalette,
} from "./ui";
import { NotificationsScreen } from "./NotificationsScreen";
import { useMonitorDraft, type MonitorForm } from "./MonitorDraft";
import {
  IncidentFeed,
  type IncidentFeedState,
  MonitorStatistics,
  closeReasonName,
} from "./MonitorInsights";

type Draft = MonitorForm;
type Zone = { id: string; label: string };
type Sort =
  "created_desc" | "created_asc" | "name_asc" | "name_desc" | "problems_first";
type MonitorPage = {
  monitors: Monitor[];
  pagination: {
    page: number;
    pageSize: number;
    total: number;
    totalPages: number;
  };
  summary: {
    total: number;
    up: number;
    down: number;
    unknown: number;
    paused: number;
    subscriptionRequired: number;
  };
  asOf: string;
};
const blank: Draft = {
  checkType: "http",
  name: "",
  url: "https://",
  host: "",
  port: "443",
  method: "GET",
  expectedStatus: "200",
  bodyContains: "",
  intervalMinutes: "5",
  failureThreshold: "2",
  timeZone: "Europe/Moscow",
  enabled: true,
};
const statusNames: Record<string, string> = {
  UP: "Доступен",
  DOWN: "Недоступен",
  UNKNOWN: "Ожидает проверки",
  PAUSED: "На паузе",
  SUBSCRIPTION_REQUIRED: "Нужна квота",
};
const resultNames: Record<string, string> = {
  UP: "Доступен",
  DOWN: "Недоступен",
  UNKNOWN: "Нет результата",
};
const checkFailureNames: Record<string, string> = {
  request_timeout: "Превышено время ожидания",
  tls_certificate_invalid: "Ошибка сертификата TLS",
  tls_handshake_failed: "Не удалось установить защищённое соединение",
  request_failed: "Ошибка соединения",
  redirect_limit: "Слишком много перенаправлений",
  invalid_redirect: "Недопустимое перенаправление",
  response_too_large: "Слишком большой ответ",
  unexpected_status: "Неожиданный HTTP-код",
  body_mismatch: "Ожидаемый текст не найден",
  blocked_target: "Адрес недоступен для проверки",
  probe_unavailable: "Проверяющий сервер временно недоступен",
  check_cancelled: "Проверка отменена",
};
const monitorStatus = (monitor: Monitor) =>
  monitor.state === "UNKNOWN" && monitor.lastCheckedAt
    ? "Нет свежих данных"
    : (statusNames[monitor.state] ?? "Состояние неизвестно");
function statusIcon(state: string): IconName {
  if (state === "UP") return "check";
  if (state === "DOWN") return "alert";
  if (state === "PAUSED") return "pause";
  if (state === "SUBSCRIPTION_REQUIRED") return "payment";
  return "info";
}
function date(value: string | null | undefined, timeZone: string) {
  if (!value) return "Нет данных";
  try {
    return new Intl.DateTimeFormat("ru-RU", {
      dateStyle: "short",
      timeStyle: "short",
      timeZone,
    }).format(new Date(value));
  } catch {
    return "Время недоступно";
  }
}

export function MonitorsScreen({
  deepLink,
  onBuyQuota,
  resumeDraft,
  onDeepLinkConsumed,
}: {
  onDeepLinkConsumed?: (link: {
    monitorId: string;
    incidentId: string;
  }) => void;
  onBuyQuota: () => void;
  resumeDraft?: number;
  deepLink?: { monitorId: string; incidentId: string } | null;
}) {
  const retained = useMonitorDraft();
  const [entitlementError, setEntitlementError] = useState("");
  const [clock, setClock] = useState(Date.now());
  const [entitlement, setEntitlement] = useState<Entitlement | null>(null);
  const [status, setStatus] = useState("all");
  const [feed, setFeed] = useState(false);
  const [feedState, setFeedState] = useState<IncidentFeedState>("active");
  const [feedLink, setFeedLink] = useState<{
    monitorId: string;
    incidentId: string;
  } | null>(null);
  const [advanced, setAdvanced] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [statisticsRefresh, setStatisticsRefresh] = useState(0);
  const [incidentsOpen, setIncidentsOpen] = useState(false);
  const [validation, setValidation] = useState(false);
  const [stale, setStale] = useState(false);
  const restoredDraft = useRef(false);
  const incidentNavigation = useRef(!!deepLink);
  const mounted = useRef(true);
  if (deepLink) incidentNavigation.current = true;
  const initialDraft = useRef("");
  const [list, setList] = useState<MonitorPage | null>(null);
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<Sort>("problems_first");
  const [filter, setFilter] = useState("");
  const [query, setQuery] = useState("");
  const [listLoading, setListLoading] = useState(false);
  const [selected, setSelected] = useState<Monitor | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState(false);
  const [notifications, setNotifications] = useState(false);
  const [draft, setDraft] = useState<Draft>(blank);
  const [zones, setZones] = useState<Zone[]>([]);
  const [zoneQuery, setZoneQuery] = useState("");
  const [zonePicker, setZonePicker] = useState(false);
  const [results, setResults] = useState<Result[]>([]);
  const [moreResults, setMoreResults] = useState(false);
  const [paginationBusy, setPaginationBusy] = useState(false);
  const [detailRefreshing, setDetailRefreshing] = useState(false);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [selectedIncident, setSelectedIncident] = useState<Incident | null>(
    null,
  );
  const incidentSource = useRef<"monitor" | "feed">("monitor");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const c = usePalette();
  const listRequest = useRef(0);
  const listAbort = useRef<AbortController | null>(null);
  const detailRequest = useRef(0);
  const detailPending = useRef(false);
  const paginationRequest = useRef(0);
  const firstResults = useRef<Result[]>([]);
  const editorBaseline = useRef<{ id: string; version: number } | null>(null);
  const filterTouched = useRef(false);
  const mutation = useRef(false);
  const selectedRef = useRef<Monitor | null>(null);
  const resultsRef = useRef<Result[]>([]);
  const selection = useRef({ page, sort, query, status });
  selection.current = { page, sort, query, status };
  selectedRef.current = selected;
  resultsRef.current = results;

  useEffect(() => {
    if (!retained.ready || restoredDraft.current) return;
    restoredDraft.current = true;
    if (retained.draft && !incidentNavigation.current && !feedLink) {
      const saved = retained.draft;
      setDraft(saved.form);
      setEditing(true);
      setCreating(!saved.editId);
      editorBaseline.current = saved.editId
        ? { id: saved.editId, version: saved.editVersion! }
        : null;
      initialDraft.current = JSON.stringify(saved.form);
    }
  }, [retained.ready, retained.draft, resumeDraft]);
  useEffect(() => {
    if (!editing || !retained.ready) return;
    retained.update({
      schemaVersion: 1,
      form: draft,
      editId: editorBaseline.current?.id ?? null,
      ...(editorBaseline.current
        ? { editVersion: editorBaseline.current.version }
        : {}),
    });
  }, [draft, editing, retained.ready]);
  const buyQuota = async () => {
    const capturedEpoch = sessionEpoch();
    retained.update({
      schemaVersion: 1,
      form: draft,
      editId: editorBaseline.current?.id ?? null,
      ...(editorBaseline.current
        ? { editVersion: editorBaseline.current.version }
        : {}),
    });
    try {
      await retained.flush();
      if (!mounted.current || sessionEpoch() !== capturedEpoch) return;
      onBuyQuota();
    } catch {
      if (!mounted.current || sessionEpoch() !== capturedEpoch) return;
      setError(
        "Не удалось сохранить черновик на телефоне. Повторите переход после восстановления хранилища.",
      );
    }
  };
  const refreshEntitlement = useCallback(async () => {
    try {
      const value = await api<{ entitlement: Entitlement }>("/entitlement");
      setEntitlement(value.entitlement);
      setEntitlementError("");
    } catch (cause) {
      setEntitlementError(errorText(cause));
    }
  }, []);
  useEffect(() => {
    void refreshEntitlement();
    const timer = setInterval(() => setClock(Date.now()), 30000);
    return () => clearInterval(timer);
  }, [refreshEntitlement]);
  const cancelEditor = () => {
    if (JSON.stringify(draft) !== initialDraft.current) {
      Alert.alert(
        "Выйти без сохранения?",
        "Несохранённые изменения будут удалены.",
        [
          { text: "Продолжить", style: "cancel" },
          {
            text: "Удалить изменения",
            style: "destructive",
            onPress: () => {
              retained.update(null);
              editorBaseline.current = null;
              setEditing(false);
              setCreating(false);
            },
          },
        ],
      );
    } else {
      retained.update(null);
      editorBaseline.current = null;
      setEditing(false);
      setCreating(false);
    }
  };
  const loadList = useCallback(async () => {
    listAbort.current?.abort();
    const controller = new AbortController();
    listAbort.current = controller;
    const request = ++listRequest.current;
    const current = selection.current;
    setListLoading(true);
    setError("");
    try {
      const params = new URLSearchParams({
        page: String(current.page),
        pageSize: "25",
        sort: current.sort,
        q: current.query,
        status: current.status,
      });
      const data = await api<MonitorPage>(`/monitors?${params.toString()}`, {
        signal: controller.signal,
      });
      if (
        request !== listRequest.current ||
        selection.current.page !== current.page ||
        selection.current.sort !== current.sort ||
        selection.current.query !== current.query ||
        selection.current.status !== current.status
      )
        return;
      setList(data);
      setStale(false);
      if (data.pagination.page !== current.page) setPage(data.pagination.page);
    } catch (cause) {
      if (!controller.signal.aborted && request === listRequest.current) {
        setError(errorText(cause));
        setStale(true);
      }
    } finally {
      if (request === listRequest.current) setListLoading(false);
      if (listAbort.current === controller) listAbort.current = null;
    }
  }, []);
  useEffect(() => {
    void loadList();
    return () => {
      listRequest.current++;
      listAbort.current?.abort();
      listAbort.current = null;
    };
  }, [page, sort, query, status, loadList]);
  useEffect(() => {
    if (!filterTouched.current) return;
    const timer = setTimeout(() => {
      const nextQuery = filter.trim().slice(0, 200);
      setPage(1);
      if (nextQuery === selection.current.query && selection.current.page === 1)
        void loadList();
      else setQuery(nextQuery);
    }, 300);
    return () => clearTimeout(timer);
  }, [filter, loadList]);
  useEffect(() => {
    let active = true;
    void api<{ timeZones: Zone[] }>("/monitor-time-zones")
      .then((data) => {
        if (active) setZones(data.timeZones);
      })
      .catch(() => {
        if (active)
          setError(
            "Список часовых поясов не загрузился. Текущий выбор сохранён.",
          );
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    const subscription = AppState.addEventListener("change", (next) => {
      if (next === "active") {
        void loadList();
        void refreshEntitlement();
        if (selectedRef.current && !editing && !notifications && !feed)
          void detail(selectedRef.current, true);
      }
    });
    return () => subscription.remove();
  }, [loadList, refreshEntitlement, editing, notifications, feed]);

  const detail = useCallback(async (monitor: Monitor, passive = false) => {
    if (!passive) editorBaseline.current = null;
    const request = ++detailRequest.current;
    paginationRequest.current++;
    setPaginationBusy(false);
    detailPending.current = true;
    setDetailRefreshing(true);
    setStatisticsRefresh((value) => value + 1);
    const switching = selectedRef.current?.id !== monitor.id;
    if (switching) {
      selectedRef.current = monitor;
      setSelected(monitor);
      setSelectedIncident(null);
      setResults([]);
      resultsRef.current = [];
      firstResults.current = [];
      setIncidents([]);
      setMoreResults(false);
    }
    if (switching && !passive) {
      setEditing(false);
      setHistoryOpen(false);
      setIncidentsOpen(false);
      setNotifications(false);
    }
    setError("");
    try {
      const [m, r, i] = await Promise.all([
        api<{ monitor: Monitor }>(`/monitors/${monitor.id}`),
        api<{ results: Result[] }>(`/monitors/${monitor.id}/results?limit=50`),
        api<{ incidents: Incident[] }>(`/monitors/${monitor.id}/incidents`),
      ]);
      if (
        request !== detailRequest.current ||
        selectedRef.current?.id !== monitor.id
      )
        return;
      selectedRef.current = m.monitor;
      setSelected(m.monitor);
      const hadOlderResults =
        resultsRef.current.length > firstResults.current.length;
      const seen = new Set(r.results.map((item) => item.id));
      const oldestFresh = r.results[r.results.length - 1]?.checkedAt;
      const preservedOlder =
        !switching && hadOlderResults && oldestFresh
          ? resultsRef.current.filter(
              (item) => !seen.has(item.id) && item.checkedAt < oldestFresh,
            )
          : [];
      firstResults.current = r.results;
      const mergedResults = [...r.results, ...preservedOlder];
      resultsRef.current = mergedResults;
      setResults(mergedResults);
      if (!preservedOlder.length) setMoreResults(r.results.length >= 50);
      setIncidents(i.incidents);
      setSelectedIncident((current) =>
        current
          ? (i.incidents.find((item) => item.id === current.id) ?? current)
          : null,
      );
    } catch (cause) {
      if (request === detailRequest.current) setError(errorText(cause));
    } finally {
      if (request === detailRequest.current) {
        detailPending.current = false;
        setDetailRefreshing(false);
      }
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      detailRequest.current++;
      paginationRequest.current++;
    };
  }, []);
  useEffect(() => {
    const targetLink = deepLink ?? feedLink;
    if (!targetLink) return;
    incidentSource.current = deepLink ? "monitor" : "feed";
    const request = ++detailRequest.current;
    incidentNavigation.current = true;
    setFeed(false);
    // Transfer the one-shot link into this guarded request before acknowledging it.
    setFeedLink(null);
    if (deepLink) onDeepLinkConsumed?.(deepLink);
    paginationRequest.current++;
    setPaginationBusy(false);
    detailPending.current = false;
    setDetailRefreshing(false);
    setError("");
    setSelected(null);
    setSelectedIncident(null);
    setEditing(false);
    setNotifications(false);
    void Promise.all([
      api<{ monitor: Monitor }>(`/monitors/${targetLink.monitorId}`),
      api<{ incident: Incident }>(
        `/monitors/${targetLink.monitorId}/incidents/${targetLink.incidentId}`,
      ),
    ])
      .then(([monitor, incident]) => {
        if (
          !mounted.current ||
          request !== detailRequest.current ||
          incident.incident.id !== targetLink.incidentId
        )
          return;
        // detail sets the monitor immediately; select the incident after that
        // so a later response cannot reopen a detail the user already closed.
        void detail(monitor.monitor);
        setSelectedIncident(incident.incident);
      })
      .catch((cause) => {
        if (mounted.current && request === detailRequest.current)
          setError(`Ссылка недоступна этому аккаунту. ${errorText(cause)}`);
      });
  }, [
    deepLink?.monitorId,
    deepLink?.incidentId,
    feedLink?.monitorId,
    feedLink?.incidentId,
    detail,
    onDeepLinkConsumed,
  ]);

  const olderResults = async () => {
    const monitor = selectedRef.current;
    const previous = resultsRef.current;
    if (!monitor || !previous.length || paginationBusy || detailPending.current)
      return;
    const request = detailRequest.current;
    const pagination = ++paginationRequest.current;
    setPaginationBusy(true);
    setError("");
    try {
      const cursor = previous[previous.length - 1].checkedAt;
      const data = await api<{ results: Result[] }>(
        `/monitors/${monitor.id}/results?limit=50&before=${encodeURIComponent(cursor)}`,
      );
      if (
        request !== detailRequest.current ||
        pagination !== paginationRequest.current ||
        selectedRef.current?.id !== monitor.id
      )
        return;
      const seen = new Set(previous.map((item) => item.id));
      const next = [
        ...previous,
        ...data.results.filter((item) => !seen.has(item.id)),
      ];
      resultsRef.current = next;
      setResults(next);
      setMoreResults(data.results.length >= 50);
      if (!data.results.length) setError("Более ранних результатов нет.");
    } catch (cause) {
      if (
        request === detailRequest.current &&
        pagination === paginationRequest.current
      )
        setError(errorText(cause));
    } finally {
      if (pagination === paginationRequest.current) setPaginationBusy(false);
    }
  };
  const collapseResults = () => {
    paginationRequest.current++;
    setPaginationBusy(false);
    resultsRef.current = firstResults.current;
    setResults(firstResults.current);
    setMoreResults(firstResults.current.length >= 50);
    setError("");
  };
  const changeFilter = (value: string) => {
    filterTouched.current = true;
    listRequest.current++;
    listAbort.current?.abort();
    listAbort.current = null;
    setListLoading(false);
    setFilter(value);
  };

  const edit = (monitor?: Monitor) => {
    detailRequest.current++;
    paginationRequest.current++;
    detailPending.current = false;
    setDetailRefreshing(false);
    setPaginationBusy(false);
    editorBaseline.current = monitor
      ? { id: monitor.id, version: monitor.version }
      : null;
    setDraft(
      monitor
        ? {
            checkType: monitor.checkType,
            name: monitor.name,
            url: monitor.url,
            host: monitor.host,
            port: String(monitor.port),
            method: monitor.method,
            expectedStatus: String(monitor.expectedStatus),
            bodyContains: monitor.bodyContains,
            intervalMinutes: String(monitor.intervalMinutes),
            failureThreshold: String(monitor.failureThreshold),
            timeZone: monitor.timeZone,
            enabled: monitor.enabled,
          }
        : { ...blank },
    );
    setValidation(false);
    setAdvanced(false);
    initialDraft.current = JSON.stringify(
      monitor
        ? {
            checkType: monitor.checkType,
            name: monitor.name,
            url: monitor.url,
            host: monitor.host,
            port: String(monitor.port),
            method: monitor.method,
            expectedStatus: String(monitor.expectedStatus),
            bodyContains: monitor.bodyContains,
            intervalMinutes: String(monitor.intervalMinutes),
            failureThreshold: String(monitor.failureThreshold),
            timeZone: monitor.timeZone,
            enabled: monitor.enabled,
          }
        : blank,
    );
    setZonePicker(false);
    setZoneQuery("");
    setError("");
    setEditing(true);
    setCreating(!monitor);
  };
  const save = async () => {
    if (mutation.current) return;
    setValidation(true);
    if (Object.keys(formErrors).length) {
      setError("Проверьте отмеченные поля.");
      return;
    }
    if (
      creating &&
      (!entitlement ||
        entitlement.quota === 0 ||
        (draft.enabled && entitlement.enabledMonitorCount >= entitlement.quota))
    ) {
      setError(
        entitlement
          ? "Для создания монитора нужна подходящая квота. Черновик сохранён."
          : "Не удалось проверить квоту. Обновите её и повторите.",
      );
      return;
    }
    mutation.current = true;
    setBusy(true);
    setError("");
    try {
      const body = {
        checkType: draft.checkType,
        name: draft.name.trim(),
        url: draft.checkType === "http" ? draft.url.trim() : "",
        host: draft.checkType === "http" ? "" : draft.host.trim(),
        port: draft.checkType === "tcp" ? Number(draft.port) : 0,
        method: draft.checkType === "http" ? draft.method : "",
        expectedStatus:
          draft.checkType === "http" ? Number(draft.expectedStatus) : 0,
        bodyContains:
          draft.checkType === "http" && draft.method === "GET"
            ? draft.bodyContains
            : "",
        timeZone: draft.timeZone,
        intervalMinutes: Number(draft.intervalMinutes),
        failureThreshold: Number(draft.failureThreshold),
        enabled: draft.enabled,
      };
      if (
        !body.name ||
        !body.timeZone ||
        !Number.isInteger(body.intervalMinutes) ||
        !Number.isInteger(body.failureThreshold) ||
        (zones.length > 0 && !zones.some((zone) => zone.id === body.timeZone))
      )
        throw new Error("invalid");
      const target = editorBaseline.current;
      if (!creating && !target) throw new Error("missing monitor");
      const response = creating
        ? await api<{ monitor: Monitor }>("/monitors", {
            method: "POST",
            body: { ...body, expectedAccountId: session()?.account.id },
          })
        : await api<{ monitor: Monitor }>(`/monitors/${target!.id}`, {
            method: "PATCH",
            body: { ...body, version: target!.version },
          });
      retained.update(null);
      void refreshEntitlement();
      // The write is committed. Subsequent read failures must never invite a second POST.
      setEditing(false);
      setCreating(false);
      editorBaseline.current = null;
      selectedRef.current = response.monitor;
      setSelected(response.monitor);
      void loadList();
      void detail(response.monitor);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      mutation.current = false;
      setBusy(false);
    }
  };
  const remove = () => {
    const monitor = selectedRef.current;
    if (!monitor || mutation.current) return;
    Alert.alert("Удалить монитор?", "История этого монитора будет удалена.", [
      { text: "Отмена", style: "cancel" },
      {
        text: "Удалить",
        style: "destructive",
        onPress: () =>
          void (async () => {
            if (mutation.current) return;
            mutation.current = true;
            setBusy(true);
            try {
              await api(`/monitors/${monitor.id}`, { method: "DELETE" });
              detailRequest.current++;
              paginationRequest.current++;
              setPaginationBusy(false);
              detailPending.current = false;
              setDetailRefreshing(false);
              setSelected(null);
              setSelectedIncident(null);
              void loadList();
              void refreshEntitlement();
            } catch (cause) {
              setError(errorText(cause));
            } finally {
              mutation.current = false;
              setBusy(false);
            }
          })(),
      },
    ]);
  };
  const closeIncident = () => {
    setSelectedIncident(null);
    if (incidentSource.current === "feed") {
      detailRequest.current++;
      paginationRequest.current++;
      detailPending.current = false;
      setDetailRefreshing(false);
      setPaginationBusy(false);
      setSelected(null);
      selectedRef.current = null;
      setFeed(true);
    }
  };
  useEffect(() => {
    const listener = BackHandler.addEventListener("hardwareBackPress", () => {
      if (feed) {
        setFeed(false);
        return true;
      }
      if (selectedIncident) {
        closeIncident();
        return true;
      }
      if (notifications) {
        setNotifications(false);
        return true;
      }
      if (editing) {
        cancelEditor();
        return true;
      }
      if (selected) {
        detailRequest.current++;
        paginationRequest.current++;
        setPaginationBusy(false);
        detailPending.current = false;
        setDetailRefreshing(false);
        setSelected(null);
        return true;
      }
      return false;
    });
    return () => listener.remove();
  }, [selectedIncident, notifications, editing, selected, draft, feed]);

  const formErrors: Partial<Record<keyof Draft, string>> = {};
  const integer = (value: string, min: number, max: number) =>
    /^\d+$/.test(value) && Number(value) >= min && Number(value) <= max;
  if (!draft.name.trim() || Array.from(draft.name.trim()).length > 100)
    formErrors.name = "От 1 до 100 символов";
  if (!integer(draft.intervalMinutes, 1, 10))
    formErrors.intervalMinutes = "От 1 до 10 минут";
  if (!integer(draft.failureThreshold, 1, 10))
    formErrors.failureThreshold = "От 1 до 10 проверок";
  if (draft.checkType === "http") {
    try {
      const target = new URL(draft.url.trim());
      if (
        !["http:", "https:"].includes(target.protocol) ||
        !target.hostname ||
        target.username ||
        target.password ||
        draft.url.length > 2048
      )
        throw new Error();
    } catch {
      formErrors.url = "Введите полный HTTP или HTTPS адрес";
    }
    if (!integer(draft.expectedStatus, 100, 599))
      formErrors.expectedStatus = "HTTP-код от 100 до 599";
    if (draft.bodyContains.length > 1000)
      formErrors.bodyContains = "Не более 1000 символов";
  } else {
    if (!draft.host.trim() || /[\s/]/.test(draft.host))
      formErrors.host = "Введите домен или IP без протокола и пути";
    if (draft.checkType === "tcp" && !integer(draft.port, 1, 65535))
      formErrors.port = "Порт от 1 до 65535";
  }
  const fieldError = (key: keyof Draft) =>
    validation ? formErrors[key] : undefined;
  if (feed)
    return (
      <IncidentFeed
        initialState={feedState}
        onStateChange={setFeedState}
        onBack={() => setFeed(false)}
        onOpen={(monitorId, incidentId) => {
          setFeed(false);
          setFeedLink({ monitorId, incidentId });
        }}
      />
    );
  if (!retained.ready)
    return (
      <Page title="Мониторы">
        <Label muted>Восстанавливаем черновик…</Label>
      </Page>
    );
  if (notifications && selected)
    return (
      <NotificationsScreen
        monitor={selected}
        onBack={() => setNotifications(false)}
      />
    );
  if (editing) {
    const zoneList = zones
      .filter((zone) =>
        `${zone.label} ${zone.id}`
          .toLocaleLowerCase("ru")
          .includes(zoneQuery.trim().toLocaleLowerCase("ru")),
      )
      .slice(0, 40);
    const zoneName =
      zones.find((zone) => zone.id === draft.timeZone)?.label ?? draft.timeZone;
    return (
      <Page title={creating ? "Новый монитор" : "Изменить монитор"}>
        <Card>
          <Label muted>
            {creating
              ? "Настройте проверку, затем запустите её. Оплата не создаёт монитор автоматически."
              : "Изменения применятся после сохранения."}
          </Label>
          <Title>Тип проверки</Title>
          <View style={{ flexDirection: "row", gap: 8, flexWrap: "wrap" }}>
            {(["http", "tcp", "icmp"] as const).map((type) => (
              <Button
                key={type}
                label={type.toUpperCase()}
                secondary={draft.checkType !== type}
                onPress={() => setDraft({ ...draft, checkType: type })}
              />
            ))}
          </View>
          <Label muted>
            {draft.checkType === "http"
              ? "HTTP проверяет страницу и код ответа. GET также умеет искать текст."
              : draft.checkType === "tcp"
                ? "TCP проверяет доступность порта сервиса."
                : "ICMP проверяет ответ на ping. Сервис может работать и при запрете ping."}
          </Label>
          <Field
            error={fieldError("name")}
            label="Название"
            value={draft.name}
            onChangeText={(name) => setDraft({ ...draft, name })}
          />
          {draft.checkType === "http" ? (
            <>
              <Field
                error={fieldError("url")}
                hint="Полный адрес, например https://example.ru/health"
                label="URL HTTPS/HTTP"
                value={draft.url}
                onChangeText={(url) => setDraft({ ...draft, url })}
                keyboardType="url"
              />
              <View style={{ flexDirection: "row", gap: 8 }}>
                <Button
                  label="GET"
                  secondary={draft.method !== "GET"}
                  onPress={() => setDraft({ ...draft, method: "GET" })}
                />
                <Button
                  label="HEAD"
                  secondary={draft.method !== "HEAD"}
                  onPress={() =>
                    setDraft({ ...draft, method: "HEAD", bodyContains: "" })
                  }
                />
              </View>
              <Field
                error={fieldError("expectedStatus")}
                hint="От 100 до 599"
                label="Ожидаемый HTTP-код"
                value={draft.expectedStatus}
                onChangeText={(expectedStatus) =>
                  setDraft({ ...draft, expectedStatus })
                }
                keyboardType="number-pad"
              />
              {draft.method === "GET" ? (
                <Field
                  error={fieldError("bodyContains")}
                  hint="До 1000 символов. Проверяется только для GET."
                  label="Текст в ответе (необязательно)"
                  value={draft.bodyContains}
                  onChangeText={(bodyContains) =>
                    setDraft({ ...draft, bodyContains })
                  }
                />
              ) : (
                <Label muted>
                  HEAD получает только заголовки, поиск текста выключен.
                </Label>
              )}
            </>
          ) : (
            <>
              <Field
                error={fieldError("host")}
                hint="Домен или IP без https:// и пути"
                label="Хост"
                value={draft.host}
                onChangeText={(host) => setDraft({ ...draft, host })}
              />
              {draft.checkType === "tcp" ? (
                <Field
                  error={fieldError("port")}
                  hint="От 1 до 65535"
                  label="Порт"
                  value={draft.port}
                  onChangeText={(port) => setDraft({ ...draft, port })}
                  keyboardType="number-pad"
                />
              ) : null}
            </>
          )}
          <Button
            label={
              advanced
                ? "Скрыть дополнительные настройки"
                : "Дополнительные настройки"
            }
            secondary
            onPress={() => setAdvanced(!advanced)}
          />
          {advanced ? (
            <>
              <Label>Часовой пояс отчётов: {zoneName}</Label>
              <Button
                label={
                  zonePicker
                    ? "Закрыть список часовых поясов"
                    : "Выбрать часовой пояс"
                }
                secondary
                onPress={() => setZonePicker(!zonePicker)}
              />
              {zonePicker ? (
                <Card>
                  <Field
                    label="Поиск города или часового пояса"
                    value={zoneQuery}
                    onChangeText={setZoneQuery}
                    placeholder="Москва, Berlin…"
                  />
                  {zoneList.map((zone) => (
                    <Button
                      key={zone.id}
                      label={`${zone.label} · ${zone.id}`}
                      secondary={zone.id !== draft.timeZone}
                      onPress={() => {
                        setDraft({ ...draft, timeZone: zone.id });
                        setZonePicker(false);
                      }}
                    />
                  ))}
                  {!zoneList.length ? (
                    <Label muted>Ничего не найдено. Измените запрос.</Label>
                  ) : null}
                </Card>
              ) : null}
              <Field
                error={fieldError("intervalMinutes")}
                hint="От 1 до 10 минут"
                label="Интервал, минут"
                value={draft.intervalMinutes}
                onChangeText={(intervalMinutes) =>
                  setDraft({ ...draft, intervalMinutes })
                }
                keyboardType="number-pad"
              />
              <Field
                error={fieldError("failureThreshold")}
                hint="От 1 до 10 неудачных проверок подряд"
                label="Подтверждение сбоя, проверок"
                value={draft.failureThreshold}
                onChangeText={(failureThreshold) =>
                  setDraft({ ...draft, failureThreshold })
                }
                keyboardType="number-pad"
              />
              <View
                style={{
                  flexDirection: "row",
                  alignItems: "center",
                  justifyContent: "space-between",
                }}
              >
                <Label>Монитор включён</Label>
                <Switch
                  value={draft.enabled}
                  onValueChange={(enabled) => setDraft({ ...draft, enabled })}
                />
              </View>
            </>
          ) : null}
          <ErrorNotice message={retained.error} />
          {creating &&
          entitlement &&
          (entitlement.quota === 0 ||
            (draft.enabled &&
              entitlement.enabledMonitorCount >= entitlement.quota)) ? (
            <Card>
              <Title>
                {draft.enabled
                  ? "Нужна квота для запуска"
                  : "Нужна квота для создания"}
              </Title>
              <Label>
                Настройки остаются в черновике. Купите место и вернитесь к этой
                форме.
              </Label>
              <Button
                label="Купить квоту"
                iconName="payment"
                onPress={() => void buyQuota()}
              />
            </Card>
          ) : null}
          {creating && !entitlement ? (
            <>
              <ErrorNotice
                message={
                  entitlementError || "Не удалось проверить доступную квоту."
                }
              />
              <Button
                label="Обновить квоту"
                secondary
                onPress={() => void refreshEntitlement()}
              />
            </>
          ) : null}
          <ErrorNotice message={error} />
          <Button
            label={
              creating
                ? draft.enabled
                  ? "Запустить монитор"
                  : "Сохранить на паузе"
                : "Сохранить изменения"
            }
            loading={busy}
            loadingLabel="Сохраняем…"
            disabled={busy}
            onPress={() => void save()}
          />
          <Button label="Отмена" secondary onPress={cancelEditor} />
        </Card>
      </Page>
    );
  }
  if (selected && selectedIncident)
    return (
      <Page
        key={`incident-${selectedIncident.id}`}
        motionKey={`incident-${selectedIncident.id}`}
        title={`Инцидент №${selectedIncident.number}`}
      >
        <Button
          label={
            incidentSource.current === "feed"
              ? "К ленте инцидентов"
              : "К монитору"
          }
          iconName="arrow-left"
          onPress={closeIncident}
          secondary
        />
        <Card>
          <Title>{selected.name}</Title>
          <Label>{selectedIncident.closedAt ? "Завершён" : "Активен"}</Label>
          <Label>{closeReasonName(selectedIncident.closeReason)}</Label>
          <Label>
            Открыт: {date(selectedIncident.openedAt, selected.timeZone)}
          </Label>
          <Label>
            Подтверждён: {date(selectedIncident.confirmedAt, selected.timeZone)}
          </Label>
          <Label>
            Завершён:{" "}
            {selectedIncident.closedAt
              ? date(selectedIncident.closedAt, selected.timeZone)
              : "Ещё не завершён"}
          </Label>
          <Label muted>Часовой пояс: {selected.timeZone}</Label>
        </Card>
        <ErrorNotice message={error} />
      </Page>
    );
  if (selected)
    return (
      <Page
        title={selected.name}
        motionKey={`monitor-${selected.id}`}
        refresh={() => detail(selected, true)}
      >
        <Button
          label="К списку"
          iconName="arrow-left"
          onPress={() => {
            detailRequest.current++;
            paginationRequest.current++;
            setPaginationBusy(false);
            detailPending.current = false;
            setDetailRefreshing(false);
            setSelected(null);
            setSelectedIncident(null);
          }}
          secondary
        />
        <Card>
          <View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
            <Icon
              name={statusIcon(selected.state)}
              color={selected.state === "DOWN" ? c.dangerText : c.accentText}
            />
            <Title>{monitorStatus(selected)}</Title>
          </View>
          <Label>
            {selected.checkType === "http"
              ? selected.url
              : selected.checkType === "tcp"
                ? `${selected.host}:${selected.port}`
                : selected.host}
          </Label>
          <Label>
            Проверка: каждые {selected.intervalMinutes} мин ·{" "}
            {selected.timeZone}
          </Label>
          <Label>
            Последняя проверка:{" "}
            {date(selected.lastCheckedAt, selected.timeZone)}
          </Label>
          <Label>
            Провайдер / CDN:{" "}
            {selected.provider.names.join(", ") || "Неизвестно"}
          </Label>
          <Button
            label="Изменить"
            iconName="settings"
            onPress={() => edit(selected)}
            secondary
          />
          <Button
            label="Уведомления"
            iconName="bell"
            onPress={() => setNotifications(true)}
            secondary
          />
          <Button
            label="Удалить"
            iconName="trash"
            danger
            disabled={busy}
            onPress={remove}
          />
        </Card>
        <MonitorStatistics
          monitorId={selected.id}
          refreshKey={statisticsRefresh}
        />
        <Button
          label={
            incidentsOpen
              ? "Свернуть инциденты"
              : `Инциденты · ${incidents.length}`
          }
          secondary
          onPress={() => setIncidentsOpen(!incidentsOpen)}
        />
        {(incidentsOpen
          ? incidents
          : incidents.filter((item) => !item.closedAt)
        ).map((item) => (
          <Card key={item.id}>
            <Label>
              №{item.number} · {date(item.openedAt, selected.timeZone)}
            </Label>
            <Label>
              {item.closedAt
                ? `Завершён ${date(item.closedAt, selected.timeZone)}`
                : "Активен"}
            </Label>
            <Button
              label="Подробнее"
              iconName="arrow-right"
              onPress={() => {
                incidentSource.current = "monitor";
                setSelectedIncident(item);
              }}
              secondary
            />
          </Card>
        ))}
        {!incidents.length ? <Label muted>Инцидентов пока нет.</Label> : null}
        <Button
          label={
            historyOpen
              ? "Свернуть историю проверок"
              : `История проверок · ${results.length}`
          }
          secondary
          onPress={() => setHistoryOpen(!historyOpen)}
        />
        {(historyOpen ? results : results.slice(0, 3)).map((item) => (
          <Card key={item.id} style={{ padding: 10, gap: 4 }}>
            <View
              style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
            >
              <Icon
                name={statusIcon(item.outcome)}
                color={item.outcome === "DOWN" ? c.dangerText : c.accentText}
              />
              <Label>
                {resultNames[item.outcome] ?? item.outcome} ·{" "}
                {date(item.checkedAt, selected.timeZone)}
              </Label>
            </View>
            <Label>
              {item.errorCode
                ? (checkFailureNames[item.errorCode] ??
                  "Проверка не завершилась успешно")
                : "Ответ получен"}
              {item.httpStatus ? ` · HTTP ${item.httpStatus}` : ""} ·{" "}
              {typeof item.roundTripMs === "number"
                ? `RTT ${item.roundTripMs} мс`
                : `${item.latencyMs} мс`}
            </Label>
          </Card>
        ))}
        {historyOpen && moreResults ? (
          <Button
            label="Более ранние результаты"
            iconName="arrow-down"
            secondary
            disabled={paginationBusy || detailRefreshing}
            onPress={() => void olderResults()}
          />
        ) : null}
        {results.length > firstResults.current.length ? (
          <Button
            label="Свернуть историю"
            iconName="close"
            secondary
            onPress={collapseResults}
          />
        ) : null}
        {!results.length ? (
          <Label muted>
            Результатов ещё нет. Они появятся после первой проверки.
          </Label>
        ) : null}
        <ErrorNotice message={error} />
      </Page>
    );
  return (
    <Page
      title="Мониторы"
      refresh={async () => {
        await Promise.all([loadList(), refreshEntitlement()]);
      }}
    >
      <Card>
        <Title>Состояние всех мониторов</Title>
        <Label>
          Всего {list?.summary.total ?? "—"} · доступны{" "}
          {list?.summary.up ?? "—"} · сбои {list?.summary.down ?? "—"}
        </Label>
        <Label muted>
          Ожидают {list?.summary.unknown ?? "—"} · пауза{" "}
          {list?.summary.paused ?? "—"} · нужна квота{" "}
          {list?.summary.subscriptionRequired ?? "—"}
        </Label>
        <ErrorNotice message={entitlementError} />
        <Label>
          Квота: {entitlement?.enabledMonitorCount ?? "—"} включено из{" "}
          {entitlement?.quota ?? "—"}
        </Label>
        {entitlement && entitlement.enabledMonitorCount >= entitlement.quota ? (
          <Button
            label="Добавить квоту"
            iconName="payment"
            secondary
            onPress={onBuyQuota}
          />
        ) : null}
      </Card>
      <Button
        label="Лента инцидентов"
        iconName="alert"
        secondary
        onPress={() => setFeed(true)}
      />
      <Button label="Добавить монитор" iconName="plus" onPress={() => edit()} />
      {list?.asOf ? (
        <Label muted>
          Обновлено {date(list.asOf, "Europe/Moscow")}
          {stale || clock - Date.parse(list.asOf) > 120000
            ? " · данные могут устареть; потяните вниз для повтора"
            : ""}
        </Label>
      ) : null}
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator
        style={{ flexGrow: 0 }}
        contentContainerStyle={{ gap: 8, paddingVertical: 3 }}
      >
        {[
          ["all", "Все"],
          ["problems", "Проблемы"],
          ...Object.entries(statusNames),
        ].map(([key, label]) => (
          <Button
            key={key}
            label={label}
            selected={status === key}
            secondary={status !== key}
            onPress={() => {
              setStatus(key);
              setPage(1);
            }}
          />
        ))}
      </ScrollView>
      <Field
        label="Поиск мониторов"
        value={filter}
        onChangeText={changeFilter}
        placeholder="Название или адрес"
      />
      <View
        style={{
          flexDirection: "row",
          alignItems: "stretch",
          gap: 8,
        }}
      >
        <Button
          label="Проблемы"
          accessibilityLabel="Сначала проблемы"
          selected={sort === "problems_first"}
          style={{ flex: 1, minWidth: 0, paddingHorizontal: 6 }}
          secondary={sort !== "problems_first"}
          onPress={() => {
            setSort("problems_first");
            setPage(1);
          }}
        />
        <Button
          label={
            sort.startsWith("name")
              ? `По имени ${sort.endsWith("asc") ? "↑" : "↓"}`
              : "По имени"
          }
          secondary={!sort.startsWith("name")}
          selected={sort.startsWith("name")}
          style={{ flex: 1, minWidth: 0, paddingHorizontal: 6 }}
          onPress={() => {
            setPage(1);
            setSort(
              sort.startsWith("name")
                ? sort === "name_asc"
                  ? "name_desc"
                  : "name_asc"
                : "name_asc",
            );
          }}
        />
        <Button
          label={
            sort.startsWith("created")
              ? `По дате ${sort.endsWith("asc") ? "↑" : "↓"}`
              : "По дате"
          }
          secondary={!sort.startsWith("created")}
          selected={sort.startsWith("created")}
          style={{ flex: 1, minWidth: 0, paddingHorizontal: 6 }}
          onPress={() => {
            setPage(1);
            setSort(
              sort.startsWith("created")
                ? sort === "created_desc"
                  ? "created_asc"
                  : "created_desc"
                : "created_desc",
            );
          }}
        />
      </View>
      <View
        style={{
          flexDirection: "row",
          alignItems: "center",
          gap: 8,
          minHeight: 24,
        }}
      >
        <Label muted>
          Найдено: {list?.pagination.total ?? "—"} · страница{" "}
          {list?.pagination.page ?? page} из {list?.pagination.totalPages ?? 1}
        </Label>
        <View style={{ width: 20, alignItems: "center" }}>
          {listLoading ? (
            <ActivityIndicator size="small" color={c.accent} />
          ) : null}
        </View>
      </View>
      {list?.monitors.map((monitor) => (
        <Pressable
          key={monitor.id}
          accessibilityRole="button"
          accessibilityLabel={`${monitor.name}. ${statusNames[monitor.state] ?? "Состояние неизвестно"}`}
          onPress={() => void detail(monitor)}
        >
          <Card style={{ padding: 14, gap: 6 }}>
            <Title>{monitor.name}</Title>
            <View
              style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
            >
              <Icon
                name={statusIcon(monitor.state)}
                color={monitor.state === "DOWN" ? c.dangerText : c.accentText}
              />
              <Label>{monitorStatus(monitor)}</Label>
            </View>
            <Label>
              {monitor.checkType === "http"
                ? monitor.url
                : monitor.checkType === "tcp"
                  ? `${monitor.host}:${monitor.port}`
                  : monitor.host}
            </Label>
            {monitor.state === "DOWN" ? (
              <Label muted>Есть проблема · откройте монитор для деталей</Label>
            ) : null}
          </Card>
        </Pressable>
      ))}
      {list && !list.monitors.length ? (
        <Card>
          <Title>
            {list.summary.total > 0
              ? "Ничего не найдено"
              : "Добавьте первый монитор"}
          </Title>
          <Label muted>
            {list.summary.total > 0
              ? "Измените поиск или выберите другой статус."
              : "Настройте адрес проверки. При необходимости купите квоту, затем явно запустите монитор."}
          </Label>
          {list.summary.total > 0 ? (
            <Button
              label="Сбросить фильтры"
              secondary
              onPress={() => {
                changeFilter("");
                setStatus("all");
                setPage(1);
              }}
            />
          ) : null}
        </Card>
      ) : null}
      <View
        style={{
          flexDirection: "row",
          justifyContent: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        <Button
          label="Назад"
          iconName="arrow-left"
          secondary
          disabled={page <= 1 || listLoading}
          onPress={() => setPage(page - 1)}
        />
        <Button
          label="Далее"
          iconName="arrow-right"
          secondary
          disabled={!list || page >= list.pagination.totalPages || listLoading}
          onPress={() => setPage(page + 1)}
        />
      </View>
      <ErrorNotice message={error} />
    </Page>
  );
}
