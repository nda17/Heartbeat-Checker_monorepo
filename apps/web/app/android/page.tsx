import Link from "next/link";
import { Brand, PublicFooter, RuStoreSoon } from "@/shared/ui";
import { ThemeToggle } from "@/features/theme-toggle";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";
import { androidRelease } from "@/shared/android";
export const metadata = publicPageMetadata("/android");
export default function AndroidPage() {
  const release = androidRelease();
  return (
    <div className="landing legal-page">
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
      <main id="main" className="legal-main">
        <p className="eyebrow">HEARTBEAT CHECKER / ANDROID</p>
        <h1>Мониторинг под рукой</h1>
        <p className="legal-lead">
          Кабинет Heartbeat Checker и черновики адресов в одном
          Android-приложении.
        </p>
        <article className="legal-content">
          <section>
            <h2>Сохраните адрес из любого приложения</h2>
            <p>
              Выберите «Поделиться» в браузере и отправьте ссылку в Heartbeat
              Checker. Черновики доступны на устройстве без сети. Откройте
              черновик в кабинете, проверьте параметры и добавьте монитор
              обычной кнопкой. Проверки начнутся только после сохранения и при
              действующей квоте.
            </p>
            <p>
              Для работы кабинета нужен интернет и браузер с поддержкой Trusted
              Web Activity. Интерфейс обновляется вместе с сайтом; новые
              возможности Android-оболочки устанавливаются с новой версией APK.
            </p>
          </section>
          <section>
            <h2>Скачать приложение</h2>
            {release ? (
              <>
                <div className="android-download-actions">
                  <a className="button" href="/downloads/android">
                    Скачать APK · {release.versionName}
                  </a>
                  <RuStoreSoon />
                </div>
                <p>
                  Android 10 и новее · Версия {release.versionName} (
                  {release.versionCode}) ·{" "}
                  {new Intl.DateTimeFormat("ru", {
                    dateStyle: "long",
                    timeZone: "UTC",
                  }).format(new Date(release.releasedAt))}{" "}
                  · {(release.sizeBytes / 1024 / 1024).toFixed(1)} МБ
                </p>
                <p>
                  При установке APK Android может попросить разрешить установку
                  из выбранного браузера. Обновляйте приложение тем же
                  подписанным APK с этой страницы.
                </p>
                <h3>Проверка файла</h3>
                <p>SHA-256:</p>
                <p className="mono" style={{ overflowWrap: "anywhere" }}>
                  {release.sha256}
                </p>
                <p>Сертификат подписи SHA-256:</p>
                <p className="mono" style={{ overflowWrap: "anywhere" }}>
                  {release.signingCertificateSha256}
                </p>
              </>
            ) : (
              <>
                <p>
                  Подписанная версия APK готовится к публикации. Ссылка на
                  скачивание появится здесь после проверки файла.
                </p>
                <div className="android-download-actions">
                  <RuStoreSoon />
                </div>
              </>
            )}
          </section>
          <section>
            <h2>Ваш аккаунт и данные</h2>
            <p>
              Вход, мониторинг и оплата работают через защищённый сайт сервиса.
              Приложение не создаёт мониторы автоматически. Локальные черновики
              удаляются в приложении; их удаление не изменяет сохранённые
              мониторы.
            </p>
            <p>
              <Link href="/privacy">Обработка персональных данных</Link> ·{" "}
              <Link href="/terms">Условия сервиса</Link> ·{" "}
              <Link href="/contacts">Поддержка</Link>
            </p>
          </section>
        </article>
      </main>
      <PublicFooter />
    </div>
  );
}
