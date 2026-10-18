import Link from "next/link";
import { ThemeToggle } from "@/features/theme-toggle";
import { Brand, PublicFooter } from "@/shared/ui";

export function LegalPage({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <div className="landing legal-page">
      <header className="landing-header">
        <Brand />
        <nav aria-label="Главная навигация">
          <ThemeToggle />
          <Link href="/">На главную</Link>
        </nav>
      </header>
      <main id="main" className="legal-main">
        <p className="eyebrow">HEARTBEAT CHECKER / ДОКУМЕНТЫ</p>
        <h1>{title}</h1>
        <p className="legal-lead">{description}</p>
        <nav className="legal-nav" aria-label="Юридические страницы">
          <Link href="/terms">Условия и оферта</Link>
          <Link href="/terms#payments">Оплата и возвраты</Link>
          <Link href="/privacy">Персональные данные</Link>
          <Link href="/cookies">Cookie</Link>
          <Link href="/contacts">Контакты</Link>
        </nav>
        <article className="legal-content">{children}</article>
      </main>
      <PublicFooter />
    </div>
  );
}
