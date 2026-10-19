import { HomePage } from "@/_pages/home";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/");
export default function Page() {
  return (
    <>
      <PublicPageStructuredData path="/" />
      <HomePage />
    </>
  );
}
