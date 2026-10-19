import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { ImageResponse } from "next/og";

export const dynamic = "force-static";

// Generated at build time from the local OFL font and original brand SVG.
export async function GET() {
  const font = await readFile(
    join(process.cwd(), "public/fonts/golos-text-og.ttf"),
  );
  return new ImageResponse(
    <div
      style={{
        width: "100%",
        height: "100%",
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        backgroundColor: "#101b23",
        color: "#f4f8f7",
        padding: "64px 76px",
        fontFamily: "Golos OG",
      }}
    >
      <div
        style={{ display: "flex", alignItems: "center", gap: 22, fontSize: 32 }}
      >
        <svg width="64" height="64" viewBox="0 0 64 64" fill="none">
          <rect width="64" height="64" rx="18" fill="#007f73" />
          <path
            d="M14 32h8l5-12 9 24 5-12h9"
            stroke="#fff"
            strokeWidth="4"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
        <span>Heartbeat Checker</span>
      </div>
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          fontSize: 84,
          fontWeight: 400,
          lineHeight: 1.1,
        }}
      >
        <span>Мониторинг сайтов</span>
        <span style={{ color: "#75ddc2" }}>и сервисов</span>
      </div>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          fontSize: 23,
          color: "#bdccc9",
        }}
      >
        <span>Доступность · Время ответа · Инциденты</span>
        <span>heartbeat-checker.ru</span>
      </div>
    </div>,
    {
      width: 1200,
      height: 630,
      headers: { "Cache-Control": "public, max-age=86400" },
      fonts: [{ name: "Golos OG", data: font, weight: 400, style: "normal" }],
    },
  );
}
