import { androidRelease } from "@/shared/android";
// A redirect only: no S3 DNS resolution, proxy, fetch, or readiness dependency.
export function GET() {
  const release = androidRelease();
  if (!release)
    return new Response("Подписанный APK пока не опубликован.", {
      status: 404,
      headers: { "Cache-Control": "no-store" },
    });
  return new Response(null, {
    status: 302,
    headers: { Location: release.downloadUrl, "Cache-Control": "no-store" },
  });
}
