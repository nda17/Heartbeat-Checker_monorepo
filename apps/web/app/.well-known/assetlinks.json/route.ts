import { androidRelease } from "@/shared/android";
export function GET() {
  const release = androidRelease();
  return Response.json(
    release
      ? [
          {
            relation: ["delegate_permission/common.handle_all_urls"],
            target: {
              namespace: "android_app",
              package_name: release.packageName,
              sha256_cert_fingerprints: [release.signingCertificateSha256],
            },
          },
        ]
      : [],
    { headers: { "Cache-Control": "public, max-age=300" } },
  );
}
