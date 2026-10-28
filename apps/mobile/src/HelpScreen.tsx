import React, { useRef, useState } from "react";
import { BackHandler, Pressable, StyleSheet, Text, View } from "react-native";
import { errorText, ORIGIN } from "./api";
import { native } from "./native";
import {
  Button,
  Card,
  ErrorNotice,
  Icon,
  Label,
  Notice,
  Page,
  TextAction,
  Title,
  usePalette,
} from "./ui";

const questions = [
  [
    "Что считается одним монитором?",
    "Один монитор проверяет одну цель: адрес сайта или API, TCP-порт либо ICMP ping. Метод, ожидаемый код и текст — настройки этой проверки. Отдельный монитор для того же адреса считается отдельно. Проверки по расписанию входят в стоимость.",
  ],
  [
    "Как быстро обнаружится сбой?",
    "Вы выбираете интервал 1–10 минут и порог 1–10 ошибок подряд. По умолчанию — 5 минут и 2 ошибки. Время обнаружения зависит от расписания и порога, а доставка уведомления — ещё и от канала. Интервал не является обещанием срока уведомления.",
  ],
  [
    "Сколько хранится история?",
    "Подробные результаты проверок хранятся 7 дней. Закрытые инциденты — 90 дней после завершения; открытые сохраняются. История доступна в пределах этих сроков, в том числе при приостановленных проверках.",
  ],
  [
    "Чем UNKNOWN отличается от DOWN?",
    "DOWN означает неуспешную проверку. UNKNOWN — отсутствие свежего результата, например при проблеме исполнителя. Это само по себе не означает недоступность сайта. Принятый результат UNKNOWN сбрасывает серию сбоев; простое отсутствие свежих данных её не сбрасывает.",
  ],
  [
    "Как приходят уведомления?",
    "Подключите email, MAX, Telegram (личные сообщения, группы и темы) или вебхук, если канал доступен в настройках. В приложении есть push-уведомления; доставка зависит от разрешения Android, выбранного провайдера и фоновых ограничений. Проверки и инциденты также видны внутри приложения.",
  ],
  [
    "Как устроены оплата и квота?",
    "Вы покупаете нужное количество мониторов на календарный месяц. Каждый пакет имеет собственный срок; действующие квоты складываются. Продление пакета оформляется отдельно. Автоматических списаний и пробного периода нет. Итоговая сумма показывается до подтверждения заказа.",
  ],
  [
    "Освобождает ли пауза место в квоте?",
    "Да. Место занимает включённый монитор. Можно поставить его на паузу и включить другой, сохранив настройки и историю. Пауза не меняет размер и стоимость уже оплаченной квоты. Без действующей квоты проверки приостанавливаются; при её появлении возобновляются автоматически.",
  ],
] as const;

export function FAQ() {
  const c = usePalette();
  const [expanded, setExpanded] = useState<number | null>(null);
  return (
    <View style={styles.questions}>
      {questions.map(([question, answer], index) => (
        <View
          key={question}
          style={[styles.question, { borderBottomColor: c.border }]}
        >
          <Pressable
            accessibilityRole="button"
            accessibilityState={{ expanded: expanded === index }}
            onPress={() => setExpanded(expanded === index ? null : index)}
            style={({ pressed }) => [
              styles.questionButton,
              { opacity: pressed ? 0.65 : 1 },
            ]}
          >
            <Text style={[styles.questionTitle, { color: c.text }]}>
              {question}
            </Text>
            <Icon name={expanded === index ? "close" : "plus"} size={20} />
          </Pressable>
          {expanded === index ? (
            <View style={styles.answer}>
              <Label muted>{answer}</Label>
            </View>
          ) : null}
        </View>
      ))}
    </View>
  );
}

export function SupportLinks({ compact = false }: { compact?: boolean }) {
  const c = usePalette();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const pending = useRef(false);
  const open = async (label: string, url: string) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(label);
    setError("");
    try {
      await native.openCustomTab(url);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      pending.current = false;
      setBusy(null);
    }
  };
  const links = [
    ["Поддержка в Telegram", "https://t.me/heartbeat_support_bot", "telegram"],
    ["Поддержка в MAX", "https://max.ru/id272385800008_1_bot", "max"],
    ["Условия и публичная оферта", `${ORIGIN}/terms`, "arrow-up-right"],
    ["Политика конфиденциальности", `${ORIGIN}/privacy`, "arrow-up-right"],
    ["Контакты и реквизиты", `${ORIGIN}/contacts`, "arrow-up-right"],
    ["Политика cookie", `${ORIGIN}/cookies`, "arrow-up-right"],
  ] as const;
  return (
    <View style={styles.support}>
      {!compact ? (
        <>
          <Title>Нужна помощь?</Title>
          <Label muted>
            Вопросы оплаты, возврата и персональных данных направляйте на email.
            Укажите email аккаунта и номер заказа, если он есть.
          </Label>
          <Text selectable style={[styles.email, { color: c.accentText }]}>
            info@heartbeat-checker.ru
          </Text>
          <Notice message="Не передавайте пароль, коды подтверждения, полный номер карты или её защитный код." />
        </>
      ) : null}
      <ErrorNotice message={error} />
      <View style={compact ? styles.compactLinks : styles.support}>
        {links
          .filter(([, url]) => !compact || url !== `${ORIGIN}/cookies`)
          .map(([label, url, icon]) =>
            compact ? (
              <TextAction
                key={url}
                label={
                  busy === label
                    ? "Открываем…"
                    : label
                        .replace("Поддержка в ", "")
                        .replace("Условия и публичная оферта", "Условия")
                        .replace(
                          "Политика конфиденциальности",
                          "Конфиденциальность",
                        )
                        .replace("Контакты и реквизиты", "Контакты")
                }
                onPress={() => void open(label, url)}
                disabled={!!busy}
                iconName={icon}
              />
            ) : (
              <Button
                key={url}
                label={label}
                onPress={() => void open(label, url)}
                secondary
                iconName={icon}
                disabled={!!busy}
                loading={busy === label}
                loadingLabel="Открываем…"
              />
            ),
          )}
      </View>
    </View>
  );
}

export function HelpScreen({ onBack }: { onBack: () => void }) {
  React.useEffect(() => {
    const listener = BackHandler.addEventListener("hardwareBackPress", () => {
      onBack();
      return true;
    });
    return () => listener.remove();
  }, [onBack]);
  return (
    <Page title="Помощь и документы">
      <TextAction label="Назад" onPress={onBack} iconName="arrow-left" />
      <Card>
        <Title>Как пользоваться сервисом</Title>
        <FAQ />
      </Card>
      <Card>
        <SupportLinks />
      </Card>
    </Page>
  );
}

const styles = StyleSheet.create({
  questions: { gap: 0 },
  question: { borderBottomWidth: StyleSheet.hairlineWidth },
  questionButton: {
    minHeight: 56,
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    paddingVertical: 14,
  },
  questionTitle: { flex: 1, fontSize: 16, lineHeight: 23, fontWeight: "600" },
  answer: { paddingBottom: 16 },
  support: { gap: 10 },
  compactLinks: {
    flexDirection: "row",
    flexWrap: "wrap",
    columnGap: 16,
    rowGap: 0,
  },
  email: {
    fontSize: 16,
    lineHeight: 24,
    fontWeight: "600",
    paddingVertical: 6,
  },
});
