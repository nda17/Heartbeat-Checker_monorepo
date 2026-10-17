import { cp } from "node:fs/promises";
import { fileURLToPath } from "node:url";

// Standalone output intentionally leaves static assets separate; Docker copies
// them in its image, while this local command prepares them before starting.
const root = new URL("../", import.meta.url);
const standalone = new URL(".next/standalone/", root);
await cp(
  fileURLToPath(new URL(".next/static/", root)),
  fileURLToPath(new URL(".next/static/", standalone)),
  { recursive: true },
);
await cp(
  fileURLToPath(new URL("public/", root)),
  fileURLToPath(new URL("public/", standalone)),
  { recursive: true },
);
process.env.HOSTNAME = "127.0.0.1";
await import(new URL("server.js", standalone).href);
