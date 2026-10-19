import Image from "next/image";
import Link from "next/link";
import { AppStoreSoon, Brand, PublicFooter } from "@/shared/ui";
import { ThemeToggle } from "@/features/theme-toggle";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";
import { androidRelease } from "@/shared/android";

export const metadata = publicPageMetadata("/android");

export default function AndroidPage() {
  const release = androidRelease();
  return (
    <div className="landing android-page">
      <PublicPageStructuredData
        path="/android"
        android={release ?? undefined}
      />
      <header className="landing-header">
        <Brand />
        <nav aria-label="Главная навигация">
          <ThemeToggle />
          <Link href="/">На главную</Link>
        </nav>
      </header>
      <main id="main" className="android-main">
        <section className="android-hero" aria-labelledby="android-title">
          <div className="android-hero-copy">
            <div className="android-app-identity">
              <Image
                src="/brand/logo.svg"
                alt=""
                width={64}
                height={64}
                loading="eager"
                unoptimized
              />
              <p className="eyebrow">
                HEARTBEAT CHECKER
                <br />
                ПРИЛОЖЕНИЕ ДЛЯ ANDROID
              </p>
            </div>
            <h1 id="android-title">
              Мониторинг
              <br />
              под рукой.
            </h1>
            <p className="android-lead">
              Кабинет Heartbeat Checker и черновики адресов в одном приложении.
              Сохраните ссылку сейчас — добавьте монитор, когда удобно.
            </p>
          </div>
          <aside
            className="android-download-card"
            aria-labelledby="android-download-title"
            data-install-promotion
          >
            <h2 id="android-download-title">Установите приложение</h2>
            <p className="android-download-intro">Для Android 10 и новее</p>
            {release ? (
              <>
                <a
                  className="button android-apk-button"
                  href="/downloads/android"
                >
                  Скачать APK
                </a>
                <dl className="android-release-facts">
                  <div>
                    <dt>Версия</dt>
                    <dd>{release.versionName}</dd>
                  </div>
                  <div>
                    <dt>Размер APK</dt>
                    <dd>{(release.sizeBytes / 1024 / 1024).toFixed(1)} МБ</dd>
                  </div>
                  <div className="android-release-date">
                    <dt>Дата выпуска</dt>
                    <dd>
                      {new Intl.DateTimeFormat("ru", {
                        dateStyle: "long",
                        timeZone: "UTC",
                      }).format(new Date(release.releasedAt))}
                    </dd>
                  </div>
                </dl>
                <p className="android-install-note">
                  При установке Android может попросить разрешить установку из
                  выбранного браузера.
                </p>
              </>
            ) : (
              <p className="android-release-pending">
                Подписанная версия APK готовится к публикации. Ссылка на
                скачивание появится после проверки файла.
              </p>
            )}
            <div className="android-store-options">
              <p>Также появится в магазинах</p>
              <AppStoreSoon store="rustore" />
              <AppStoreSoon store="google-play" />
              <AppStoreSoon store="appgallery" />
            </div>
          </aside>
          <div className="android-hero-notes">
            <ul className="android-benefits">
              <li>Кабинет сервиса на вашем телефоне</li>
              <li>Ссылки из других приложений через «Поделиться»</li>
              <li>Черновики на устройстве доступны без сети</li>
            </ul>
            <p className="android-compatibility">
              Кабинету нужны интернет и совместимый браузер на устройстве. Без
              сети можно работать с локальными черновиками.
            </p>
          </div>
        </section>
        <section
          className="android-start"
          aria-labelledby="android-start-title"
        >
          <p className="eyebrow">БЕЗ ЛИШНИХ ДЕЙСТВИЙ</p>
          <h2 id="android-start-title">От ссылки до монитора — три шага</h2>
          <ol className="android-steps">
            <li>
              <span className="android-step-number" aria-hidden="true">
                01
              </span>
              <h3>Поделитесь ссылкой</h3>
              <p>
                В браузере или другом приложении выберите «Поделиться» и
                отправьте адрес в Heartbeat Checker.
              </p>
            </li>
            <li>
              <span className="android-step-number" aria-hidden="true">
                02
              </span>
              <h3>Сохраните черновик</h3>
              <p>
                Проверьте адрес и добавьте название. Черновик останется на
                устройстве и будет доступен без сети.
              </p>
            </li>
            <li>
              <span className="android-step-number" aria-hidden="true">
                03
              </span>
              <h3>Добавьте монитор</h3>
              <p>
                Откройте черновик в кабинете и подтвердите создание. Проверки
                начнутся после сохранения при действующей квоте.
              </p>
            </li>
          </ol>
        </section>
        <section
          className="android-account"
          aria-labelledby="android-account-title"
        >
          <div>
            <h2 id="android-account-title">Ваш аккаунт — тот же</h2>
            <p>
              Вход, мониторинг и оплата работают через защищённый сайт сервиса.
              Приложение не создаёт мониторы автоматически. Удаление локального
              черновика не меняет сохранённые мониторы.
            </p>
          </div>
          <nav aria-label="Об аккаунте и приложении">
            <Link href="/privacy">Персональные данные ↗</Link>
            <Link href="/terms">Условия сервиса ↗</Link>
            <Link href="/contacts">Поддержка ↗</Link>
          </nav>
        </section>
        {release && (
          <details className="android-verification" data-install-promotion>
            <summary>Проверка файла</summary>
            <div className="android-verification-content">
              <p>
                Версия {release.versionName} ({release.versionCode}). Обновляйте
                приложение подписанным APK с этой страницы. Для проверки
                сравните контрольную сумму файла и сертификат подписи.
              </p>
              <dl>
                <div>
                  <dt>SHA-256 файла</dt>
                  <dd>
                    <code>{release.sha256}</code>
                  </dd>
                </div>
                <div>
                  <dt>Сертификат подписи SHA-256</dt>
                  <dd>
                    <code>{release.signingCertificateSha256}</code>
                  </dd>
                </div>
              </dl>
            </div>
          </details>
        )}
      </main>
      <PublicFooter androidAvailable={Boolean(release)} />
    </div>
  );
}
