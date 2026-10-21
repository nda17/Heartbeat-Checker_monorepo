import { useId } from "react";
import { Icon } from "./icon";
export { Icon } from "./icon";
import Link from "next/link";
import { FooterLegalNotice } from "./legal-notice";
export { FooterLegalNotice };
export { PasswordInput } from "./password-input";
export function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <Link href="/" className="brand" aria-label="Heartbeat Checker — главная">
      <span className="brand-mark" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" focusable="false">
          <path
            d="M3 12h4l2.5-6 5 12 2.5-6h4"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
      <span className="brand-wordmark">
        Heartbeat{!compact && <span className="brand-light"> Checker</span>}
      </span>
    </Link>
  );
}
// Icon sources and license notices: /third-party-notices.txt
type AppStore = "rustore" | "google-play" | "appgallery";
function AppStoreIcon({ store }: { store: AppStore }) {
  if (store === "google-play") {
    // Official Google Play store header: https://play.google.com/store/apps
    return (
      <svg
        width="24"
        height="24"
        viewBox="0 0 40 40"
        aria-hidden="true"
        focusable="false"
      >
        <path
          fill="#EA4335"
          d="M19.7,19.2L4.3,35.3c0,0,0,0,0,0c0.5,1.7,2.1,3,4,3c0.8,0,1.5-0.2,2.1-0.6l0,0l17.4-9.9L19.7,19.2z"
        />
        <path
          fill="#FBBC04"
          d="M35.3,16.4L35.3,16.4l-7.5-4.3l-8.4,7.4l8.5,8.3l7.5-4.2c1.3-0.7,2.2-2.1,2.2-3.6C37.5,18.5,36.6,17.1,35.3,16.4z"
        />
        <path
          fill="#4285F4"
          d="M4.3,4.7C4.2,5,4.2,5.4,4.2,5.8v28.5c0,0.4,0,0.7,0.1,1.1l16-15.7L4.3,4.7z"
        />
        <path
          fill="#34A853"
          d="M19.8,20l8-7.9L10.5,2.3C9.9,1.9,9.1,1.7,8.3,1.7c-1.9,0-3.6,1.3-4,3c0,0,0,0,0,0L19.8,20z"
        />
      </svg>
    );
  }
  if (store === "appgallery") {
    // Original SVG asset from https://appgallery.huawei.com/.
    return (
      <svg
        width="24"
        height="24"
        viewBox="2 2 36 36"
        aria-hidden="true"
        focusable="false"
      >
        <image href="/brand/appgallery.svg" width="40" height="40" />
      </svg>
    );
  }
  // Official RuStore media kit: https://www.rustore.ru/media-kit
  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 96 96"
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      <path
        d="M6.74826 89.2517C13.4965 96 24.3577 96 46.08 96H49.92C71.6423 96 82.5034 96 89.2518 89.2517C96 82.5034 96 71.6422 96 49.9197V46.0797C96 24.3573 96 13.4961 89.2518 6.74786C82.5034 0 71.6423 0 49.9201 0H46.08C24.3577 0 13.4965 0 6.74826 6.74787C0 13.4961 0 24.3573 0 46.0797V49.9198C0 71.6422 0 82.5035 6.74826 89.2517Z"
        fill="#0077FF"
      />
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M74.1174 63.8091L67.6309 62.1887C66.8499 61.9647 66.2983 61.2619 66.2706 60.445L65.4611 36.5712C65.227 33.4112 62.8813 30.8981 60.4301 30.158C60.2925 30.1164 60.1456 30.1724 60.0641 30.2908C59.9813 30.4112 60.0132 30.5777 60.1288 30.6671C60.7341 31.1357 62.4 32.6648 62.4 35.3022L62.3952 66.5166C62.3952 69.5307 59.5471 71.7413 56.6093 71.0074L50.0226 69.362C49.2923 69.1064 48.785 68.4263 48.7584 67.6417L47.949 43.7662C47.7149 40.6063 45.3692 38.0932 42.918 37.3531C42.7804 37.3115 42.6335 37.3675 42.552 37.4859C42.4692 37.6062 42.5011 37.7728 42.6167 37.8622C43.222 38.3307 44.8879 39.8599 44.8879 42.4973L44.883 68.5161L44.8867 68.5168V73.7147C44.8867 76.7288 42.0386 78.9393 39.1007 78.2055L20.4963 73.558C17.9098 72.9119 16.0963 70.5982 16.0963 67.9445L16.0963 36.6814C16.0963 33.6673 18.9444 31.4567 21.8822 32.1906L33.6048 35.119V29.4833C33.6048 26.4692 36.4529 24.2587 39.3908 24.9925L51.1129 27.9208V22.285C51.1129 19.2709 53.961 17.0603 56.8988 17.7942L75.5032 22.4417C78.0897 23.0878 79.9033 25.4015 79.9033 28.0552V59.3183C79.9033 62.3324 77.0552 64.5429 74.1174 63.8091Z"
        fill="white"
      />
    </svg>
  );
}
export function AppStoreSoon({
  store,
  label = store === "rustore"
    ? "Установить из RuStore"
    : store === "google-play"
      ? "Установить из Google Play"
      : "Установить из AppGallery",
}: {
  store: AppStore;
  label?: string;
}) {
  return (
    <span className="store-soon">
      <button
        type="button"
        aria-disabled="true"
        aria-label={`${label} — Скоро`}
        title="Скоро"
      >
        <AppStoreIcon store={store} />
        <span>{label}</span>
      </button>
      <span className="store-soon-tooltip" aria-hidden="true">
        Скоро
      </span>
    </span>
  );
}
function AndroidIcon() {
  // Google Material Icons 3.0.1, full Android robot (Apache-2.0).
  return (
    <svg
      className="footer-link-icon"
      width="24"
      height="24"
      viewBox="0 0 24 24"
      aria-hidden="true"
      focusable="false"
    >
      <path
        fill="#3DDC84"
        d="M6 18c0 .55.45 1 1 1h1v3.5c0 .83.67 1.5 1.5 1.5s1.5-.67 1.5-1.5V19h2v3.5c0 .83.67 1.5 1.5 1.5s1.5-.67 1.5-1.5V19h1c.55 0 1-.45 1-1V8H6v10zM3.5 8C2.67 8 2 8.67 2 9.5v7c0 .83.67 1.5 1.5 1.5S5 17.33 5 16.5v-7C5 8.67 4.33 8 3.5 8zm17 0c-.83 0-1.5.67-1.5 1.5v7c0 .83.67 1.5 1.5 1.5s1.5-.67 1.5-1.5v-7c0-.83-.67-1.5-1.5-1.5zm-4.97-5.84l1.3-1.3c.2-.2.2-.51 0-.71-.2-.2-.51-.2-.71 0l-1.48 1.48C13.85 1.23 12.95 1 12 1c-.96 0-1.86.23-2.66.63L7.85.15c-.2-.2-.51-.2-.71 0-.2.2-.2.51 0 .71l1.31 1.31C6.97 3.26 6 5.01 6 7h12c0-1.99-.97-3.75-2.47-4.84zM10 5H9V4h1v1zm5 0h-1V4h1v1z"
      />
    </svg>
  );
}
// Original vendor SVGs use isolated image documents so their IDs cannot collide.
function VKLogo() {
  return <image href="/brand/vk-colored.svg" width="24" height="24" />;
}
export function OAuthIcon({ provider }: { provider: "yandex" | "vk" }) {
  return (
    <svg
      className="oauth-provider-icon"
      width="20"
      height="20"
      viewBox={provider === "vk" ? "3 3 18 18" : "0 0 44 44"}
      style={provider === "yandex" ? { borderRadius: "50%" } : undefined}
      aria-hidden="true"
      focusable="false"
    >
      {provider === "vk" ? (
        <VKLogo />
      ) : (
        <image href="/brand/yandex.svg" width="44" height="44" />
      )}
    </svg>
  );
}

export function MessengerIcon({ brand }: { brand: "vk" | "max" | "telegram" }) {
  const gradientId = `telegram-${useId().replace(/:/g, "")}`;
  // Official Telegram press assets: https://telegram.org/tour/screenshots
  if (brand === "telegram") {
    return (
      <svg
        className="footer-link-icon"
        width="24"
        height="24"
        viewBox="0 0 1000 1000"
        aria-hidden="true"
        focusable="false"
      >
        <defs>
          <linearGradient
            id={gradientId}
            x1="50%"
            y1="0%"
            x2="50%"
            y2="99.2583404%"
          >
            <stop stopColor="#2AABEE" offset="0%" />
            <stop stopColor="#229ED9" offset="100%" />
          </linearGradient>
        </defs>
        <circle fill={`url(#${gradientId})`} cx="500" cy="500" r="500" />
        <path
          d="M226.328419,494.722069 C372.088573,431.216685 469.284839,389.350049 517.917216,369.122161 C656.772535,311.36743 685.625481,301.334815 704.431427,301.003532 C708.567621,300.93067 717.815839,301.955743 723.806446,306.816707 C728.864797,310.92121 730.256552,316.46581 730.922551,320.357329 C731.588551,324.248848 732.417879,333.113828 731.758626,340.040666 C724.234007,419.102486 691.675104,610.964674 675.110982,699.515267 C668.10208,736.984342 654.301336,749.547532 640.940618,750.777006 C611.904684,753.448938 589.856115,731.588035 561.733393,713.153237 C517.726886,684.306416 492.866009,666.349181 450.150074,638.200013 C400.78442,605.66878 432.786119,587.789048 460.919462,558.568563 C468.282091,550.921423 596.21508,434.556479 598.691227,424.000355 C599.00091,422.680135 599.288312,417.758981 596.36474,415.160431 C593.441168,412.561881 589.126229,413.450484 586.012448,414.157198 C581.598758,415.158943 511.297793,461.625274 375.109553,553.556189 C355.154858,567.258623 337.080515,573.934908 320.886524,573.585046 C303.033948,573.199351 268.692754,563.490928 243.163606,555.192408 C211.851067,545.013936 186.964484,539.632504 189.131547,522.346309 C190.260287,513.342589 202.659244,504.134509 226.328419,494.722069 Z"
          fill="#FFFFFF"
        />
      </svg>
    );
  }
  // VK: https://github.com/VKCOM/icons — logo_vk_24.svg
  // MAX: https://go.max.ru/brandbook — exact official colored SVG; isolated IDs.
  return (
    <svg
      className="footer-link-icon"
      width="24"
      height="24"
      viewBox={brand === "vk" ? "3 3 18 18" : "0 0 100 100"}
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      {brand === "vk" ? (
        <VKLogo />
      ) : (
        <image href="/brand/max-colored.svg" width="100" height="100" />
      )}
    </svg>
  );
}
export function PublicFooter({
  onHome = false,
  androidAvailable = true,
}: {
  onHome?: boolean;
  androidAvailable?: boolean;
}) {
  const landingMode = process.env.WEB_MODE === "landing";
  return (
    <footer className="landing-footer">
      <div className="landing-footer-inner">
        <nav
          className="landing-footer-install"
          aria-label="Приложения"
          data-install-promotion
        >
          <h2>Приложения</h2>
          {androidAvailable ? (
            <a href="/apps/android/download">
              <AndroidIcon />
              <span>Скачать с сайта</span>
            </a>
          ) : (
            <span className="footer-download-pending" aria-disabled="true">
              <AndroidIcon />
              <span>APK скоро</span>
            </span>
          )}
          <AppStoreSoon store="rustore" />
          <AppStoreSoon store="google-play" />
          <AppStoreSoon store="appgallery" />
        </nav>
        <nav className="landing-footer-social" aria-label="Соцсети">
          <h2>Соцсети</h2>
          <a
            href="https://max.ru/id272385800008_biz"
            target="_blank"
            rel="noopener noreferrer"
          >
            <MessengerIcon brand="max" />
            <span>Наш канал в MAX</span>
          </a>
          <a
            href="https://vk.ru/heartbeat_ch"
            target="_blank"
            rel="noopener noreferrer"
          >
            <MessengerIcon brand="vk" />
            <span>Наш канал в VK</span>
          </a>
        </nav>
        <nav className="landing-footer-support" aria-label="Техподдержка">
          <h2>Техподдержка</h2>
          <a
            href="https://max.ru/id272385800008_1_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            <MessengerIcon brand="max" />
            <span>Техподдержка в MAX</span>
          </a>
          <a
            href="https://t.me/heartbeat_support_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            <MessengerIcon brand="telegram" />
            <span>Техподдержка в Telegram</span>
          </a>
          <Link href="/contacts">Вопросы и предложения</Link>
          <a href="mailto:info@heartbeat-checker.ru">
            <Icon name="mail" size={24} className="footer-link-icon" />
            <span>
              info@
              <wbr />
              heartbeat-checker.ru
            </span>
          </a>
        </nav>
        <nav className="landing-footer-documents" aria-label="Документы">
          <h2>Документы</h2>
          <Link href="/terms">Условия и оферта</Link>
          <Link href="/terms#payments">Оплата и возвраты</Link>
          <Link href="/privacy">Персональные данные</Link>
          <Link href="/cookies">Cookie</Link>
        </nav>
        <div className="landing-footer-bottom">
          <div className="landing-footer-main">
            <Brand />
            <p>Круглосуточный мониторинг сайтов и сервисов.</p>
          </div>
          {onHome && (
            <div className="landing-footer-contact">
              <Link href={landingMode ? "#how" : "/login"}>
                {landingMode ? "Как это работает" : "Личный кабинет"} <Arrow />
              </Link>
            </div>
          )}
          <FooterLegalNotice />
        </div>
      </div>
    </footer>
  );
}
export function Arrow() {
  return <Icon name="arrow-up-right" />;
}
export function Notice({
  children,
  success = false,
}: {
  children: React.ReactNode;
  success?: boolean;
}) {
  return (
    <div
      className={`notice ${success ? "success" : ""}`}
      role={success ? "status" : "alert"}
    >
      {children}
    </div>
  );
}
export function Spinner() {
  return <Icon name="loader" className="spinner" />;
}
export function Loading({ text = "Загрузка…" }: { text?: string }) {
  return (
    <div className="loading" role="status">
      <Spinner />
      {text}
    </div>
  );
}
export function dateTime(
  value: string | null | undefined,
  { year = false }: { year?: boolean } = {},
) {
  return value
    ? new Intl.DateTimeFormat("ru", {
        day: "2-digit",
        month: "short",
        year: year ? "numeric" : undefined,
        hour: "2-digit",
        minute: "2-digit",
      }).format(new Date(value))
    : "—";
}
