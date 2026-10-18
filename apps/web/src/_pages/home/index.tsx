import Link from "next/link";
import { ThemeToggle } from "@/features/theme-toggle";
import { Arrow, Brand } from "@/shared/ui";
import { MonitoringDemo } from "./monitoring-demo";

export function HomePage() {
  return (
    <div className="landing">
      <header className="landing-header">
        <Brand />
        <nav aria-label="Главная навигация">
          <ThemeToggle />
          <Link href="#how">Как это работает</Link>
          <Link href="#pricing">Тариф</Link>
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
              Сайт отвечает? API вернул нужный код? На странице есть важный
              текст? Выбирайте интервал от 1 до 10 минут и разбирайте сбои по
              реальным результатам.
            </p>
            <div className="hero-actions">
              <Link href="/register" className="button">
                Открыть своё пространство <Arrow />
              </Link>
              <a href="#use-cases" className="text-link">
                Что можно проверять ↓
              </a>
            </div>
            <p className="small muted hero-note">
              GET и HEAD · История проверок и инцидентов · Управление через сайт
            </p>
          </div>
          <MonitoringDemo />
        </section>

        <div
          className="landing-facts"
          aria-label="Основные правила мониторинга"
        >
          <div>
            <strong>1–10 мин</strong>
            <span>интервал · по умолчанию 5 мин</span>
          </div>
          <div>
            <strong>1–10 сбоев</strong>
            <span>порог инцидента · по умолчанию 2</span>
          </div>
          <div>
            <strong>7 дней</strong>
            <span>подробных результатов</span>
          </div>
          <div>
            <strong>90 дней</strong>
            <span>закрытых инцидентов</span>
          </div>
        </div>

        <section id="use-cases" className="landing-section">
          <div className="landing-section-heading">
            <div>
              <p className="eyebrow">ЧТО ВАЖНО ВАШЕМУ ПРОЕКТУ</p>
              <h2>
                Три простых способа
                <br />
                держать руку на пульсе.
              </h2>
            </div>
            <p>
              Один монитор — один настроенный запрос. Метод, код ответа и текст
              можно сочетать в рамках одной проверки.
            </p>
          </div>
          <div className="landing-use-cases">
            <article>
              <div className="landing-case-icon" aria-hidden="true">
                ↗
              </div>
              <span className="landing-case-type">САЙТ ИЛИ ЛЕНДИНГ</span>
              <h3>Страница доступна</h3>
              <p>
                Проверяйте основной сайт, страницу входа или другой публичный
                адрес. Ожидаемый HTTP-код задаёте вы.
              </p>
              <div className="landing-case-setting">
                <code>GET /</code>
                <span>Ожидаем: HTTP 200</span>
              </div>
            </article>
            <article>
              <div className="landing-case-icon" aria-hidden="true">
                {"{ }"}
              </div>
              <span className="landing-case-type">ПУБЛИЧНЫЙ API</span>
              <h3>Сервис отвечает</h3>
              <p>
                Следите за публичным endpoint. Выберите GET или HEAD, если
                достаточно проверить код без чтения тела ответа.
              </p>
              <div className="landing-case-setting">
                <code>HEAD /health</code>
                <span>Ожидаем: HTTP 200</span>
              </div>
            </article>
            <article>
              <div className="landing-case-icon" aria-hidden="true">
                Aa
              </div>
              <span className="landing-case-type">СОДЕРЖИМОЕ СТРАНИЦЫ</span>
              <h3>На месте нужный текст</h3>
              <p>
                Одного кода 200 бывает мало. Для GET добавьте условие: в
                полученном ответе должен присутствовать указанный текст.
              </p>
              <div className="landing-case-setting">
                <code>GET + текст</code>
                <span>Например: «Каталог»</span>
              </div>
            </article>
          </div>
        </section>

        <section id="how" className="how-section">
          <div className="section-intro">
            <p className="eyebrow">ПОНЯТНЫЙ ПРОЦЕСС</p>
            <h2>
              От адреса —<br /> к ясной картине.
            </h2>
          </div>
          <div className="how-grid">
            <article>
              <span className="step">01</span>
              <h3>Настройте проверку</h3>
              <p>
                Добавьте публичный URL, метод, условия успешного ответа и
                интервал 1–10 минут. Проверки работают при действующей квоте.
              </p>
            </article>
            <article>
              <span className="step">02</span>
              <h3>Сбой подтверждается автоматически</h3>
              <p>
                Выберите, сколько неудачных проверок подряд открывают инцидент:
                от 1 до 10. Порог задаётся отдельно для каждого монитора.
              </p>
            </article>
            <article>
              <span className="step">03</span>
              <h3>Проследите восстановление</h3>
              <p>
                Первый успешный ответ закрывает инцидент. В истории видны его
                начало, подтверждение и причина завершения.
              </p>
            </article>
          </div>
        </section>

        <section className="landing-observability landing-section">
          <div className="landing-observability-copy">
            <p className="eyebrow">ДАННЫЕ ДЛЯ РАЗБОРА, А НЕ ДОГАДОК</p>
            <h2>
              За каждым состоянием
              <br />
              есть конкретный результат.
            </h2>
            <p>
              Откройте монитор и посмотрите, что произошло: когда прошла
              проверка, какой код вернул сервер, сколько занял ответ и возникла
              ли ошибка.
            </p>
            <ul>
              <li>
                <span aria-hidden="true">↗</span>
                <div>
                  <h3>Подробные проверки</h3>
                  <p>
                    Время, HTTP-код, задержка ответа и причина ошибки — за
                    последние 7 дней.
                  </p>
                </div>
              </li>
              <li>
                <span aria-hidden="true">↗</span>
                <div>
                  <h3>История инцидентов</h3>
                  <p>
                    Закрытые инциденты хранятся 90 дней, открытые сохраняются.
                    Пауза и изменение настроек отмечаются своей причиной.
                  </p>
                </div>
              </li>
            </ul>
          </div>
          <div className="landing-states">
            <div className="landing-states-heading">
              <span className="eyebrow">КАК ЧИТАТЬ СОСТОЯНИЯ</span>
              <span aria-hidden="true">◉</span>
            </div>
            <article>
              <span className="badge up">
                <span className="status-dot" />
                UP
              </span>
              <div>
                <h3>Последняя проверка успешна</h3>
                <p>Ответ соответствует всем заданным условиям.</p>
              </div>
            </article>
            <article>
              <span className="badge down">
                <span className="status-dot" />
                DOWN
              </span>
              <div>
                <h3>Проверка не пройдена</h3>
                <p>
                  Не совпал код, отсутствует текст или возникла ошибка. Инцидент
                  открывается при достижении заданного порога сбоев подряд.
                </p>
              </div>
            </article>
            <article>
              <span className="badge unknown">
                <span className="status-dot" />
                UNKNOWN
              </span>
              <div>
                <h3>Пока нет свежих данных</h3>
                <p>
                  Неизвестное состояние само по себе не означает падение вашего
                  сайта.
                </p>
              </div>
            </article>
            <div className="landing-state-note">
              На паузе или без действующей квоты проверки останавливаются. Это
              отдельные состояния, а не инциденты сайта.
            </div>
          </div>
        </section>

        <section id="pricing" className="landing-pricing landing-section">
          <div className="landing-pricing-copy">
            <p className="eyebrow">ПОНЯТНАЯ ЕДИНИЦА СТОИМОСТИ</p>
            <h2>
              Платить за нужные адреса.
              <br />
              Менять их по мере роста.
            </h2>
            <p>
              Планируемый тариф — за монитор в месяц. Несколько условий одного
              HTTP-ответа не увеличивают число мониторов. URL можно заменять
              внутри своей квоты.
            </p>
            <span className="landing-planned-label">
              Планируемый тариф · Оплата пока недоступна
            </span>
          </div>
          <div className="landing-price-card">
            <div className="landing-price">
              <strong>
                24<span> ₽</span>
              </strong>
              <p>
                за монитор
                <br />в месяц
              </p>
            </div>
            <div className="landing-price-examples">
              <div>
                <span>3 монитора</span>
                <strong>
                  72 ₽ <small>/ мес.</small>
                </strong>
              </div>
              <div>
                <span>10 мониторов</span>
                <strong>
                  240 ₽ <small>/ мес.</small>
                </strong>
              </div>
            </div>
            <p className="landing-price-note">
              Пробного периода нет. После регистрации квота равна 0. Подключение
              оплаты и оплаченных проверок — следующий этап.
            </p>
            <Link href="/register" className="button">
              Создать аккаунт <Arrow />
            </Link>
          </div>
        </section>

        <section id="faq" className="landing-faq landing-section">
          <div className="section-intro">
            <p className="eyebrow">ДЕТАЛИ И ОГРАНИЧЕНИЯ</p>
            <h2>
              До первого монитора
              <br /> осталось несколько ответов.
            </h2>
            <p>Честно о том, что уже работает и что ещё впереди.</p>
          </div>
          <div className="landing-faq-list">
            <details>
              <summary>Что считается одним монитором?</summary>
              <p>
                Один настроенный HTTP-запрос к одному URL. Метод, ожидаемый код
                и проверка текста относятся к одному монитору. Повторные
                проверки того же монитора не создают дополнительные платные
                единицы.
              </p>
            </details>
            <details>
              <summary>Сколько хранится история?</summary>
              <p>
                Подробные результаты проверок — 7 дней. Закрытые инциденты
                хранятся 90 дней после завершения; открытые инциденты
                сохраняются. История доступна в кабинете в пределах этих сроков,
                в том числе при приостановке проверок.
              </p>
            </details>
            <details>
              <summary>Почему UNKNOWN отличается от DOWN?</summary>
              <p>
                DOWN означает неуспешную проверку. UNKNOWN — отсутствие свежего
                результата, в том числе при проблеме исполнителя. Это не
                позволяет утверждать, что сайт недоступен. Серию сбоев
                сбрасывает принятый неизвестный результат проверки (UNKNOWN), а
                не само отсутствие свежих данных. Число неуспешных проверок
                подряд для открытия инцидента задаётся в настройках монитора.
              </p>
            </details>
            <details>
              <summary>Освобождает ли пауза место в квоте?</summary>
              <p>
                Нет. Квота учитывает настроенные мониторы, включая
                приостановленные. Чтобы освободить место, удалите ненужный
                монитор. Для другого адреса можно изменить URL существующего
                монитора.
              </p>
            </details>
            <details>
              <summary>Как будут приходить уведомления?</summary>
              <p>
                Запланированы Telegram — личные сообщения, группы и темы — а
                также email и MAX. Привязка получателей и доставка ещё не
                подключены. Сейчас результаты и инциденты доступны в кабинете.
              </p>
            </details>
            <details>
              <summary>Как быстро подтверждается сбой?</summary>
              <p>
                Это зависит от интервала и порога конкретного монитора. Доступны
                интервалы 1–10 минут и пороги 1–10 сбоев подряд; для новых
                мониторов по умолчанию — 5 минут и 2 сбоя. Сначала нужно
                дождаться проверок и достичь заданного порога. Доставка
                уведомлений ещё не подключена; интервал проверок не является
                обещанием срока уведомления.
              </p>
            </details>
          </div>
        </section>

        <section className="landing-cta">
          <div>
            <p className="eyebrow">МЕНЬШЕ НЕИЗВЕСТНОСТИ</p>
            <h2>
              Ваш следующий шаг —<br />
              своё рабочее пространство.
            </h2>
            <p>
              Создайте аккаунт, подтвердите email и откройте кабинет.
              <br />
              Доступная квота отображается внутри.
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
