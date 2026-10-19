import { PrivacyPage } from "@/_pages/legal";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/privacy");
export default function Page() {
  return (
    <>
      <PublicPageStructuredData path="/privacy" />
      <PrivacyPage />
    </>
  );
}
