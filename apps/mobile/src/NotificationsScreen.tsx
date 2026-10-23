import React, { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Switch, View } from "react-native";
import { ApiError, api, errorText, sessionEpoch } from "./api";
import { native } from "./native";
import type { Monitor } from "./types";
import {
  Button,
  Card,
  ErrorNotice,
  Field,
  Icon,
  Label,
  Page,
  Title,
  type IconName,
} from "./ui";

type Destination = {
  id: string;
  provider: "email" | "telegram" | "max";
  kind: string;
  label: string;
  ready: boolean;
  version: number;
  email?: string;
};
type Route = {
  provider: string;
  destinationId: string;
  enabled: boolean;
  reminderMinutes: number;
  version: number;
};
type Webhook = { id: string; url: string; enabled: boolean; version: number };
type Push = { enabled: boolean; version: number };
type Link = {
  id: string;
  status: string;
  errorCode?: string;
  expiresAt: string;
  provider: string;
  kind: string;
  candidate: {
    label: string;
    chatId: string;
    userId: string;
    threadId: string | null;
  } | null;
};
type Provider = Destination["provider"];
type RouteDraft = {
  destinationId: string;
  enabled: boolean;
  reminderMinutes: number;
};
const providers: Provider[] = ["email", "telegram", "max"];
const providerIcon = (provider: Provider): IconName =>
  provider === "email" ? "mail" : provider === "max" ? "max" : "telegram";
const providerLabel = (provider: Provider) =>
  provider === "email" ? "Email" : provider === "max" ? "MAX" : "Telegram";
const reminderOptions = [0, 15, 30, 60, 180, 360, 1440];

export function NotificationsScreen({
  monitor,
  onBack,
}: {
  monitor?: Monitor;
  onBack: () => void;
}) {
  const [channelsOpen, setChannelsOpen] = useState(!monitor);
  const [webhookOpen, setWebhookOpen] = useState(false);
  const [expandedProvider, setExpandedProvider] = useState<Provider | null>(
    null,
  );
  const [now, setNow] = useState(Date.now());
  const busyRef = useRef(false);
  const mounted = useRef(true);
  const epoch = useRef(sessionEpoch());
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [routes, setRoutes] = useState<Route[]>([]);
  const [routeDrafts, setRouteDrafts] = useState<
    Partial<Record<Provider, RouteDraft>>
  >({});
  const [webhook, setWebhook] = useState<Webhook | null>(null);
  const [webhookURL, setWebhookURL] = useState("");
  const [push, setPush] = useState<Push | null>(null);
  const [email, setEmail] = useState("");
  const [emailCode, setEmailCode] = useState("");
  const [emailChallenge, setEmailChallenge] = useState<{
    id: string;
    email: string;
  } | null>(null);
  const [link, setLink] = useState<Link | null>(null);
  const [linkURL, setLinkURL] = useState("");
  const [linkCommand, setLinkCommand] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const path = monitor ? `/monitors/${monitor.id}` : "";

  const refresh = useCallback(async () => {
    try {
      const [d, r, w, p, e] = await Promise.all([
        api<{ destinations: Destination[] }>("/messenger-destinations"),
        monitor
          ? api<{ routes: Route[] }>(`${path}/messenger-routes`)
          : Promise.resolve({ routes: [] }),
        monitor
          ? api<{ webhook: Webhook | null }>(`${path}/webhook`)
          : Promise.resolve({ webhook: null }),
        monitor ? api<Push>(`${path}/push`) : Promise.resolve(null),
        api<{ challenge: { id: string; email: string } | null }>(
          "/messenger-destinations/email/verification",
        ),
      ]);
      if (!mounted.current || epoch.current !== sessionEpoch()) return;
      const drafts: Partial<Record<Provider, RouteDraft>> = {};
      for (const provider of providers) {
        const route = r.routes.find((item) => item.provider === provider);
        drafts[provider] = {
          destinationId:
            route?.destinationId ??
            d.destinations.find(
              (item) => item.provider === provider && item.ready,
            )?.id ??
            "",
          enabled: route?.enabled ?? false,
          reminderMinutes: route?.reminderMinutes ?? 0,
        };
      }
      setDestinations(d.destinations);
      setRoutes(r.routes);
      setRouteDrafts(drafts);
      setWebhook(w.webhook);
      setWebhookURL(w.webhook?.url ?? "");
      setPush(p);
      setEmailChallenge(e.challenge);
    } catch (cause) {
      setError(errorText(cause));
    }
  }, [path, monitor?.id]);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => {
      mounted.current = false;
      clearInterval(timer);
    };
  }, [refresh]);
  const run = async (task: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await task();
    } catch (cause) {
      if (mounted.current && epoch.current === sessionEpoch())
        setError(errorText(cause));
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy(false);
    }
  };
  const editRoute = (provider: Provider, change: Partial<RouteDraft>) =>
    setRouteDrafts((current) => ({
      ...current,
      [provider]: { ...current[provider]!, ...change },
    }));
  const saveRoute = (provider: Provider) =>
    void run(async () => {
      const draft = routeDrafts[provider];
      if (
        !draft ||
        !draft.destinationId ||
        !reminderOptions.includes(draft.reminderMinutes) ||
        !destinations.some(
          (item) =>
            item.id === draft.destinationId &&
            item.provider === provider &&
            item.ready,
        )
      )
        throw new Error("Invalid route");
      const old = routes.find((route) => route.provider === provider);
      await api(`${path}/messenger-routes/${provider}`, {
        method: "PUT",
        body: { ...draft, version: old?.version ?? 0 },
      });
      await refresh();
    });
  const connectEmail = () =>
    void run(async () => {
      const response = await api<{
        destination?: Destination;
        challenge?: { id: string; email: string };
      }>("/messenger-destinations/email", {
        method: "POST",
        body: email ? { email: email.trim() } : {},
      });
      if (response.challenge) {
        setEmailChallenge(response.challenge);
        setNotice("Код отправлен на email.");
      } else {
        setNotice("Email подключён.");
        await refresh();
      }
    });
  const confirmEmail = () =>
    void run(async () => {
      if (!emailChallenge) return;
      await api(
        `/messenger-destinations/email/verification/${emailChallenge.id}/confirm`,
        { method: "POST", body: { code: emailCode } },
      );
      setEmailCode("");
      setEmailChallenge(null);
      await refresh();
    });
  const createLink = (
    provider: "telegram" | "max",
    kind: "private" | "group" | "topic",
  ) =>
    void run(async () => {
      const response = await api<{
        link: Link;
        startUrl: string | null;
        command: string | null;
      }>("/messenger-links", { method: "POST", body: { provider, kind } });
      setLink(response.link);
      setLinkURL(response.startUrl ?? "");
      setLinkCommand(response.command ?? "");
      if (
        response.startUrl &&
        /^https:\/\/(t\.me|max\.ru)\//.test(response.startUrl)
      )
        await native.openCustomTab(response.startUrl);
    });
  const checkLink = () =>
    void run(async () => {
      if (!link) return;
      const status = await api<{ link: Link }>(`/messenger-links/${link.id}`);
      setLink(status.link);
      if (status.link.status === "confirmed") {
        setLink(null);
        setLinkCommand("");
        await refresh();
        setNotice("Подключение подтверждено.");
      } else if (status.link.status === "candidate")
        setNotice(
          "Чат найден. Проверьте получателя и подтвердите подключение отдельной кнопкой.",
        );
    });
  const confirmLink = () =>
    void run(async () => {
      if (
        !link?.candidate ||
        link.status !== "candidate" ||
        Date.parse(link.expiresAt) <= Date.now()
      )
        return;
      await api("/messenger-links/" + link.id + "/confirm", {
        method: "POST",
        body: {},
      });
      setLink(null);
      setLinkCommand("");
      setLinkURL("");
      setNotice("Чат подключён. Выберите его в маршрутах нужного монитора.");
      await refresh();
    });
  const cancelLink = () =>
    void run(async () => {
      if (!link) return;
      try {
        await api("/messenger-links/" + link.id, { method: "DELETE" });
      } catch (cause) {
        if (
          cause instanceof ApiError &&
          cause.code === "link_already_confirmed"
        ) {
          setLink(null);
          setLinkCommand("");
          setLinkURL("");
          setNotice(
            "Этот чат уже подключён. Его можно отключить в списке получателей.",
          );
          await refresh();
          return;
        }
        throw cause;
      }
      setLink(null);
      setLinkCommand("");
      setLinkURL("");
      setNotice("Подключение отменено.");
    });
  const saveWebhook = (enabled: boolean, rotateSecret = false) =>
    void run(async () => {
      const response = await api<{ webhook: Webhook; secret?: string }>(
        `${path}/webhook`,
        {
          method: "PUT",
          body: {
            id: webhook?.id,
            version: webhook?.version ?? 0,
            url: webhookURL.trim(),
            enabled,
            rotateSecret,
          },
        },
      );
      setSecret(response.secret ?? "");
      await refresh();
    });
  const togglePush = (enabled: boolean) =>
    void run(async () => {
      if (!push) return;
      await api(`${path}/push`, {
        method: "PUT",
        body: { enabled, version: push.version },
      });
      await refresh();
    });
  return (
    <Page
      title={
        monitor ? `Уведомления · ${monitor.name}` : "Адреса и чаты аккаунта"
      }
    >
      <Button
        label={monitor ? "К монитору" : "К уведомлениям"}
        iconName="arrow-left"
        onPress={onBack}
        secondary
      />
      {monitor ? (
        <Card>
          <View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
            <Icon name="bell" size={24} />
            <Title>Push на телефоны</Title>
          </View>
          <Label>
            Настройка монитора действует для всех подключённых телефонов.
          </Label>
          <View
            style={{
              flexDirection: "row",
              justifyContent: "space-between",
              alignItems: "center",
            }}
          >
            <Label>Отправлять push</Label>
            <Switch
              value={push?.enabled ?? false}
              disabled={!push || busy}
              onValueChange={togglePush}
            />
          </View>
        </Card>
      ) : null}
      {monitor ? (
        <Card>
          <Title>Маршруты этого монитора</Title>
          {providers.map((provider) => {
            const options = destinations.filter(
              (item) => item.provider === provider && item.ready,
            );
            const draft = routeDrafts[provider];
            return (
              <View key={provider} style={{ gap: 7 }}>
                <View
                  style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
                >
                  <Icon name={providerIcon(provider)} size={24} />
                  <Label>{providerLabel(provider)}</Label>
                </View>
                <Button
                  label={
                    expandedProvider === provider
                      ? "Свернуть настройку"
                      : `${draft?.enabled ? "Включён" : "Выключен"} · настроить`
                  }
                  secondary
                  onPress={() =>
                    setExpandedProvider(
                      expandedProvider === provider ? null : provider,
                    )
                  }
                />
                {expandedProvider === provider ? (
                  <>
                    {options.length ? (
                      options.map((destination) => (
                        <Button
                          key={destination.id}
                          label={`${draft?.destinationId === destination.id ? "✓ " : ""}${destination.label}`}
                          iconName={providerIcon(provider)}
                          secondary={draft?.destinationId !== destination.id}
                          disabled={busy}
                          onPress={() =>
                            editRoute(provider, {
                              destinationId: destination.id,
                            })
                          }
                        />
                      ))
                    ) : (
                      <Label>Сначала подключите адрес или чат.</Label>
                    )}
                    <View
                      style={{
                        flexDirection: "row",
                        justifyContent: "space-between",
                        alignItems: "center",
                      }}
                    >
                      <Label>Отправлять уведомления</Label>
                      <Switch
                        value={draft?.enabled ?? false}
                        disabled={busy || !options.length}
                        onValueChange={(enabled) =>
                          editRoute(provider, { enabled })
                        }
                      />
                    </View>
                    <Label>Напоминание об открытом инциденте</Label>
                    <View
                      style={{ flexDirection: "row", flexWrap: "wrap", gap: 6 }}
                    >
                      {reminderOptions.map((minutes) => (
                        <Button
                          key={minutes}
                          label={
                            minutes === 0
                              ? "Нет"
                              : minutes < 60
                                ? `${minutes} мин`
                                : `${minutes / 60} ч`
                          }
                          secondary={draft?.reminderMinutes !== minutes}
                          disabled={busy || !options.length}
                          onPress={() =>
                            editRoute(provider, { reminderMinutes: minutes })
                          }
                        />
                      ))}
                    </View>
                    <Button
                      label={`Сохранить ${providerLabel(provider)}`}
                      iconName="check"
                      disabled={
                        busy || !draft?.destinationId || !options.length
                      }
                      onPress={() => saveRoute(provider)}
                    />
                  </>
                ) : null}
              </View>
            );
          })}
        </Card>
      ) : null}
      <Button
        label={
          channelsOpen
            ? "Скрыть адреса и чаты аккаунта"
            : "Подключить адрес или чат аккаунта"
        }
        secondary
        iconName="mail"
        onPress={() => setChannelsOpen(!channelsOpen)}
      />
      {channelsOpen ? (
        <>
          <Card>
            <Title>Подключения аккаунта</Title>
            <Label muted>
              Это общие получатели. Доставка каждого монитора включается
              отдельно в его маршрутах.
            </Label>
            {destinations.map((destination) => (
              <View key={destination.id} style={{ gap: 6 }}>
                <Label>
                  {providerLabel(destination.provider)} · {destination.label} ·{" "}
                  {destination.ready ? "готов" : "нужно проверить"}
                </Label>
                <Button
                  label="Отключить получателя"
                  danger
                  secondary
                  disabled={busy}
                  onPress={() =>
                    Alert.alert(
                      "Отключить получателя?",
                      "Уведомления всех связанных мониторов перестанут приходить сюда.",
                      [
                        { text: "Отмена", style: "cancel" },
                        {
                          text: "Отключить",
                          style: "destructive",
                          onPress: () =>
                            void run(async () => {
                              await api(
                                `/messenger-destinations/${destination.id}`,
                                {
                                  method: "DELETE",
                                  body: { version: destination.version },
                                },
                              );
                              await refresh();
                            }),
                        },
                      ],
                    )
                  }
                />
              </View>
            ))}
            {!destinations.length ? (
              <Label muted>Получателей пока нет.</Label>
            ) : null}
          </Card>
          <Card>
            <View
              style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
            >
              <Icon name="mail" size={24} />
              <Title>Подключить email</Title>
            </View>
            <Label>
              Это адрес уведомлений. Он не меняет email входа. Оставьте поле
              пустым для подтверждённого email аккаунта.
            </Label>
            <Field
              label="Адрес email"
              value={email}
              onChangeText={setEmail}
              keyboardType="email-address"
            />
            <Button
              label="Подключить"
              iconName="plus"
              disabled={busy}
              onPress={connectEmail}
            />
            {emailChallenge ? (
              <>
                <Label>Подтверждение для {emailChallenge.email}</Label>
                <Field
                  label="Шесть цифр из письма"
                  value={emailCode}
                  onChangeText={(value) =>
                    setEmailCode(value.replace(/\D/g, "").slice(0, 6))
                  }
                  keyboardType="number-pad"
                />
                <Button
                  label="Подтвердить email"
                  iconName="check"
                  disabled={busy || emailCode.length !== 6}
                  onPress={confirmEmail}
                />
              </>
            ) : null}
          </Card>
          <Card>
            <Title>Подключить мессенджер</Title>
            <Button
              label="Telegram · личный чат"
              iconName="telegram"
              onPress={() => createLink("telegram", "private")}
              secondary
            />
            <Button
              label="Telegram · группа"
              iconName="telegram"
              onPress={() => createLink("telegram", "group")}
              secondary
            />
            <Button
              label="Telegram · топик"
              iconName="telegram"
              onPress={() => createLink("telegram", "topic")}
              secondary
            />
            <Button
              label="MAX · личный чат"
              iconName="max"
              onPress={() => createLink("max", "private")}
              secondary
            />
            {link ? (
              <>
                <Label>
                  Откройте бота и отправьте команду в нужный чат. Срок действия:{" "}
                  {new Date(link.expiresAt).toLocaleString("ru-RU")}
                </Label>
                {linkCommand ? <Label>{linkCommand}</Label> : null}
                {linkURL ? (
                  <Button
                    label="Открыть бота"
                    iconName="globe"
                    onPress={() =>
                      void run(() => native.openCustomTab(linkURL))
                    }
                    secondary
                  />
                ) : null}
                <Label>
                  {Date.parse(link.expiresAt) <= now ||
                  link.status === "expired"
                    ? "Срок подключения истёк. Начните заново."
                    : link.status === "candidate"
                      ? "Получатель найден — подтвердите его ниже."
                      : link.errorCode === "link_cancelled"
                        ? "Подключение отменено."
                        : link.status === "failed"
                          ? "Подключение не удалось. Начните заново."
                          : link.status === "cancelled"
                            ? "Подключение отменено."
                            : "Ожидаем ответ бота."}
                </Label>
                {link.candidate ? (
                  <Card>
                    <Title>{link.candidate.label || "Найденный чат"}</Title>
                    <Label>
                      Чат: {link.candidate.chatId} · отправитель:{" "}
                      {link.candidate.userId}
                      {link.candidate.threadId
                        ? ` · топик: ${link.candidate.threadId}`
                        : ""}
                    </Label>
                    <Label muted>
                      Убедитесь, что это нужный получатель. Только подтверждение
                      добавит его в аккаунт.
                    </Label>
                    <Button
                      label="Подтвердить этот чат"
                      iconName="check"
                      disabled={
                        busy ||
                        link.status !== "candidate" ||
                        Date.parse(link.expiresAt) <= now
                      }
                      onPress={confirmLink}
                    />
                  </Card>
                ) : null}
                <Button
                  label="Отменить подключение"
                  secondary
                  disabled={busy}
                  onPress={cancelLink}
                />
                <Button
                  disabled={busy || Date.parse(link.expiresAt) <= now}
                  label="Проверить подключение"
                  iconName="refresh"
                  onPress={checkLink}
                />
              </>
            ) : null}
          </Card>
        </>
      ) : null}
      {monitor ? (
        <Button
          label={webhookOpen ? "Скрыть webhook" : "Дополнительно: webhook"}
          secondary
          iconName="globe"
          onPress={() => setWebhookOpen(!webhookOpen)}
        />
      ) : null}
      {monitor && webhookOpen ? (
        <Card>
          <View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
            <Icon name="globe" size={24} />
            <Title>Webhook</Title>
          </View>
          <Field
            label="HTTPS URL"
            value={webhookURL}
            onChangeText={setWebhookURL}
            keyboardType="url"
          />
          <Button
            label="Сохранить webhook"
            iconName="check"
            disabled={busy || !webhookURL.startsWith("https://")}
            onPress={() => saveWebhook(webhook?.enabled ?? true)}
          />
          {secret ? (
            <Label>Секрет webhook (показывается один раз): {secret}</Label>
          ) : null}
          {webhook ? (
            <>
              <Label>
                {webhook.enabled ? "Доставка включена" : "Доставка выключена"}
              </Label>
              <Button
                label={
                  webhook.enabled ? "Выключить webhook" : "Включить webhook"
                }
                disabled={busy}
                onPress={() => saveWebhook(!webhook.enabled)}
                secondary
              />
              <Button
                label="Обновить секрет"
                iconName="refresh"
                disabled={busy}
                onPress={() =>
                  Alert.alert(
                    "Обновить секрет webhook?",
                    "Старый секрет перестанет действовать.",
                    [
                      { text: "Отмена", style: "cancel" },
                      {
                        text: "Обновить",
                        onPress: () => saveWebhook(webhook.enabled, true),
                      },
                    ],
                  )
                }
                secondary
              />
              <Button
                label="Удалить webhook"
                iconName="trash"
                danger
                onPress={() =>
                  Alert.alert("Удалить webhook?", "", [
                    { text: "Отмена", style: "cancel" },
                    {
                      text: "Удалить",
                      style: "destructive",
                      onPress: () =>
                        void run(async () => {
                          await api(`${path}/webhook`, {
                            method: "DELETE",
                            body: { id: webhook.id, version: webhook.version },
                          });
                          await refresh();
                        }),
                    },
                  ])
                }
              />
            </>
          ) : null}
        </Card>
      ) : null}
      <ErrorNotice message={error} />
      {notice ? <Label>{notice}</Label> : null}
    </Page>
  );
}
