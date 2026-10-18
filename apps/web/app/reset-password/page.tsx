import { AuthPage } from "@/_pages/auth";
import { redirect } from "next/navigation";
export default function Page() {
  if (process.env.WEB_MODE === "landing") redirect("/");
  return <AuthPage mode="reset-confirm" />;
}
