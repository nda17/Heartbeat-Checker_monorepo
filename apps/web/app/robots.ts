import type { MetadataRoute } from "next";
import { publicOrigin } from "@/shared/seo";

export default function robots(): MetadataRoute.Robots {
  return {
    // Auth and cabinet stay crawlable so bots can read their noindex directives.
    rules: {
      userAgent: "*",
      allow: "/",
      disallow: ["/api/", "/downloads/", "/.well-known/"],
    },
    sitemap: publicOrigin + "/sitemap.xml",
  };
}
