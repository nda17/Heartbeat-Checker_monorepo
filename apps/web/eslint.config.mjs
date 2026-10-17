import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";
const layers = ["_app", "_pages", "widgets", "features", "entities", "shared"];
const sourceRoot = fileURLToPath(new URL("src/", import.meta.url));
const boundaries = {
  meta: { type: "problem", schema: [] },
  create(context) {
    return {
      ImportDeclaration(node) {
        const path = node.source.value;
        if (typeof path !== "string") return;
        const isAlias = path.startsWith("@/");
        if (!isAlias && !path.startsWith(".")) return;
        const target = (
          isAlias
            ? path.slice(2)
            : relative(sourceRoot, resolve(dirname(context.filename), path))
        )
          .replaceAll("\\", "/")
          .split("/");
        const source = relative(sourceRoot, context.filename)
          .replaceAll("\\", "/")
          .split("/");
        if (!layers.includes(target[0])) return;
        const sameSlice = source[0] === target[0] && source[1] === target[1];
        if (sameSlice && !isAlias) return;
        if (
          layers.includes(source[0]) &&
          layers.indexOf(target[0]) <= layers.indexOf(source[0]) &&
          !(target[0] === "shared" && source[0] === "shared")
        ) {
          context.report({
            node,
            message:
              "FSD imports must point to lower layers; use relative imports inside a slice.",
          });
        }
        if (target.length > 2)
          context.report({
            node,
            message: "Import a slice through its public index.",
          });
      },
    };
  },
};
export default defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    plugins: { fsd: { rules: { boundaries } } },
    rules: { "fsd/boundaries": "error" },
  },
  globalIgnores([".next/**", "out/**", "next-env.d.ts"]),
]);
