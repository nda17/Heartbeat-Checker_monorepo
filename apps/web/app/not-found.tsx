import Link from "next/link";
export default function NotFound() {
  return (
    <main id="main" className="standalone">
      <p className="eyebrow">404 / СТРАНИЦА НЕ НАЙДЕНА</p>
      <h1>Здесь пока ничего нет</h1>
      <p>Проверьте адрес или вернитесь к мониторам.</p>
      <Link className="button" href="/app">
        Открыть кабинет
      </Link>
    </main>
  );
}
