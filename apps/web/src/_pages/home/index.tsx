import Link from "next/link";
import { ThemeToggle } from "@/features/theme-toggle";
import { Arrow, Brand } from "@/shared/ui";
export function HomePage() {
  return (
    <div className="landing">
      <header className="landing-header">
        <Brand />
        <nav aria-label="Главная навигация">
          <ThemeToggle />
          <Link href="#how">Как это работает</Link>
          <Link href="/login">Войти</Link>
          <Link className="button small-button" href="/register">
            Создать аккаунт <Arrow />
          </Link>
        </nav>
      </header>
      <main id="main">
        <section className="hero">
          <div className="hero-copy">
            <p className="eyebrow">
              <span className="live-dot" /> ВАШИ СЕРВИСЫ В ПОЛЕ ЗРЕНИЯ
            </p>
            <h1>
              Проверять сайты.
              <br />
              <span>Сохранять спокойствие.</span>
            </h1>
            <p className="hero-description">
              Доступность сайта или API не должна быть загадкой. Настройте
              HTTP-проверки и разбирайте результаты в одном понятном кабинете.
            </p>
            <div className="hero-actions">
              <Link href="/register" className="button">
                Открыть своё пространство <Arrow />
              </Link>
              <Link href="/login" className="text-link">
                Уже с нами? Войти →
              </Link>
            </div>
            <p className="small muted hero-note">
              Без пробного периода. Проверки работают при действующей квоте.
            </p>
          </div>
          <div className="hero-object">
            <div className="object-label">
              <span className="brand-mark mini" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none">
                  <path
                    d="M3 12h4l2.5-6 5 12 2.5-6h4"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              </span>
              Наблюдайте за важным
              <span className="object-number">01 / HTTP</span>
            </div>
            <div className="signal-orbit" aria-hidden="true">
              <div className="orbit-ring ring-one" />
              <div className="orbit-ring ring-two" />
              <div className="orbit-core">
                <svg viewBox="0 0 80 80" fill="none">
                  <path
                    d="M8 40h15l9-22 16 44 9-22h15"
                    stroke="currentColor"
                    strokeWidth="4"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              </div>
              <span className="orbit-node node-one" />
              <span className="orbit-node node-two" />
              <span className="orbit-node node-three" />
            </div>
            <div className="object-footer">
              <span>URL → проверка → результат</span>
              <span>Всё под наблюдением ↗</span>
            </div>
          </div>
        </section>
        <section id="how" className="how-section">
          <div className="section-intro">
            <p className="eyebrow">ПОНЯТНЫЙ ПРОЦЕСС</p>
            <h2>
              От адреса —<br />к ясной картине.
            </h2>
          </div>
          <div className="how-grid">
            <article>
              <span className="step">01</span>
              <h3>Добавьте адрес</h3>
              <p>
                Укажите публичный URL, выберите GET или HEAD и ожидаемый код
                ответа.
              </p>
            </article>
            <article>
              <span className="step">02</span>
              <h3>Задайте условия</h3>
              <p>
                Для GET можно проверить наличие текста. Каждая настройка
                относится к одному монитору.
              </p>
            </article>
            <article>
              <span className="step">03</span>
              <h3>Разбирайте события</h3>
              <p>
                Смотрите реальные результаты, задержку ответа и историю открытых
                и закрытых инцидентов.
              </p>
            </article>
          </div>
        </section>
        <section className="landing-cta">
          <div>
            <p className="eyebrow">МЕНЬШЕ НЕИЗВЕСТНОСТИ</p>
            <h2>
              Ваш следующий шаг —<br />
              первый монитор.
            </h2>
            <p>
              Создайте аккаунт и откройте кабинет. Доступная квота отображается
              внутри.
            </p>
          </div>
          <Link href="/register" className="button">
            Создать аккаунт <Arrow />
          </Link>
        </section>
      </main>
      <footer className="landing-footer">
        <Brand />
        <span>Мониторинг начинается с внимания.</span>
        <Link href="/login">Личный кабинет ↗</Link>
      </footer>
    </div>
  );
}
