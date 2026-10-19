import { TermsPage } from "@/_pages/legal";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/terms");
export default function Page() {
  return (
    <>
      <PublicPageStructuredData path="/terms" />
      <TermsPage />
    </>
  );
}
