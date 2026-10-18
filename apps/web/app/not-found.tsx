import Link from "next/link";
export default function NotFound() {
  const landingMode = process.env.WEB_MODE === "landing";
  return (
    <main id="main" className="standalone">
      <p className="eyebrow">404 / СТРАНИЦА НЕ НАЙДЕНА</p>
      <h1>Здесь пока ничего нет</h1>
      <p>
        {landingMode
          ? "Проверьте адрес или вернитесь на главную."
          : "Проверьте адрес или вернитесь к мониторам."}
      </p>
      <Link className="button" href={landingMode ? "/" : "/app"}>
        {landingMode ? "На главную" : "Открыть кабинет"}
      </Link>
    </main>
  );
}
