import React, { useCallback, useEffect, useState } from "react";
import { Alert, Switch, View } from "react-native";
import { api, errorText } from "./api";
import { native } from "./native";
import type { Monitor } from "./types";
import { Button, Card, ErrorNotice, Field, Label, Page, Title } from "./ui";

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
};
type Provider = Destination["provider"];
type RouteDraft = {
  destinationId: string;
  enabled: boolean;
  reminderMinutes: number;
};
const providers: Provider[] = ["email", "telegram", "max"];
const reminderOptions = [0, 15, 30, 60, 180, 360, 1440];

export function NotificationsScreen({
  monitor,
  onBack,
}: {
  monitor: Monitor;
  onBack: () => void;
}) {
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
  const path = `/monitors/${monitor.id}`;

  const refresh = useCallback(async () => {
    try {
      const [d, r, w, p, e] = await Promise.all([
        api<{ destinations: Destination[] }>("/messenger-destinations"),
        api<{ routes: Route[] }>(`${path}/messenger-routes`),
        api<{ webhook: Webhook | null }>(`${path}/webhook`),
        api<Push>(`${path}/push`),
        api<{ challenge: { id: string; email: string } | null }>(
          "/messenger-destinations/email/verification",
        ),
      ]);
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
  }, [path]);
  useEffect(() => {
    void refresh();
  }, []);
  const run = async (task: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await task();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(false);
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
      if (
        status.link.status === "ready" ||
        status.link.status === "candidate"
      ) {
        await api(`/messenger-links/${link.id}/confirm`, {
          method: "POST",
          body: {},
        });
        setLink(null);
        setLinkCommand("");
        await refresh();
      }
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
      title={`Уведомления · ${monitor.name}`}
      refresh={() => void refresh()}
    >
      <Button label="К монитору" onPress={onBack} secondary />
      <Card>
        <Title>Push на телефоны</Title>
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
      <Card>
        <Title>Email, Telegram и MAX</Title>
        {providers.map((provider) => {
          const options = destinations.filter(
            (item) => item.provider === provider && item.ready,
          );
          const draft = routeDrafts[provider];
          return (
            <View key={provider} style={{ gap: 7 }}>
              <Label>{provider.toUpperCase()}</Label>
              {options.length ? (
                options.map((destination) => (
                  <Button
                    key={destination.id}
                    label={`${draft?.destinationId === destination.id ? "✓ " : ""}${destination.label}`}
                    secondary={draft?.destinationId !== destination.id}
                    disabled={busy}
                    onPress={() =>
                      editRoute(provider, { destinationId: destination.id })
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
                  onValueChange={(enabled) => editRoute(provider, { enabled })}
                />
              </View>
              <Label>Напоминание об открытом инциденте</Label>
              <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 6 }}>
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
                label={`Сохранить ${provider.toUpperCase()}`}
                disabled={busy || !draft?.destinationId || !options.length}
                onPress={() => saveRoute(provider)}
              />
            </View>
          );
        })}
      </Card>
      <Card>
        <Title>Подключить email</Title>
        <Label>Оставьте поле пустым для подтверждённого email аккаунта.</Label>
        <Field
          label="Адрес email"
          value={email}
          onChangeText={setEmail}
          keyboardType="email-address"
        />
        <Button label="Подключить" disabled={busy} onPress={connectEmail} />
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
          onPress={() => createLink("telegram", "private")}
          secondary
        />
        <Button
          label="Telegram · группа"
          onPress={() => createLink("telegram", "group")}
          secondary
        />
        <Button
          label="Telegram · топик"
          onPress={() => createLink("telegram", "topic")}
          secondary
        />
        <Button
          label="MAX · личный чат"
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
                onPress={() => void native.openCustomTab(linkURL)}
                secondary
              />
            ) : null}
            <Button label="Проверить подключение" onPress={checkLink} />
          </>
        ) : null}
      </Card>
      <Card>
        <Title>Webhook</Title>
        <Field
          label="HTTPS URL"
          value={webhookURL}
          onChangeText={setWebhookURL}
          keyboardType="url"
        />
        <Button
          label="Сохранить webhook"
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
              label={webhook.enabled ? "Выключить webhook" : "Включить webhook"}
              disabled={busy}
              onPress={() => saveWebhook(!webhook.enabled)}
              secondary
            />
            <Button
              label="Обновить секрет"
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
      <ErrorNotice message={error} />
      {notice ? <Label>{notice}</Label> : null}
    </Page>
  );
}
