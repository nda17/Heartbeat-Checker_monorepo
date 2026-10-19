import type { NextConfig } from "next";
const webMode = process.env.WEB_MODE ?? "app";
if (webMode !== "app" && webMode !== "frontend" && webMode !== "landing") {
  throw new Error("WEB_MODE must be app, frontend, or landing");
}
if (webMode === "app" && !process.env.GO_API_ORIGIN) {
  throw new Error("App mode requires GO_API_ORIGIN");
}
if (webMode === "frontend" && process.env.GO_API_ORIGIN) {
  throw new Error("Frontend mode must not have a GO_API_ORIGIN");
}
const apiOrigin =
  webMode === "app" ? new URL(process.env.GO_API_ORIGIN!) : null;
const publicOrigin = new URL(
  process.env.WEB_PUBLIC_ORIGIN ?? "http://127.0.0.1:3000",
);
const local = ["127.0.0.1", "localhost", "[::1]"].includes(
  publicOrigin.hostname,
);
if (
  !["http:", "https:"].includes(publicOrigin.protocol) ||
  publicOrigin.username ||
  publicOrigin.password ||
  publicOrigin.pathname !== "/" ||
  publicOrigin.search ||
  publicOrigin.hash ||
  (!local && publicOrigin.protocol !== "https:")
) {
  throw new Error(
    "WEB_PUBLIC_ORIGIN must be a valid origin; public deployment requires HTTPS",
  );
}
const siteKey = process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY;
if (webMode !== "landing" && !local && (!siteKey || /^[123]x0/.test(siteKey))) {
  throw new Error(
    "Public deployment requires a real NEXT_PUBLIC_TURNSTILE_SITE_KEY",
  );
}
if (
  apiOrigin &&
  (!["http:", "https:"].includes(apiOrigin.protocol) ||
    apiOrigin.username ||
    apiOrigin.password ||
    apiOrigin.pathname !== "/" ||
    apiOrigin.search ||
    apiOrigin.hash)
) {
  throw new Error(
    "GO_API_ORIGIN must be an HTTP origin without credentials or a path",
  );
}
const config: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  env: { WEB_MODE: webMode },
  async rewrites() {
    return {
      beforeFiles: apiOrigin
        ? [
            {
              source: "/api/v1/:path*",
              destination: `${apiOrigin.origin}/api/v1/:path*`,
            },
          ]
        : [],
      afterFiles: [],
      fallback: [],
    };
  },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "Referrer-Policy", value: "no-referrer" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "X-Frame-Options", value: "DENY" },
          { key: "X-Robots-Tag", value: "noindex, nofollow" },
          {
            key: "Permissions-Policy",
            value: "camera=(), microphone=(), geolocation=()",
          },
        ],
      },
      ...["/", "/android", "/contacts", "/terms", "/privacy", "/cookies"].map(
        (source) => ({
          source,
          headers: [{ key: "X-Robots-Tag", value: "index, follow" }],
        }),
      ),
      // Discovery files must not inherit the private-page noindex default.
      ...["/robots.txt", "/sitemap.xml"].map((source) => ({
        source,
        headers: [{ key: "X-Robots-Tag", value: "index, follow" }],
      })),
    ];
  },
};
export default config;
