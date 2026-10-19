// Keep old links on the same origin; object-store URLs are never exposed here.
function redirect() {
  return new Response(null, {
    status: 307,
    headers: {
      Location: "/apps/android/download",
      "Cache-Control": "no-store",
    },
  });
}
export function GET() {
  return redirect();
}
export function HEAD() {
  return redirect();
}
