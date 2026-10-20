import type { Metadata } from "next";

// Canonical URLs identify the public site, including local and preview builds.
export const publicOrigin = "https://heartbeat-checker.ru";
export const publicPagePaths = [
  "/",
  "/android",
  "/contacts",
  "/terms",
  "/privacy",
  "/cookies",
] as const;
type PublicPagePath = (typeof publicPagePaths)[number];
const pages: Record<PublicPagePath, { title: string; description: string }> = {
  "/": {
    title: "Heartbeat Checker — мониторинг сайтов и серверов",
    description:
      "Мониторинг сайтов и API по HTTP/HTTPS, открытых TCP-портов и серверов по ICMP. Отслеживайте сбои, восстановление и историю в Heartbeat Checker.",
  },
  "/android": {
    title: "Heartbeat Checker для Android — мониторинг сайтов",
    description:
      "Приложение Heartbeat Checker для Android: кабинет мониторинга сайтов и API, история проверок и инцидентов, настройки мониторов и уведомлений.",
  },
  "/contacts": {
    title: "Контакты и реквизиты · Heartbeat Checker",
    description:
      "Контакты поддержки Heartbeat Checker, email для обращений и реквизиты исполнителя сервиса мониторинга сайтов и API.",
  },
  "/terms": {
    title: "Условия использования и оферта · Heartbeat Checker",
    description:
      "Условия использования Heartbeat Checker: мониторинг сайтов и API, платное обслуживание, порядок оплаты, возвратов и обращения в поддержку.",
  },
  "/privacy": {
    title: "Политика обработки персональных данных · Heartbeat Checker",
    description:
      "Как Heartbeat Checker обрабатывает персональные данные посетителей и пользователей, обращения в поддержку и запросы по вопросам данных.",
  },
  "/cookies": {
    title: "Cookie и локальное хранилище · Heartbeat Checker",
    description:
      "Cookie авторизации и локальные настройки Heartbeat Checker: назначение, сроки хранения и управление данными сайта в браузере.",
  },
};

export function publicPageUrl(path: PublicPagePath) {
  return path === "/" ? publicOrigin : publicOrigin + path;
}

export function publicPageMetadata(path: PublicPagePath): Metadata {
  const page = pages[path];
  const url = publicPageUrl(path);
  const image = {
    url: publicOrigin + "/og",
    width: 1200,
    height: 630,
    alt: "Heartbeat Checker — мониторинг сайтов и API",
  };
  return {
    title: { absolute: page.title },
    description: page.description,
    alternates: { canonical: url },
    robots: { index: true, follow: true },
    openGraph: {
      type: "website",
      locale: "ru_RU",
      siteName: "Heartbeat Checker",
      url,
      title: page.title,
      description: page.description,
      images: [image],
    },
    twitter: {
      card: "summary_large_image",
      title: page.title,
      description: page.description,
      images: [image],
    },
  };
}

export function PublicPageStructuredData({
  path,
  android,
}: {
  path: PublicPagePath;
  android?: {
    versionName: string;
    downloadUrl: string;
    sizeBytes: number;
    releasedAt: string;
  };
}) {
  const page = pages[path];
  const url = publicPageUrl(path);
  const websiteId = publicOrigin + "/#website";
  const applicationId = publicOrigin + "/android#application";
  const data = {
    "@context": "https://schema.org",
    "@graph": [
      {
        "@type": "WebSite",
        "@id": websiteId,
        url: publicOrigin,
        name: "Heartbeat Checker",
        inLanguage: "ru",
      },
      {
        "@type": path === "/contacts" ? "ContactPage" : "WebPage",
        "@id": url + "#webpage",
        url,
        name: page.title,
        description: page.description,
        inLanguage: "ru",
        isPartOf: { "@id": websiteId },
        ...(android && path === "/android"
          ? { mainEntity: { "@id": applicationId } }
          : {}),
      },
      ...(android && path === "/android"
        ? [
            {
              "@type": "SoftwareApplication",
              "@id": applicationId,
              name: "Heartbeat Checker",
              url,
              operatingSystem: "Android 10+",
              applicationCategory: "UtilitiesApplication",
              softwareVersion: android.versionName,
              downloadUrl: android.downloadUrl,
              fileSize: `${android.sizeBytes} bytes`,
              datePublished: android.releasedAt,
            },
          ]
        : []),
    ],
  };
  return (
    <script
      type="application/ld+json"
      dangerouslySetInnerHTML={{
        __html: JSON.stringify(data).replace(/</g, "\\u003c"),
      }}
    />
  );
}
