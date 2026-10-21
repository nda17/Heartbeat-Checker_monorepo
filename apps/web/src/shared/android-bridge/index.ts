const bridgePaths = new Set([
  "/mobile/challenge",
  "/android/auth/complete",
  "/android/auth/reset",
  "/android/auth/verify",
  "/android/payments/return",
]);

export function androidBridge(path: string): Response {
  if (!bridgePaths.has(path)) return new Response(null, { status: 404 });

  const siteKey = process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY ?? "";
  const unavailable = path === "/mobile/challenge" && !siteKey;
  const title =
    path === "/mobile/challenge"
      ? "Проверка безопасности"
      : "Возврат в приложение";
  const markup = `<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${title} · Heartbeat Checker</title><meta name="robots" content="noindex,nofollow">
<style>body{font-family:system-ui,sans-serif;margin:0;background:#101b28;color:#f4f8fc}main{max-width:480px;margin:12vh auto;padding:24px}h1{font-size:1.5rem}p{line-height:1.5}button,a{font:inherit;color:#fff}button{background:#2879c5;border:0;border-radius:8px;padding:12px 18px;cursor:pointer}a{display:inline-block;margin-top:20px}#widget{margin:22px 0}</style>
</head><body><main><h1>${title}</h1><p id="status" role="status">Загрузка…</p><div id="widget"></div><button id="open" type="button" hidden>Открыть приложение</button><button id="retry" type="button" hidden>Повторить проверку</button><a id="fallback" href="/android" hidden>Скачать приложение</a></main>
<script defer src="/android-bridge.js" data-site-key="${siteKey.replaceAll("&", "&amp;").replaceAll('"', "&quot;").replaceAll("<", "&lt;")}"></script>
${path === "/mobile/challenge" && !unavailable ? '<script defer src="https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit"></script>' : ""}
</body></html>`;

  return new Response(markup, {
    status: unavailable ? 503 : 200,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store, max-age=0",
      "Referrer-Policy": "no-referrer",
      "X-Robots-Tag": "noindex, nofollow",
      "X-Content-Type-Options": "nosniff",
      "Content-Security-Policy":
        "default-src 'none'; script-src 'self' https://challenges.cloudflare.com; style-src 'unsafe-inline'; frame-src https://challenges.cloudflare.com; connect-src https://challenges.cloudflare.com; img-src https://challenges.cloudflare.com data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
    },
  });
}
