import { ContactsPage } from "@/_pages/legal";
import { publicPageMetadata, PublicPageStructuredData } from "@/shared/seo";

export const metadata = publicPageMetadata("/contacts");
export default function Page() {
  return (
    <>
      <PublicPageStructuredData path="/contacts" />
      <ContactsPage />
    </>
  );
}
