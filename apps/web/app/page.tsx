import { connection } from "next/server";
import { readPublicPricing } from "@/entities/billing/server";
import { HomePage } from "@/_pages/home";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/");
export default async function Page() {
  await connection();
  const initialPricing = await readPublicPricing();
  return (
    <>
      <PublicPageStructuredData path="/" />
      <HomePage initialPricing={initialPricing} />
    </>
  );
}
