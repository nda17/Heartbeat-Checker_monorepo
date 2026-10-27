import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  AppState,
  Animated,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";
import { api } from "./api";
import { FAQ, SupportLinks } from "./HelpScreen";
import {
  BrandHeader,
  Button,
  Card,
  DetailRow,
  GlassBackdrop,
  Icon,
  IconName,
  Label,
  Notice,
  TextAction,
  Title,
  usePalette,
  useEntryMotion,
} from "./ui";

type Pricing = {
  pricePerMonitorMinor: number;
  currency: "RUB";
  period: "month";
  version: number;
  updatedAt: string;
};
function isPricing(value: unknown): value is Pricing {
  if (!value || typeof value !== "object") return false;
  const p = value as Partial<Pricing>;
  return (
    Number.isSafeInteger(p.pricePerMonitorMinor) &&
    p.pricePerMonitorMinor! >= 100 &&
    p.pricePerMonitorMinor! <= 1_000_000 &&
    Number.isSafeInteger(p.version) &&
    p.version! > 0 &&
    p.currency === "RUB" &&
    p.period === "month" &&
    typeof p.updatedAt === "string" &&
    Number.isFinite(Date.parse(p.updatedAt))
  );
}
function money(minor: number) {
  return new Intl.NumberFormat("ru-RU", {
    style: "currency",
    currency: "RUB",
    minimumFractionDigits: minor % 100 ? 2 : 0,
    maximumFractionDigits: 2,
  }).format(minor / 100);
}

function PublicPrice({ onRegister }: { onRegister: () => void }) {
  const c = usePalette();
  const [pricing, setPricing] = useState<Pricing | null>(null);
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  const [checkedAt, setCheckedAt] = useState<number | null>(null);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const [now, setNow] = useState(Date.now());
  const lastSuccess = useRef<number | null>(null);
  const refreshAfter = 5 * 60_000;
  useEffect(() => {
    let alive = true;
    let current: AbortController | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = async (force = false) => {
      if (
        !alive ||
        current ||
        AppState.currentState === "background" ||
        AppState.currentState === "inactive"
      )
        return;
      const startedAt = Date.now();
      setNow(startedAt);
      if (
        !force &&
        lastSuccess.current !== null &&
        startedAt - lastSuccess.current < refreshAfter
      )
        return;
      const controller = new AbortController();
      current = controller;
      setLoading(true);
      const timeout = setTimeout(() => {
        if (!alive || current !== controller) return;
        current = null;
        controller.abort();
        setRefreshFailed(true);
        setLoading(false);
      }, 5000);
      timer = timeout;
      try {
        const data = await api<{ pricing: unknown }>("/billing/pricing", {
          auth: false,
          signal: controller.signal,
        });
        if (alive && current === controller && !controller.signal.aborted) {
          if (!isPricing(data.pricing))
            throw new Error("Invalid public pricing");
          const fetchedAt = Date.now();
          lastSuccess.current = fetchedAt;
          setPricing(data.pricing);
          setCheckedAt(fetchedAt);
          setNow(fetchedAt);
          setRefreshFailed(false);
        }
      } catch {
        if (alive && current === controller) setRefreshFailed(true);
      } finally {
        clearTimeout(timeout);
        if (alive && current === controller) {
          current = null;
          setLoading(false);
        }
      }
    };
    void refresh(attempt > 0);
    const app = AppState.addEventListener("change", (state) => {
      if (state === "active") void refresh();
      else {
        current?.abort();
        current = null;
        clearTimeout(timer);
        setLoading(false);
      }
    });
    const interval = setInterval(() => void refresh(), 60_000);
    return () => {
      alive = false;
      current?.abort();
      clearTimeout(timer);
      clearInterval(interval);
      app.remove();
    };
  }, [attempt]);
  const stale =
    refreshFailed || (checkedAt !== null && now - checkedAt >= refreshAfter);
  return (
    <Card>
      <Eyebrow>ПОНЯТНАЯ ЕДИНИЦА СТОИМОСТИ</Eyebrow>
      <Title>Платите за нужные адреса</Title>
      <Label muted>
        Один монитор — одна цель. Все проверки по расписанию входят в стоимость.
      </Label>
      {pricing ? (
        <>
          <Text style={[styles.price, { color: c.text }]}>
            {money(pricing.pricePerMonitorMinor)}
          </Text>
          <Label muted>за один монитор за календарный месяц</Label>
          <DetailRow
            label="Главная + вход + API · 3 монитора"
            value={`${money(3 * pricing.pricePerMonitorMinor)} / мес.`}
          />
          <DetailRow
            label="10 мониторов"
            value={`${money(10 * pricing.pricePerMonitorMinor)} / мес.`}
          />
          <Label muted>
            {stale
              ? "Последняя полученная цена. Актуальность пока не подтверждена."
              : "Текущая публичная цена."}{" "}
            Итоговая сумма проверяется при оформлении заказа.
          </Label>
          {stale ? (
            <>
              <Notice
                message={
                  loading
                    ? "Проверяем актуальность цены…"
                    : "Не удалось подтвердить актуальную цену. Обновите её, когда появится сеть."
                }
              />
              <Button
                label="Обновить цену"
                onPress={() => setAttempt((value) => value + 1)}
                loading={loading}
                loadingLabel="Проверяем цену…"
                secondary
                iconName="refresh"
              />
            </>
          ) : null}
        </>
      ) : (
        <>
          <Notice
            message={
              loading
                ? "Загружаем текущую цену…"
                : "Цена временно недоступна. Обновите её, когда появится сеть."
            }
          />
          <Button
            label="Обновить цену"
            onPress={() => setAttempt((value) => value + 1)}
            loading={loading}
            loadingLabel="Загружаем цену…"
            secondary
            iconName="refresh"
          />
        </>
      )}
      <Label>
        Покупайте отдельные пакеты нужного размера. У каждого свой срок,
        действующие квоты складываются. Продление оформляется вручную.
      </Label>
      <Notice message="Без автоматических списаний и пробного периода. Окончательная сумма в рублях видна до подтверждения оплаты." />
      <Button
        label="Создать аккаунт"
        onPress={onRegister}
        iconName="arrow-right"
      />
    </Card>
  );
}

function Eyebrow({ children }: { children: React.ReactNode }) {
  const c = usePalette();
  return (
    <Text style={[styles.eyebrow, { color: c.accentText }]}>{children}</Text>
  );
}

function Feature({
  icon,
  title,
  children,
  example,
}: {
  icon: IconName;
  title: string;
  children: React.ReactNode;
  example: string;
}) {
  const c = usePalette();
  return (
    <Card>
      <View style={styles.featureHeading}>
        <View style={[styles.iconTile, { backgroundColor: c.bg }]}>
          <Icon name={icon} />
        </View>
        <Text
          accessibilityRole="header"
          style={[styles.featureTitle, { color: c.text }]}
        >
          {title}
        </Text>
      </View>
      <Label muted>{children}</Label>
      <Text
        selectable
        style={[styles.example, { color: c.accentText, backgroundColor: c.bg }]}
      >
        {example}
      </Text>
    </Card>
  );
}

const demoDays = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"];
const demoMonitors = [
  {
    name: "Главная страница",
    target: "example.com · HTTP 200",
    state: "UP",
    detail: "184 мс",
    down: [0, 0, 0, 0, 0, 0, 0],
  },
  {
    name: "API магазина",
    target: "api.example.com · HTTP 503",
    state: "DOWN",
    detail: "2 ошибки подряд",
    down: [0, 0, 6, 0, 0, 0, 2],
  },
  {
    name: "SSH сервера",
    target: "server.example.com:22 · TCP",
    state: "UP",
    detail: "42 мс",
    down: [0, 0, 0, 0, 0, 0, 0],
  },
];
const checksPerDay = 288;
function successfulChecks(down: number, total: number) {
  return (((total - down) / total) * 100).toLocaleString("ru-RU", {
    maximumFractionDigits: 1,
  });
}

function Demo() {
  const c = usePalette();
  const [selectedMonitor, setSelectedMonitor] = useState(1);
  const progress = useEntryMotion(selectedMonitor, false);
  const revealStyle = {
    opacity: progress.interpolate({
      inputRange: [0, 1],
      outputRange: [0.72, 1],
    }),
    transform: [
      {
        translateY: progress.interpolate({
          inputRange: [0, 1],
          outputRange: [3, 0],
        }),
      },
    ],
  };
  const selected = demoMonitors[selectedMonitor];
  const total = checksPerDay * demoDays.length;
  const failed = selected.down.reduce((sum, count) => sum + count, 0);
  return (
    <Card>
      <Eyebrow>ПРИМЕР ИНТЕРФЕЙСА</Eyebrow>
      <Title>Все важные цели перед глазами</Title>
      <Label muted>
        Выберите монитор, чтобы посмотреть успешные проверки по дням.
      </Label>
      {demoMonitors.map((monitor, index) => {
        const down = monitor.down.reduce((sum, count) => sum + count, 0);
        const success = successfulChecks(down, total);
        return (
          <Pressable
            key={monitor.name}
            accessibilityRole="button"
            accessibilityState={{ selected: index === selectedMonitor }}
            accessibilityLabel={`${monitor.name}. Успешные проверки за 7 дней: ${success}%. ${monitor.state}.`}
            onPress={() => setSelectedMonitor(index)}
            style={({ pressed }) => [
              styles.demoRow,
              {
                borderColor:
                  index === selectedMonitor ? c.accentText : c.border,
                backgroundColor: index === selectedMonitor ? c.bg : c.card,
                opacity: pressed ? 0.76 : 1,
              },
            ]}
          >
            <View style={styles.demoCopy}>
              <Text style={[styles.demoName, { color: c.text }]}>
                {monitor.name}
              </Text>
              <Text style={[styles.small, { color: c.subtle }]}>
                {monitor.target}
              </Text>
            </View>
            <View style={styles.demoState}>
              <Text
                style={[
                  styles.status,
                  {
                    color:
                      monitor.state === "DOWN" ? c.dangerText : c.accentText,
                  },
                ]}
              >
                {monitor.state}
              </Text>
              <Text style={[styles.small, { color: c.subtle }]}>
                {monitor.detail}
              </Text>
              <Text style={[styles.small, { color: c.accentText }]}>
                {success}% за 7 дней
              </Text>
            </View>
            <View style={[styles.monitorTrack, { backgroundColor: c.border }]}>
              <View style={{ flex: total - down, backgroundColor: c.accent }} />
              {down > 0 ? (
                <View
                  style={{
                    flex: down,
                    backgroundColor: c.dangerText,
                  }}
                />
              ) : null}
            </View>
          </Pressable>
        );
      })}
      <Animated.View
        style={[styles.analyticsSummary, revealStyle]}
        accessibilityLiveRegion="polite"
      >
        <Text style={[styles.demoName, { color: c.text }]}>
          {selected.name} · 7 дней
        </Text>
        <Text style={[styles.analyticsValue, { color: c.accentText }]}>
          {successfulChecks(failed, total)}%
        </Text>
        <Label>Успешные проверки</Label>
        <Label muted>
          {total - failed} из {total} проверок успешны
        </Label>
      </Animated.View>
      <Animated.View style={[styles.dailyChart, revealStyle]}>
        {selected.down.map((down, index) => (
          <View
            key={index}
            style={styles.dailyColumn}
            accessible
            accessibilityLabel={`${demoDays[index]}: ${checksPerDay - down} из ${checksPerDay} проверок успешны, ${successfulChecks(down, checksPerDay)}%, ошибок ${down}`}
          >
            <View style={[styles.dailyBar, { backgroundColor: c.border }]}>
              {down > 0 ? (
                <View
                  style={{
                    flex: down,
                    backgroundColor: c.dangerText,
                  }}
                />
              ) : null}
              <View
                style={{ flex: checksPerDay - down, backgroundColor: c.accent }}
              />
            </View>
            <Text style={[styles.small, { color: c.subtle }]}>
              {demoDays[index]}
            </Text>
          </View>
        ))}
      </Animated.View>
      <Label muted>
        По 288 проверок в день. Процент рассчитан по результатам проверок.
      </Label>
      <Label muted>
        В реальном мониторе доступны результаты проверок, задержка, HTTP-код и
        история инцидентов.
      </Label>
    </Card>
  );
}

export function PublicScreen({
  onLogin,
  onRegister,
  onHelp,
}: {
  onLogin: () => void;
  onRegister: () => void;
  onHelp?: () => void;
}) {
  const c = usePalette();
  const scroll = useRef<React.ComponentRef<typeof ScrollView>>(null);
  const sections = useRef<Record<string, number>>({});
  const jump = useCallback(
    (section: string) =>
      scroll.current?.scrollTo({
        y: sections.current[section] ?? 0,
        animated: false,
      }),
    [],
  );
  return (
    <View style={[styles.screen, { backgroundColor: c.bg }]}>
      <GlassBackdrop />
      <ScrollView ref={scroll} contentContainerStyle={styles.content}>
        <View style={styles.header}>
          <BrandHeader />
          <TextAction label="Войти" onPress={onLogin} />
        </View>
        <View style={styles.hero}>
          <Eyebrow>ВАШИ СЕРВИСЫ В ПОЛЕ ЗРЕНИЯ</Eyebrow>
          <Text
            accessibilityRole="header"
            style={[styles.heroTitle, { color: c.text }]}
          >
            Круглосуточный мониторинг ваших сайтов и сервисов.
          </Text>
          <Text style={[styles.heroSubtitle, { color: c.subtle }]}>
            Узнавайте о сбоях и восстановлении. Выберите цель, частоту проверок
            и удобный канал уведомлений.
          </Text>
          <Button
            label="Создать аккаунт"
            onPress={onRegister}
            iconName="arrow-right"
          />
          <TextAction label="У меня уже есть аккаунт" onPress={onLogin} />
          <Label muted>HTTP/HTTPS · TCP · ICMP</Label>
          <View style={styles.navigation}>
            <TextAction label="Возможности" onPress={() => jump("features")} />
            <TextAction label="Стоимость" onPress={() => jump("pricing")} />
            <TextAction label="Вопросы" onPress={() => jump("faq")} />
          </View>
        </View>
        <Demo />
        <View style={styles.section}>
          <Eyebrow>ЗАМЕТЬТЕ ПРОБЛЕМУ РАНЬШЕ КЛИЕНТА</Eyebrow>
          <Title>Не нужно постоянно открывать сайт</Title>
          <Label muted>
            Heartbeat Checker регулярно проверяет ваши цели и фиксирует сбои.
            Скорость обнаружения зависит от выбранных интервала и порога;
            доставка сообщения — ещё и от канала.
          </Label>
        </View>
        <Card>
          <DetailRow label="Интервал на ваш выбор" value="1–10 минут" />
          <DetailRow
            label="Порог подтверждения сбоя"
            value="1–10 ошибок подряд"
          />
          <DetailRow label="Подробные проверки" value="7 дней" />
          <DetailRow label="Закрытые инциденты" value="90 дней" />
          <Label muted>
            По умолчанию: проверка каждые 5 минут, подтверждение после 2 ошибок
            подряд.
          </Label>
        </Card>
        <View
          style={styles.section}
          onLayout={(event) => {
            sections.current.features = event.nativeEvent.layout.y;
          }}
        >
          <Eyebrow>ЧТО ВАЖНО ВАШЕМУ ПРОЕКТУ</Eyebrow>
          <Title>Три типа проверки</Title>
          <Label muted>
            Один монитор проверяет одну цель. Выбирайте проверку, которая
            отвечает на ваш вопрос.
          </Label>
        </View>
        <Feature
          icon="globe"
          title="HTTP/HTTPS · Сайт или API"
          example="GET / → ожидаем HTTP 200"
        >
          Выберите GET или HEAD и ожидаемый HTTP-код. Для GET можно
          дополнительно проверить наличие нужного текста в ответе.
        </Feature>
        <Feature
          icon="settings"
          title="TCP · Порт сервиса"
          example="server.example.com:22 → соединение"
        >
          Проверяйте публичный хост и порт, например SSH на порту 22. Успешное
          соединение подтверждает открытый порт; приложение за ним проверяется
          отдельно.
        </Feature>
        <Feature
          icon="activity"
          title="ICMP · Пинг сервера"
          example="server.example.com → ICMP echo"
        >
          Проверьте публичный IPv4 или домен с IPv4-адресом и время ping.
          Сетевой экран может блокировать ICMP даже на работающем сервере.
        </Feature>
        <View style={styles.section}>
          <Eyebrow>ПОНЯТНЫЙ ПРОЦЕСС</Eyebrow>
          <Title>Как работает мониторинг</Title>
        </View>
        <Card>
          {[
            [
              "01",
              "Настройте цель",
              "Укажите адрес, протокол, интервал и часовой пояс. Для запуска нужна действующая квота.",
            ],
            [
              "02",
              "Получите подтверждение сбоя",
              "Инцидент открывается после выбранного числа неудачных проверок подряд.",
            ],
            [
              "03",
              "Проследите восстановление",
              "Первый успешный ответ закрывает инцидент. В истории видны его начало, подтверждение и причина завершения.",
            ],
          ].map(([step, title, body]) => (
            <View key={step} style={styles.step}>
              <Text style={[styles.stepNumber, { color: c.accentText }]}>
                {step}
              </Text>
              <View style={styles.stepCopy}>
                <Text
                  accessibilityRole="header"
                  style={[styles.featureTitle, { color: c.text }]}
                >
                  {title}
                </Text>
                <Label muted>{body}</Label>
              </View>
            </View>
          ))}
        </Card>
        <Card>
          <Eyebrow>ДАННЫЕ ДЛЯ РАЗБОРА</Eyebrow>
          <Title>История проверок и сбоев</Title>
          <Label muted>
            Смотрите время, результат, длительность и причину ошибки. Для HTTP
            виден код ответа, для успешного ICMP — отдельно ping.
          </Label>
          <DetailRow label="UP" value="Проверка успешна" />
          <DetailRow label="DOWN" value="Условия проверки не выполнены" />
          <DetailRow label="UNKNOWN" value="Пока нет свежего результата" />
          <Label muted>
            UNKNOWN само по себе не означает падение сервиса. Пауза и изменение
            настроек отражаются в причинах завершения инцидентов. Открытые
            инциденты сохраняются.
          </Label>
        </Card>
        <Card>
          <Eyebrow>УВЕДОМЛЕНИЯ В УДОБНОМ КАНАЛЕ</Eyebrow>
          <Title>Сообщения о сбое и восстановлении</Title>
          <View style={styles.channels}>
            <Icon name="mail" />
            <Icon name="max" />
            <Icon name="telegram" />
            <Icon name="bell" />
          </View>
          <Label muted>
            Email, MAX, Telegram и вебхуки — при доступности канала в
            настройках. Для push разрешите уведомления Android и проверьте
            подключение. Результаты и инциденты всегда можно открыть в
            приложении.
          </Label>
        </Card>
        <View
          onLayout={(event) => {
            sections.current.pricing = event.nativeEvent.layout.y;
          }}
        >
          <PublicPrice onRegister={onRegister} />
        </View>
        <View
          onLayout={(event) => {
            sections.current.faq = event.nativeEvent.layout.y;
          }}
        >
          <Card>
            <Eyebrow>ДЕТАЛИ И ОГРАНИЧЕНИЯ</Eyebrow>
            <Title>До первого монитора — несколько ответов</Title>
            <FAQ />
          </Card>
        </View>
        <Card>
          <Title>Начните мониторинг своих сайтов</Title>
          <Label muted>
            Создайте аккаунт, выберите количество мониторов и настройте
            проверки.
          </Label>
          <Button
            label="Создать аккаунт"
            onPress={onRegister}
            iconName="arrow-right"
          />
          <Button label="Войти" onPress={onLogin} secondary />
        </Card>
        <Card>
          <Title>Поддержка и документы</Title>
          <SupportLinks compact />
          {onHelp ? (
            <TextAction
              label="Открыть раздел помощи"
              onPress={onHelp}
              iconName="info"
            />
          ) : null}
        </Card>
        <Label muted>
          Heartbeat Checker · Мониторинг доступности сайтов и сервисов
        </Label>
      </ScrollView>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  content: {
    padding: 18,
    paddingBottom: 40,
    gap: 18,
    width: "100%",
    maxWidth: 760,
    alignSelf: "center",
  },
  header: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    flexWrap: "wrap",
    columnGap: 12,
  },
  hero: { gap: 15, paddingVertical: 22 },
  heroTitle: {
    fontSize: 34,
    lineHeight: 40,
    fontWeight: "800",
    letterSpacing: -1,
  },
  heroSubtitle: { fontSize: 18, lineHeight: 28 },
  eyebrow: {
    fontSize: 11,
    lineHeight: 17,
    letterSpacing: 1.2,
    fontWeight: "800",
  },
  navigation: { flexDirection: "row", flexWrap: "wrap", gap: 16 },
  section: { gap: 8, paddingTop: 18, paddingHorizontal: 2 },
  featureHeading: { flexDirection: "row", alignItems: "center", gap: 12 },
  featureTitle: {
    fontSize: 17,
    lineHeight: 24,
    fontWeight: "700",
    flexShrink: 1,
  },
  iconTile: {
    width: 44,
    height: 44,
    borderRadius: 12,
    alignItems: "center",
    justifyContent: "center",
  },
  example: {
    fontSize: 13,
    lineHeight: 20,
    padding: 12,
    borderRadius: 10,
    fontFamily: "monospace",
  },
  demoRow: {
    flexDirection: "row",
    flexWrap: "wrap",
    alignItems: "center",
    gap: 8,
    borderWidth: 1,
    borderRadius: 10,
    padding: 10,
  },
  demoCopy: { width: "100%", gap: 3 },
  demoState: {
    flexDirection: "row",
    flexWrap: "wrap",
    alignItems: "center",
    justifyContent: "space-between",
    width: "100%",
    gap: 7,
  },
  demoName: { fontSize: 15, lineHeight: 22, fontWeight: "600" },
  small: { fontSize: 12, lineHeight: 18 },
  status: { fontSize: 13, lineHeight: 19, fontWeight: "800" },
  monitorTrack: {
    flexDirection: "row",
    height: 5,
    borderRadius: 3,
    overflow: "hidden",
    width: "100%",
  },
  analyticsSummary: { gap: 3, paddingTop: 8 },
  analyticsValue: { fontSize: 32, lineHeight: 40, fontWeight: "800" },
  dailyChart: { flexDirection: "row", gap: 8, paddingVertical: 8 },
  dailyColumn: { flex: 1, alignItems: "center", gap: 7 },
  dailyBar: { width: "100%", height: 72, borderRadius: 4, overflow: "hidden" },
  step: { flexDirection: "row", gap: 14, paddingVertical: 9 },
  stepNumber: { fontSize: 18, lineHeight: 25, fontWeight: "800" },
  stepCopy: { flex: 1, gap: 5 },
  channels: { flexDirection: "row", gap: 18, paddingVertical: 10 },
  price: { fontSize: 44, lineHeight: 54, fontWeight: "800", letterSpacing: -1 },
});
