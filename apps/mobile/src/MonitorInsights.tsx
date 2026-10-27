import React, { useCallback, useEffect, useRef, useState } from "react";
import { AppState, Pressable, View } from "react-native";
import { api, errorText } from "./api";
import type { Incident, Monitor } from "./types";
import {
  Button,
  Card,
  ErrorNotice,
  Label,
  Page,
  Title,
  usePalette,
} from "./ui";

type Counts = {
  up: number;
  down: number;
  unknown: number;
  total: number;
  successfulCheckPercent: number | null;
  averageLatencyMs: number | null;
  latencySampleCount: number;
};
type Statistics = {
  monitorId: string;
  periodDays: number;
  from: string;
  to: string;
  generatedAt: string;
  timeZone: "UTC";
  coverage: {
    firstRecordedDay: string | null;
    daysWithData: number;
    scheduleCompleteness: "unknown";
  };
  totals: Counts;
  days: (Counts & { date: string; partial: boolean })[];
};
const percent = (value: number | null) =>
  value === null
    ? "Нет данных"
    : `${value.toLocaleString("ru-RU", { maximumFractionDigits: 2 })}%`;
export const closeReasonName = (value?: string | null) =>
  ({
    recovered: "Сервис восстановился",
    paused: "Монитор поставлен на паузу",
    config_changed: "Настройки монитора изменились",
    recovery: "Сервис восстановился",
    monitor_paused: "Монитор поставлен на паузу",
    monitor_disabled: "Монитор выключен",
    subscription_expired: "Срок оплаты завершён",
    subscription_required: "Недостаточно квоты",
    monitor_updated: "Настройки монитора изменились",
    monitor_deleted: "Монитор удалён",
  })[value ?? ""] ??
  (value ? "Инцидент завершён системой" : "Инцидент активен");
export function MonitorStatistics({
  monitorId,
  refreshKey,
}: {
  monitorId: string;
  refreshKey: number;
}) {
  const [days, setDays] = useState<7 | 30 | 90>(7);
  const [data, setData] = useState<Statistics | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState(false);
  const [methodOpen, setMethodOpen] = useState(false);
  const c = usePalette();
  useEffect(() => {
    let active = true;
    setBusy(true);
    setError("");
    setData(null);
    void api<{ statistics: Statistics }>(
      `/monitors/${monitorId}/statistics?days=${days}`,
    )
      .then((value) => {
        if (active) setData(value.statistics);
      })
      .catch((cause) => {
        if (active) setError(errorText(cause));
      })
      .finally(() => {
        if (active) setBusy(false);
      });
    return () => {
      active = false;
    };
  }, [monitorId, days, refreshKey]);
  return (
    <Card>
      <Title>Статистика проверок</Title>
      <View style={{ flexDirection: "row", gap: 8, flexWrap: "wrap" }}>
        {([7, 30, 90] as const).map((value) => (
          <Button
            key={value}
            label={`${value} дней`}
            secondary={days !== value}
            onPress={() => setDays(value)}
          />
        ))}
      </View>
      <Label>Доля успешных проверок · дни UTC</Label>
      <Button
        label={methodOpen ? "Скрыть расчёт" : "Как рассчитывается"}
        secondary
        onPress={() => setMethodOpen(!methodOpen)}
      />
      {methodOpen ? (
        <Label muted>
          Успешные проверки делятся на сумму успешных и неудачных. Результаты
          без определённого статуса учитываются отдельно. Это доля записанных
          проверок, а не время доступности или SLA. Пропуски расписания
          неизвестны. Дни и границы периода — UTC; текущий день частичный.
        </Label>
      ) : null}
      {busy ? <Label muted>Загружаем статистику…</Label> : null}
      {data ? (
        <>
          <Title>{percent(data.totals.successfulCheckPercent)}</Title>
          <Label>
            Успешно {data.totals.up} · сбои {data.totals.down} · без результата{" "}
            {data.totals.unknown}
          </Label>
          <Label>
            Средняя длительность:{" "}
            {data.totals.averageLatencyMs === null
              ? "Нет данных"
              : `${Math.round(data.totals.averageLatencyMs)} мс`}{" "}
            · измерений {data.totals.latencySampleCount}
          </Label>
          <Label muted>
            Дней с данными: {data.coverage.daysWithData} из {data.periodDays}.
            Частичный текущий день отмечен *. Обновлено{" "}
            {new Date(data.generatedAt).toLocaleString("ru-RU")}.
          </Label>
          <Button
            label={expanded ? "Свернуть дни" : "Результаты по дням"}
            secondary
            onPress={() => setExpanded(!expanded)}
          />
          {expanded
            ? data.days.map((day) => (
                <View key={day.date} style={{ gap: 4 }}>
                  <Label>
                    {day.date}
                    {day.partial ? " *" : ""} ·{" "}
                    {percent(day.successfulCheckPercent)}
                  </Label>
                  <View
                    accessible
                    accessibilityLabel={`Успешно ${day.up}, сбои ${day.down}, без результата ${day.unknown}`}
                    style={{
                      height: 7,
                      flexDirection: "row",
                      backgroundColor: c.border,
                      borderRadius: 4,
                      overflow: "hidden",
                    }}
                  >
                    {day.total > 0 ? (
                      <>
                        <View
                          style={{ flex: day.up, backgroundColor: c.accent }}
                        />
                        <View
                          style={{ flex: day.down, backgroundColor: c.danger }}
                        />
                        <View
                          style={{
                            flex: day.unknown,
                            backgroundColor: c.subtle,
                          }}
                        />
                      </>
                    ) : null}
                  </View>
                  <Label muted>
                    Успешно {day.up} · сбои {day.down} · без результата{" "}
                    {day.unknown}
                  </Label>
                </View>
              ))
            : null}
        </>
      ) : null}
      <ErrorNotice message={error} />
    </Card>
  );
}
type FeedIncident = Incident & {
  monitorId: string;
  monitor: {
    id: string;
    name: string;
    checkType: Monitor["checkType"];
    target: string;
    timeZone: string;
  };
};
type FeedPage = {
  incidents: FeedIncident[];
  pagination: { nextCursor: string | null; hasMore: boolean };
  asOf: string;
};
export type IncidentFeedState = "all" | "active" | "closed";
export function IncidentFeed({
  onBack,
  onOpen,
  initialState = "active",
  onStateChange,
}: {
  onBack: () => void;
  onOpen: (monitorId: string, incidentId: string) => void;
  initialState?: IncidentFeedState;
  onStateChange?: (state: IncidentFeedState) => void;
}) {
  const [state, setState] = useState<IncidentFeedState>(initialState);
  const [data, setData] = useState<FeedPage | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const load = useCallback(
    async (cursor?: string) => {
      const request = ++generation.current;
      setBusy(true);
      setError("");
      try {
        const value = await api<FeedPage>(
          `/incidents?state=${state}&limit=25${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
        );
        if (request !== generation.current) return;
        setData((previous) =>
          cursor && previous
            ? {
                ...value,
                incidents: [
                  ...previous.incidents,
                  ...value.incidents.filter(
                    (item) =>
                      !previous.incidents.some((old) => old.id === item.id),
                  ),
                ],
              }
            : value,
        );
      } catch (cause) {
        if (request === generation.current) setError(errorText(cause));
      } finally {
        if (request === generation.current) setBusy(false);
      }
    },
    [state],
  );
  useEffect(() => {
    setData(null);
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);
  useEffect(() => {
    const sub = AppState.addEventListener("change", (next) => {
      if (next === "active") void load();
    });
    return () => sub.remove();
  }, [load]);
  return (
    <Page title="Инциденты" refresh={() => load()}>
      <Button
        label="К мониторам"
        secondary
        iconName="arrow-left"
        onPress={onBack}
      />
      <View style={{ flexDirection: "row", gap: 6, flexWrap: "wrap" }}>
        {(
          [
            ["active", "Активные"],
            ["closed", "Завершённые"],
            ["all", "Все"],
          ] as const
        ).map(([key, label]) => (
          <Button
            key={key}
            label={label}
            secondary={state !== key}
            onPress={() => {
              setState(key);
              onStateChange?.(key);
            }}
          />
        ))}
      </View>
      {data ? (
        <Label muted>
          Обновлено {new Date(data.asOf).toLocaleString("ru-RU")}
          {error ? " · сохранённые данные" : ""}
        </Label>
      ) : null}
      <ErrorNotice message={error} />
      {data?.incidents.map((item) => (
        <Pressable
          key={item.id}
          accessibilityRole="button"
          accessibilityLabel={`Инцидент ${item.number}, ${item.monitor.name}`}
          onPress={() => onOpen(item.monitorId, item.id)}
        >
          <Card>
            <Title>{item.monitor.name}</Title>
            <Label>
              №{item.number} ·{" "}
              {item.closedAt ? closeReasonName(item.closeReason) : "Активен"}
            </Label>
            <Label muted>
              {new Date(item.openedAt).toLocaleString("ru-RU")} ·{" "}
              {item.monitor.target}
            </Label>
          </Card>
        </Pressable>
      ))}
      {data && !data.incidents.length ? (
        <Card>
          <Title>
            {state === "active"
              ? "Активных инцидентов нет"
              : "Инцидентов пока нет"}
          </Title>
          <Label muted>
            Здесь появятся подтверждённые сбои ваших мониторов.
          </Label>
        </Card>
      ) : null}
      {busy ? <Label muted>Обновляем…</Label> : null}
      {data?.pagination.hasMore ? (
        <Button
          label="Показать ещё"
          secondary
          disabled={busy}
          onPress={() => void load(data.pagination.nextCursor ?? undefined)}
        />
      ) : null}
    </Page>
  );
}
