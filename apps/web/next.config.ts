import type { NextConfig } from "next";
const apiOrigin = new URL(process.env.GO_API_ORIGIN ?? "http://127.0.0.1:8080");
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
if (!local && (!siteKey || /^[123]x0/.test(siteKey))) {
  throw new Error(
    "Public deployment requires a real NEXT_PUBLIC_TURNSTILE_SITE_KEY",
  );
}
if (
  !["http:", "https:"].includes(apiOrigin.protocol) ||
  apiOrigin.username ||
  apiOrigin.password ||
  apiOrigin.pathname !== "/" ||
  apiOrigin.search ||
  apiOrigin.hash
) {
  throw new Error(
    "GO_API_ORIGIN must be an HTTP origin without credentials or a path",
  );
}
const config: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  async rewrites() {
    return [
      {
        source: "/api/v1/:path*",
        destination: `${apiOrigin.origin}/api/v1/:path*`,
      },
    ];
  },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "Referrer-Policy", value: "no-referrer" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "X-Frame-Options", value: "DENY" },
          {
            key: "Permissions-Policy",
            value: "camera=(), microphone=(), geolocation=()",
          },
        ],
      },
    ];
  },
};
export default config;
