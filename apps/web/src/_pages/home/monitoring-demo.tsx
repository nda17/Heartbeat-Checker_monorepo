"use client";
import { Icon } from "@/shared/ui";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";

const stages = [
  {
    label: "Доступен",
    code: 200,
    tone: "healthy",
    title: "Всё отвечает как нужно",
    text: "Код ответа совпал с ожидаемым. Проверка успешна.",
    event: "Инцидентов нет",
    count: "0 / 2",
    button: "Успех",
  },
  {
    label: "Сбой 1 из 2",
    code: 503,
    tone: "warning",
    title: "Одна ошибка — ещё не инцидент",
    text: "Проверка не пройдена. Ждём следующий результат для подтверждения сбоя.",
    event: "Инцидент ещё не открыт",
    count: "1 / 2",
    button: "Первый сбой",
  },
  {
    label: "Инцидент открыт",
    code: 503,
    tone: "incident",
    title: "Две ошибки подтверждают сбой",
    text: "Вторая неудачная проверка подряд открыла инцидент.",
    event: "Новый инцидент в истории",
    count: "2 / 2",
    button: "Второй сбой",
  },
  {
    label: "Доступ восстановлен",
    code: 200,
    tone: "recovered",
    title: "Первого успеха достаточно",
    text: "Сервис снова ответил ожидаемым кодом. Инцидент закрыт с причиной «Восстановление».",
    event: "Инцидент завершён",
    count: "0 / 2",
    button: "Восстановление",
  },
] as const;

const motionQuery = "(prefers-reduced-motion: reduce)";
function subscribeMotion(onChange: () => void) {
  const media = window.matchMedia(motionQuery);
  media.addEventListener("change", onChange);
  return () => media.removeEventListener("change", onChange);
}
function subscribeVisibility(onChange: () => void) {
  document.addEventListener("visibilitychange", onChange);
  return () => document.removeEventListener("visibilitychange", onChange);
}

const analyticsDays = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"];
const analyticsMonitors = [
  {
    id: "website",
    name: "Главная страница",
    target: "example.com",
    type: "HTTPS",
    down: [0, 0, 0, 0, 0, 0, 0],
  },
  {
    id: "api",
    name: "API магазина",
    target: "api.example.com/health",
    type: "HTTPS",
    down: [0, 0, 6, 0, 0, 0, 2],
  },
  {
    id: "ssh",
    name: "SSH сервера",
    target: "server.example.com:22",
    type: "TCP",
    down: [0, 0, 0, 0, 0, 0, 0],
  },
].map((monitor) => ({
  ...monitor,
  days: monitor.down.map((down, index) => ({
    label: analyticsDays[index],
    up: 288 - down,
    down,
  })),
}));

function successfulChecksPercent(up: number, down: number) {
  return (100 * up) / (up + down);
}

function formatChecksPercent(up: number, down: number) {
  return `${successfulChecksPercent(up, down).toLocaleString("ru-RU", {
    maximumFractionDigits: 1,
  })}%`;
}

export function AnalyticsDemo() {
  const [selectedId, setSelectedId] = useState("api");
  const selected = analyticsMonitors.find(
    (monitor) => monitor.id === selectedId,
  )!;
  const totals = selected.days.reduce(
    (sum, day) => ({ up: sum.up + day.up, down: sum.down + day.down }),
    { up: 0, down: 0 },
  );

  return (
    <section
      className="landing-analytics"
      aria-labelledby="landing-analytics-title"
    >
      <div className="landing-analytics-heading">
        <div>
          <p className="eyebrow">ИСТОРИЯ В ОДНОМ ВЗГЛЯДЕ</p>
          <h2 id="landing-analytics-title">Каждый сервис. Каждый день.</h2>
          <p>
            Выберите монитор и посмотрите, как менялись результаты проверок.
          </p>
        </div>
        <span className="landing-analytics-example">Пример интерфейса</span>
      </div>
      <div className="landing-analytics-panel">
        <div
          role="group"
          aria-label="Мониторы в примере"
          className="landing-analytics-monitors"
        >
          {analyticsMonitors.map((monitor) => {
            const up = monitor.days.reduce((sum, day) => sum + day.up, 0);
            const down = monitor.days.reduce((sum, day) => sum + day.down, 0);
            return (
              <button
                type="button"
                key={monitor.id}
                aria-pressed={selectedId === monitor.id}
                aria-controls="landing-analytics-details"
                onClick={() => setSelectedId(monitor.id)}
              >
                <span className="landing-analytics-monitor-icon">
                  <Icon name={monitor.type === "TCP" ? "network" : "globe"} />
                </span>
                <span className="landing-analytics-monitor-name">
                  <strong>{monitor.name}</strong>
                  <span>{monitor.type} · за 7 дней</span>
                </span>
                <span className="landing-analytics-monitor-percent">
                  <span className="sr-only">Успешные проверки: </span>
                  {formatChecksPercent(up, down)}
                </span>
              </button>
            );
          })}
        </div>
        <div
          id="landing-analytics-details"
          className="landing-analytics-details"
          role="region"
          aria-label={`Результаты проверок: ${selected.name}`}
        >
          <div className="landing-analytics-summary">
            <div>
              <h3>{selected.name}</h3>
              <p>{selected.target}</p>
            </div>
            <div className="landing-analytics-score">
              <strong>{formatChecksPercent(totals.up, totals.down)}</strong>
              <span>Успешные проверки за 7 дней</span>
            </div>
          </div>
          <figure className="landing-analytics-chart">
            <figcaption>Доля успешных проверок по дням</figcaption>
            <ol>
              {selected.days.map((day) => (
                <li
                  key={day.label}
                  aria-label={`${day.label}: ${formatChecksPercent(day.up, day.down)}, успешных ${day.up}, неудачных ${day.down}`}
                >
                  <span aria-hidden="true">
                    {formatChecksPercent(day.up, day.down)}
                  </span>
                  <div className="landing-analytics-bar" aria-hidden="true">
                    <span
                      className="landing-analytics-bar-down"
                      style={{
                        height: `${successfulChecksPercent(day.down, day.up)}%`,
                      }}
                    />
                    <span
                      className="landing-analytics-bar-up"
                      style={{
                        height: `${successfulChecksPercent(day.up, day.down)}%`,
                      }}
                    />
                  </div>
                  <span aria-hidden="true">{day.label}</span>
                </li>
              ))}
            </ol>
          </figure>
          <dl className="landing-analytics-counts">
            <div>
              <dt>
                <i className="landing-analytics-key-up" />
                Успешных
              </dt>
              <dd>{totals.up.toLocaleString("ru-RU")}</dd>
            </div>
            <div>
              <dt>
                <i className="landing-analytics-key-down" />
                Неудачных
              </dt>
              <dd>{totals.down}</dd>
            </div>
            <div>
              <dt>Всего проверок</dt>
              <dd>{(totals.up + totals.down).toLocaleString("ru-RU")}</dd>
            </div>
          </dl>
        </div>
      </div>
    </section>
  );
}

export function MonitoringDemo() {
  const container = useRef<HTMLDivElement>(null);
  const [stage, setStage] = useState(0);
  const [playback, setPlayback] = useState<"auto" | "playing" | "paused">(
    "auto",
  );
  const [inView, setInView] = useState(false);
  const reducedMotion = useSyncExternalStore(
    subscribeMotion,
    () => window.matchMedia(motionQuery).matches,
    () => true,
  );
  const tabVisible = useSyncExternalStore(
    subscribeVisibility,
    () => document.visibilityState === "visible",
    () => false,
  );

  useEffect(() => {
    const observer = new IntersectionObserver(
      ([entry]) => setInView(entry.isIntersecting),
      { threshold: 0.15 },
    );
    if (container.current) observer.observe(container.current);
    return () => observer.disconnect();
  }, []);

  // Auto mode respects the browser preference; an explicit Continue is opt-in.
  const playing =
    playback === "playing" || (playback === "auto" && !reducedMotion);
  const running = playing && inView && tabVisible;
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(
      () => setStage((value) => (value + 1) % stages.length),
      4000,
    );
    return () => clearInterval(timer);
  }, [running]);

  const current = stages[stage];
  return (
    <div
      id="demo"
      ref={container}
      className="landing-demo"
      data-tone={current.tone}
      data-running={running}
    >
      <div className="landing-demo-top">
        <span className="landing-demo-label">ДЕМОНСТРАЦИЯ HTTP</span>
      </div>
      <div className="landing-demo-monitor">
        <div>
          <strong>Основной сайт</strong>
          <code>GET demo.example.test</code>
        </div>
        <span className="landing-demo-status">
          <i />
          {current.label}
        </span>
      </div>
      <div className="landing-demo-signal" aria-hidden="true">
        <svg viewBox="0 0 480 88" preserveAspectRatio="none">
          <defs>
            <pattern
              id="landing-demo-grid"
              width="24"
              height="22"
              patternUnits="userSpaceOnUse"
            >
              <path className="landing-demo-grid-line" d="M24 0H0V22" />
            </pattern>
          </defs>
          <rect width="480" height="88" fill="url(#landing-demo-grid)" />
          <path
            className="landing-demo-ecg"
            pathLength="100"
            d="M0 44H70l9-7 9 7h28l12-25 19 48 15-33 12 10h57l9-7 9 7h48l12-25 19 48 15-33 12 10h116"
          />
        </svg>
        <span className="landing-demo-beat" />
      </div>
      <div
        key={stage}
        className="landing-demo-stage"
        id="landing-demo-stage"
        aria-live="off"
      >
        <div className="landing-demo-readings">
          <div>
            <span>Ответ сервера</span>
            <strong>HTTP {current.code}</strong>
          </div>
          <div>
            <span>Сбоев подряд</span>
            <strong>{current.count}</strong>
          </div>
        </div>
        <h2>{current.title}</h2>
        <p>{current.text}</p>
        <div className="landing-demo-event">
          <span aria-hidden="true">
            <Icon
              name={stage === 2 ? "alert" : stage === 3 ? "check" : "dot"}
              size={16}
            />
          </span>
          {current.event}
        </div>
      </div>
      <div className="landing-demo-controls">
        <div
          role="group"
          aria-label="Этапы демонстрации"
          className="landing-demo-steps"
        >
          {stages.map((item, index) => (
            <button
              key={item.tone}
              type="button"
              aria-pressed={stage === index}
              aria-controls="landing-demo-stage"
              onClick={() => {
                setStage(index);
                setPlayback("paused");
              }}
              title={item.button}
            >
              <span>{index + 1}</span>
              <span className="sr-only">{item.button}</span>
            </button>
          ))}
        </div>
        <button
          type="button"
          className="landing-demo-play"
          onClick={() => setPlayback(playing ? "paused" : "playing")}
          aria-label={
            playing ? "Приостановить демонстрацию" : "Продолжить демонстрацию"
          }
        >
          <Icon name={playing ? "pause" : "play"} />
          {playing ? "Пауза" : "Продолжить"}
        </button>
      </div>
      <p className="landing-demo-caption">
        Пример: интервал 5 минут, порог — 2 сбоя подряд. Время ускорено.
      </p>
    </div>
  );
}
