import type { Metadata } from "next";
import "@/_app/styles";
import { publicOrigin } from "@/shared/seo";
import { CookieNotice } from "@/features/cookie-notice";
export const metadata: Metadata = {
  metadataBase: new URL(publicOrigin),
  title: {
    default: "Heartbeat Checker — мониторинг сайтов",
    template: "%s · Heartbeat Checker",
  },
  description:
    "Проверяйте доступность HTTP-сервисов и разбирайте инциденты в одном кабинете.",
  robots: { index: false, follow: false },
};
export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="ru" suppressHydrationWarning>
      <head>
        {/* Apply the theme and installed-app UI before paint using a static same-origin script. */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts */}
        <script src="/theme-init.js" />
      </head>
      <body>
        <a href="#main" className="skip-link">
          К содержимому
        </a>
        {children}
        <CookieNotice />
      </body>
    </html>
  );
}
