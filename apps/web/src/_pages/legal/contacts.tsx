import Link from "next/link";
import { LegalPage } from "./layout";

export function ContactsPage() {
  return (
    <LegalPage
      title="Контакты и реквизиты"
      description="Сведения об исполнителе и способы связи по вопросам работы Heartbeat Checker."
    >
      <section>
        <h2>Поддержка Heartbeat Checker</h2>
        <p>
          Электронная почта:{" "}
          <a href="mailto:info@heartbeat-checker.ru">
            info@heartbeat-checker.ru
          </a>
          .
        </p>
        <p>
          Сайт: <Link href="/">heartbeat-checker.ru</Link>.
        </p>
        <p>
          Боты поддержки в мессенджерах:{" "}
          <a
            href="https://t.me/heartbeat_support_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            Telegram
          </a>{" "}
          и{" "}
          <a
            href="https://max.ru/id272385800008_1_bot"
            target="_blank"
            rel="noopener noreferrer"
          >
            MAX
          </a>
          .
        </p>
        <p>
          По вопросам работы сайта и подписки обращайтесь по электронной почте
          или через ботов поддержки. Вопросы платежей, возвратов и персональных
          данных направляйте на info@heartbeat-checker.ru, чтобы обращение можно
          было связать с вашим заказом или аккаунтом. Не присылайте пароль, коды
          подтверждения, полный номер банковской карты или её защитный код.
        </p>
      </section>
      <section>
        <h2>Исполнитель и оператор персональных данных</h2>
        <p>Индивидуальный предприниматель Нефёдов Дмитрий Александрович.</p>
        <p>ИНН 272385800008 · ОГРНИП 326508100524538.</p>
        <p>
          Оплачивайте услугу только при оформлении заказа на сайте. Не
          переводите деньги по реквизитам или сообщениям, которые не относятся к
          оформленному заказу.
        </p>
        <p>
          Условия предоставления услуги, оплаты и возвратов изложены на странице{" "}
          <Link href="/terms">«Условия использования и публичная оферта»</Link>.
          Обработка персональных данных описана в{" "}
          <Link href="/privacy">соответствующей политике</Link>.
        </p>
      </section>
    </LegalPage>
  );
}
