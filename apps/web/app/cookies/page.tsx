import { CookiesPage } from "@/_pages/legal";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/cookies");
export default function Page() {
  return (
    <>
      <PublicPageStructuredData path="/cookies" />
      <CookiesPage />
    </>
  );
}
