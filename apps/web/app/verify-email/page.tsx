import { AuthPage } from "@/_pages/auth";
import { redirect } from "next/navigation";
export default async function Page({
  searchParams,
}: {
  searchParams: Promise<{ oauth?: string }>;
}) {
  if (process.env.WEB_MODE === "landing") redirect("/");
  return (
    <AuthPage
      mode="verify"
      oauthRegistration={(await searchParams).oauth === "1"}
    />
  );
}
