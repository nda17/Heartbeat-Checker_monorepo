import type { Metadata } from "next";
import "@/_app/styles";
export const metadata: Metadata = {
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
        {/* Apply the saved theme before paint; this static same-origin script contains no user data. */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts */}
        <script src="/theme-init.js" />
      </head>
      <body>
        <a href="#main" className="skip-link">
          К содержимому
        </a>
        {children}
      </body>
    </html>
  );
}
