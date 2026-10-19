"use client";
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
            {stage === 2 ? "!" : stage === 3 ? "✓" : "·"}
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
          <span aria-hidden="true">{playing ? "Ⅱ" : "▷"}</span>
          {playing ? "Пауза" : "Продолжить"}
        </button>
      </div>
      <p className="landing-demo-caption">
        Пример: интервал 5 минут, порог — 2 сбоя подряд. Время ускорено.
      </p>
    </div>
  );
}
