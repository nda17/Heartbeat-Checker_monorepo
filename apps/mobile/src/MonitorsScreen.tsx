import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  Alert,
  BackHandler,
  Switch,
  View,
} from "react-native";
import { api, errorText, session } from "./api";
import type { Incident, Monitor, Result } from "./types";
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

type Draft = {
  checkType: "http" | "tcp" | "icmp";
  name: string;
  url: string;
  host: string;
  port: string;
  method: "GET" | "HEAD";
  expectedStatus: string;
  bodyContains: string;
  intervalMinutes: string;
  failureThreshold: string;
  timeZone: string;
  enabled: boolean;
};
type Zone = { id: string; label: string };
type Sort = "created_desc" | "created_asc" | "name_asc" | "name_desc";
type MonitorPage = {
  monitors: Monitor[];
  pagination: {
    page: number;
    pageSize: number;
    total: number;
    totalPages: number;
  };
  summary: { total: number; up: number; down: number };
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
}: {
  deepLink?: { monitorId: string; incidentId: string } | null;
}) {
  const [list, setList] = useState<MonitorPage | null>(null);
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<Sort>("created_desc");
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
  const selection = useRef({ page, sort, query });
  selection.current = { page, sort, query };
  selectedRef.current = selected;
  resultsRef.current = results;

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
      });
      const data = await api<MonitorPage>(`/monitors?${params.toString()}`, {
        signal: controller.signal,
      });
      if (
        request !== listRequest.current ||
        selection.current.page !== current.page ||
        selection.current.sort !== current.sort ||
        selection.current.query !== current.query
      )
        return;
      setList(data);
      if (data.pagination.page !== current.page) setPage(data.pagination.page);
    } catch (cause) {
      if (!controller.signal.aborted && request === listRequest.current)
        setError(errorText(cause));
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
  }, [page, sort, query, loadList]);
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

  const detail = useCallback(async (monitor: Monitor) => {
    editorBaseline.current = null;
    const request = ++detailRequest.current;
    paginationRequest.current++;
    setPaginationBusy(false);
    detailPending.current = true;
    setDetailRefreshing(true);
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
    setEditing(false);
    setNotifications(false);
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
      firstResults.current = r.results;
      resultsRef.current = r.results;
      setResults(r.results);
      setMoreResults(r.results.length >= 50);
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
    if (!deepLink) return;
    let active = true;
    const request = ++detailRequest.current;
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
      api<{ monitor: Monitor }>(`/monitors/${deepLink.monitorId}`),
      api<{ incident: Incident }>(
        `/monitors/${deepLink.monitorId}/incidents/${deepLink.incidentId}`,
      ),
    ])
      .then(([monitor, incident]) => {
        if (
          !active ||
          request !== detailRequest.current ||
          incident.incident.id !== deepLink.incidentId
        )
          return;
        setSelected(monitor.monitor);
        setSelectedIncident(incident.incident);
        const loading = detail(monitor.monitor);
        const detailGeneration = detailRequest.current;
        void loading.then(() => {
          if (active && detailGeneration === detailRequest.current)
            setSelectedIncident(incident.incident);
        });
      })
      .catch((cause) => {
        if (active && request === detailRequest.current)
          setError(`Ссылка недоступна этому аккаунту. ${errorText(cause)}`);
      });
    return () => {
      active = false;
      detailRequest.current++;
    };
  }, [deepLink?.monitorId, deepLink?.incidentId, detail]);

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
    setZonePicker(false);
    setZoneQuery("");
    setError("");
    setEditing(true);
    setCreating(!monitor);
  };
  const save = async () => {
    if (mutation.current) return;
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
        bodyContains: draft.checkType === "http" ? draft.bodyContains : "",
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
  useEffect(() => {
    const listener = BackHandler.addEventListener("hardwareBackPress", () => {
      if (selectedIncident) {
        setSelectedIncident(null);
        return true;
      }
      if (notifications) {
        setNotifications(false);
        return true;
      }
      if (editing) {
        editorBaseline.current = null;
        setEditing(false);
        setCreating(false);
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
  }, [selectedIncident, notifications, editing, selected]);

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
          <Field
            label="Название"
            value={draft.name}
            onChangeText={(name) => setDraft({ ...draft, name })}
          />
          {draft.checkType === "http" ? (
            <>
              <Field
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
                  onPress={() => setDraft({ ...draft, method: "HEAD" })}
                />
              </View>
              <Field
                label="Ожидаемый HTTP-код"
                value={draft.expectedStatus}
                onChangeText={(expectedStatus) =>
                  setDraft({ ...draft, expectedStatus })
                }
                keyboardType="number-pad"
              />
              <Field
                label="Текст в ответе (необязательно)"
                value={draft.bodyContains}
                onChangeText={(bodyContains) =>
                  setDraft({ ...draft, bodyContains })
                }
              />
            </>
          ) : (
            <>
              <Field
                label="Хост"
                value={draft.host}
                onChangeText={(host) => setDraft({ ...draft, host })}
              />
              {draft.checkType === "tcp" ? (
                <Field
                  label="Порт"
                  value={draft.port}
                  onChangeText={(port) => setDraft({ ...draft, port })}
                  keyboardType="number-pad"
                />
              ) : null}
            </>
          )}
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
            label="Интервал, минут"
            value={draft.intervalMinutes}
            onChangeText={(intervalMinutes) =>
              setDraft({ ...draft, intervalMinutes })
            }
            keyboardType="number-pad"
          />
          <Field
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
          <ErrorNotice message={error} />
          <Button
            label="Сохранить"
            disabled={busy}
            onPress={() => void save()}
          />
          <Button
            label="Отмена"
            secondary
            onPress={() => {
              editorBaseline.current = null;
              setEditing(false);
              setCreating(false);
            }}
          />
        </Card>
      </Page>
    );
  }
  if (selected)
    return (
      <Page title={selected.name} refresh={() => detail(selected)}>
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
            <Title>{statusNames[selected.state] ?? selected.state}</Title>
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
        {selectedIncident ? (
          <Card>
            <Title>Инцидент №{selectedIncident.number}</Title>
            <Label>
              Открыт: {date(selectedIncident.openedAt, selected.timeZone)}
            </Label>
            <Label>
              Подтверждён:{" "}
              {date(selectedIncident.confirmedAt, selected.timeZone)}
            </Label>
            <Label>
              Завершён: {date(selectedIncident.closedAt, selected.timeZone)}
            </Label>
            <Button
              label="Закрыть"
              iconName="close"
              onPress={() => setSelectedIncident(null)}
              secondary
            />
          </Card>
        ) : null}
        <Title>Инциденты</Title>
        {incidents.map((item) => (
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
              onPress={() => setSelectedIncident(item)}
              secondary
            />
          </Card>
        ))}
        {!incidents.length ? <Label muted>Инцидентов пока нет.</Label> : null}
        <Title>История проверок</Title>
        {results.map((item) => (
          <Card key={item.id}>
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
              {item.errorCode ||
                (item.httpStatus ? `HTTP ${item.httpStatus}` : "Ответ")}{" "}
              · {item.latencyMs} мс
            </Label>
          </Card>
        ))}
        {moreResults ? (
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
    <Page title="Мониторы" refresh={loadList}>
      <Button label="Добавить монитор" iconName="plus" onPress={() => edit()} />
      <Field
        label="Поиск мониторов"
        value={filter}
        onChangeText={changeFilter}
        placeholder="Название или адрес"
      />
      <View
        style={{
          flexDirection: "row",
          justifyContent: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        <Button
          label={
            sort.startsWith("name")
              ? `По имени ${sort.endsWith("asc") ? "↑" : "↓"}`
              : "По имени"
          }
          secondary={!sort.startsWith("name")}
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
        <Card key={monitor.id}>
          <Title>{monitor.name}</Title>
          <View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
            <Icon
              name={statusIcon(monitor.state)}
              color={monitor.state === "DOWN" ? c.dangerText : c.accentText}
            />
            <Label>{statusNames[monitor.state] ?? monitor.state}</Label>
          </View>
          <Label>
            {monitor.checkType === "http"
              ? monitor.url
              : monitor.checkType === "tcp"
                ? `${monitor.host}:${monitor.port}`
                : monitor.host}
          </Label>
          <Button
            label="Открыть"
            iconName="arrow-right"
            onPress={() => void detail(monitor)}
            secondary
          />
        </Card>
      ))}
      {list && !list.monitors.length ? (
        <Label muted>По этому запросу мониторов нет.</Label>
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
