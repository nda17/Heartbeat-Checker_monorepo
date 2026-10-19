import { connection } from "next/server";
import { readPublicPricing } from "@/entities/billing/server";
import { TermsPage } from "@/_pages/legal";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/terms");
export default async function Page() {
  await connection();
  const initialPricing = await readPublicPricing();
  return (
    <>
      <PublicPageStructuredData path="/terms" />
      <TermsPage initialPricing={initialPricing} />
    </>
  );
}
