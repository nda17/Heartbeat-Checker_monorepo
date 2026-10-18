export const dynamic = "force-dynamic";

function unavailable() {
  if (process.env.WEB_MODE !== "frontend") {
    return new Response(null, {
      status: 404,
      headers: { "Cache-Control": "no-store" },
    });
  }

  return Response.json(
    { error: "temporarily_unavailable" },
    { status: 503, headers: { "Cache-Control": "no-store" } },
  );
}

function unavailableHead() {
  return new Response(null, {
    status: process.env.WEB_MODE === "frontend" ? 503 : 404,
    headers: { "Cache-Control": "no-store" },
  });
}

export {
  unavailable as GET,
  unavailable as POST,
  unavailable as PUT,
  unavailable as PATCH,
  unavailable as DELETE,
  unavailable as OPTIONS,
  unavailableHead as HEAD,
};
