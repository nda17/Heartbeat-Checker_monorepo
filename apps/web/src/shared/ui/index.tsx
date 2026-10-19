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
    // Simple Icons (CC0): https://simpleicons.org/?q=googleplay
    return (
      <svg
        width="24"
        height="24"
        viewBox="0 0 24 24"
        aria-hidden="true"
        focusable="false"
      >
        <path
          fill="currentColor"
          d="M22.018 13.298l-3.919 2.218-3.515-3.493 3.543-3.521 3.891 2.202a1.49 1.49 0 0 1 0 2.594zM1.337.924a1.486 1.486 0 0 0-.112.568v21.017c0 .217.045.419.124.6l11.155-11.087L1.337.924zm12.207 10.065l3.258-3.238L3.45.195a1.466 1.466 0 0 0-.946-.179l11.04 10.973zm0 2.067l-11 10.933c.298.036.612-.016.906-.183l13.324-7.54-3.23-3.21z"
        />
      </svg>
    );
  }
  if (store === "appgallery") {
    // Simple Icons (CC0): https://simpleicons.org/?q=appgallery
    return (
      <svg
        width="24"
        height="24"
        viewBox="0 0 24 24"
        aria-hidden="true"
        focusable="false"
      >
        <path
          fill="currentColor"
          d="M6.734 0C1.804 0 0 1.803 0 6.733v10.535C0 22.198 1.803 24 6.734 24h10.529C22.193 24 24 22.197 24 17.268V6.733C24 1.803 22.197 0 17.266 0zM8 4h.565A3.439 3.439 0 0 0 12 7.435 3.439 3.439 0 0 0 15.435 4H16c0 2.206-1.794 4-4 4S8 6.206 8 4zm6.468 8h.52l.713 2.16.696-2.158h.66l-1.092 3.14h-.532l-.714-2.063-.714 2.063h-.528l-1.095-3.14h.678l.694 2.158zm6.236 0h.629v3.138h-.629zM2.666 12h.638v1.267h1.439V12h.637v3.142h-.637v-1.276h-1.44v1.276h-.637zm5.668 0h.637v1.772c0 .9-.496 1.417-1.36 1.417-.856 0-1.347-.507-1.347-1.39v-1.797H6.9v1.775c0 .524.255.805.719.805.46 0 .714-.273.714-.784zm2.344 0h.563l1.378 3.14h-.668l-.282-.654H10.23l-.286.654h-.651zm6.893.002h2.312v.572H18.2v.643h1.16v.573H18.2v.777h1.744v.573H17.57zm-6.623.793-.48 1.124h.964z"
        />
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
  // Simple Icons (CC0): https://simpleicons.org/?q=android
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
        fill="currentColor"
        d="M18.4395 5.5586c-.675 1.1664-1.352 2.3318-2.0274 3.498-.0366-.0155-.0742-.0286-.1113-.043-1.8249-.6957-3.484-.8-4.42-.787-1.8551.0185-3.3544.4643-4.2597.8203-.084-.1494-1.7526-3.021-2.0215-3.4864a1.1451 1.1451 0 0 0-.1406-.1914c-.3312-.364-.9054-.4859-1.379-.203-.475.282-.7136.9361-.3886 1.5019 1.9466 3.3696-.0966-.2158 1.9473 3.3593.0172.031-.4946.2642-1.3926 1.0177C2.8987 12.176.452 14.772 0 18.9902h24c-.119-1.1108-.3686-2.099-.7461-3.0683-.7438-1.9118-1.8435-3.2928-2.7402-4.1836a12.1048 12.1048 0 0 0-2.1309-1.6875c.6594-1.122 1.312-2.2559 1.9649-3.3848.2077-.3615.1886-.7956-.0079-1.1191a1.1001 1.1001 0 0 0-.8515-.5332c-.5225-.0536-.9392.3128-1.0488.5449zm-.0391 8.461c.3944.5926.324 1.3306-.1563 1.6503-.4799.3197-1.188.0985-1.582-.4941-.3944-.5927-.324-1.3307.1563-1.6504.4727-.315 1.1812-.1086 1.582.4941zM7.207 13.5273c.4803.3197.5506 1.0577.1563 1.6504-.394.5926-1.1038.8138-1.584.4941-.48-.3197-.5503-1.0577-.1563-1.6504.4008-.6021 1.1087-.8106 1.584-.4941z"
      />
    </svg>
  );
}
function MessengerIcon({ brand }: { brand: "vk" | "max" | "telegram" }) {
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
            id="footer-telegram-gradient"
            x1="50%"
            y1="0%"
            x2="50%"
            y2="99.2583404%"
          >
            <stop stopColor="#2AABEE" offset="0%" />
            <stop stopColor="#229ED9" offset="100%" />
          </linearGradient>
        </defs>
        <circle
          fill="url(#footer-telegram-gradient)"
          cx="500"
          cy="500"
          r="500"
        />
        <path
          d="M226.328419,494.722069 C372.088573,431.216685 469.284839,389.350049 517.917216,369.122161 C656.772535,311.36743 685.625481,301.334815 704.431427,301.003532 C708.567621,300.93067 717.815839,301.955743 723.806446,306.816707 C728.864797,310.92121 730.256552,316.46581 730.922551,320.357329 C731.588551,324.248848 732.417879,333.113828 731.758626,340.040666 C724.234007,419.102486 691.675104,610.964674 675.110982,699.515267 C668.10208,736.984342 654.301336,749.547532 640.940618,750.777006 C611.904684,753.448938 589.856115,731.588035 561.733393,713.153237 C517.726886,684.306416 492.866009,666.349181 450.150074,638.200013 C400.78442,605.66878 432.786119,587.789048 460.919462,558.568563 C468.282091,550.921423 596.21508,434.556479 598.691227,424.000355 C599.00091,422.680135 599.288312,417.758981 596.36474,415.160431 C593.441168,412.561881 589.126229,413.450484 586.012448,414.157198 C581.598758,415.158943 511.297793,461.625274 375.109553,553.556189 C355.154858,567.258623 337.080515,573.934908 320.886524,573.585046 C303.033948,573.199351 268.692754,563.490928 243.163606,555.192408 C211.851067,545.013936 186.964484,539.632504 189.131547,522.346309 C190.260287,513.342589 202.659244,504.134509 226.328419,494.722069 Z"
          fill="#FFFFFF"
        />
      </svg>
    );
  }
  // VK: https://github.com/VKCOM/icons — logo_vk_24.svg
  // MAX: https://go.max.ru/brandbook — official monochrome silhouette.
  return (
    <svg
      className="footer-link-icon"
      width="24"
      height="24"
      viewBox={brand === "vk" ? "0 0 24 24" : "0 0 100 100"}
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      {brand === "vk" ? (
        <path
          clipRule="evenodd"
          d="m4.2653 4.2653c-1.2653 1.2653-1.2653 3.30177-1.2653 7.3747v.72c0 4.0729 0 6.1094 1.2653 7.3747s3.30176 1.2653 7.3747 1.2653h.72c4.0729 0 6.1094 0 7.3747-1.2653s1.2653-3.3018 1.2653-7.3747v-.72c0-4.07293 0-6.1094-1.2653-7.3747s-3.3018-1.2653-7.3747-1.2653h-.72c-4.07294 0-6.1094 0-7.3747 1.2653zm1.7347 4.2347c.09636 4.6823 2.55576 7.5 6.6095 7.5h.2351v-2.6787c1.4764.1499 2.5773 1.2536 3.0268 2.6787h2.1286c-.5775-2.1318-2.0747-3.3105-3.0052-3.7608.9298-.5569 2.2458-1.9071 2.5557-3.7392h-1.9365c-.4064 1.48991-1.6152 2.8401-2.7701 2.9683v-2.9683h-1.9675v5.1967c-1.19737-.2997-2.75931-1.7572-2.82331-5.1967z"
          fill="currentColor"
          fillRule="evenodd"
        />
      ) : (
        <path
          fill="currentColor"
          d="M50.7571 0.261719C78.2929 0.261719 99.8857 22.5974 99.8857 50.1474C99.8857 77.6974 77.6071 99.4903 51.0214 99.4903C41.5857 99.4903 37.0143 98.1617 29.65 92.9474C29.1429 92.5903 28.45 92.6831 28.0214 93.1403C22.3571 99.1831 7.85 103.426 7.18571 95.176C7.18571 80.7903 0 71.4474 0 49.876C0 21.5546 23.2214 0.261719 50.7571 0.261719ZM51.5286 24.8117C38.4643 24.126 28.2643 33.1974 26.0143 47.3831C24.15 59.1332 27.45 73.4546 30.2786 74.176C31.4786 74.4832 34.3571 72.276 36.4571 70.2974C36.85 69.926 37.45 69.8617 37.9071 70.1474C41.1786 72.1474 44.8786 73.6474 48.9571 73.8617C62.3714 74.5617 74.2571 64.0617 74.9643 50.6474C75.6643 37.2331 64.9429 25.5046 51.5286 24.8046V24.8117Z"
        />
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
          aria-label="Установка приложения"
          data-install-promotion
        >
          <h2>Установка приложения</h2>
          {androidAvailable ? (
            <a href="/downloads/android">
              <AndroidIcon />
              <span>Скачать .APK по прямой ссылке</span>
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
          <Link href="/contacts">Контакты</Link>
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
export function dateTime(value: string | null | undefined) {
  return value
    ? new Intl.DateTimeFormat("ru", {
        day: "2-digit",
        month: "short",
        hour: "2-digit",
        minute: "2-digit",
      }).format(new Date(value))
    : "—";
}
