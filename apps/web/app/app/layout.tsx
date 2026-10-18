import { ConsoleShell } from "@/widgets/console-shell";
import { redirect } from "next/navigation";
export default function Layout({ children }: { children: React.ReactNode }) {
  if (process.env.WEB_MODE === "landing") redirect("/");
  return <ConsoleShell>{children}</ConsoleShell>;
}
