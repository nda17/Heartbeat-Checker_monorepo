import type { MetadataRoute } from "next";
export default function manifest(): MetadataRoute.Manifest {
  return {
    id: "/app",
    name: "Heartbeat Checker",
    short_name: "Heartbeat",
    description: "Мониторинг доступности сайтов и API",
    lang: "ru",
    start_url: "/app",
    scope: "/",
    display: "standalone",
    background_color: "#101b23",
    theme_color: "#101b23",
    icons: [
      {
        src: "/brand/avatar512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
    ],
  };
}
