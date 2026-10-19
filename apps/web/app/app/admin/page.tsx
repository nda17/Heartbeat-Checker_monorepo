import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Администрирование",
  robots: { index: false, follow: false, noarchive: true },
};

export { AdminPage as default } from "@/_pages/admin";
