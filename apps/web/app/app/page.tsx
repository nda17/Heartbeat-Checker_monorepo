import { Suspense } from "react";
import { MonitorsPage } from "@/_pages/monitors";
import { Loading } from "@/shared/ui";

export default function Page() {
  return (
    <Suspense fallback={<Loading text="Загружаем мониторы…" />}>
      <MonitorsPage />
    </Suspense>
  );
}
