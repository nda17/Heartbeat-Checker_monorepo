import type { MetadataRoute } from "next";
import { publicPagePaths, publicPageUrl } from "@/shared/seo";

export default function sitemap(): MetadataRoute.Sitemap {
  return publicPagePaths.map((path) => ({ url: publicPageUrl(path) }));
}
