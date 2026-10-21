import React, { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Appearance,
  PermissionsAndroid,
  Platform,
  Switch,
  View,
} from "react-native";
import { acceptSession, api, errorText, session } from "./api";
import { native } from "./native";
import type { Provider } from "./types";
import { Button, Card, ErrorNotice, Field, Label, Page, Title } from "./ui";

type Profile = {
  account: { id: string; name: string; email: string; role?: string };
  email: string;
  hasPassword: boolean;
  emailVerified: boolean;
};
type DeviceSession = {
  id: string;
  createdAt: string;
  lastUsedAt: string;
  expiresAt: string;
  ip: string;
  userAgent: string;
  current: boolean;
};
type Identity = { id: string; provider: string; label?: string };

export function AccountScreen({ onLogout }: { onLogout: () => Promise<void> }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [name, setName] = useState("");
  const [sessions, setSessions] = useState<DeviceSession[]>([]);
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [password, setPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [deletePassword, setDeletePassword] = useState("");
  const [push, setPush] = useState<{
    enabled: boolean;
    optedOut: boolean;
    provider: string;
    status: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const refresh = useCallback(async () => {
    try {
      const [p, s, d, status] = await Promise.all([
        api<Profile>("/me"),
        api<{ sessions: DeviceSession[] }>("/me/sessions"),
        api<{ identities: Identity[] }>("/me/identities"),
        native.pushStatus(),
      ]);
      setProfile(p);
      setName(p.account.name);
      setSessions(s.sessions);
      setIdentities(d.identities);
      setPush(status);
    } catch (cause) {
      setError(errorText(cause));
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, []);
  const run = async (work: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await work();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(false);
    }
  };
  const logout = () => void run(onLogout);
  const changePassword = () =>
    void run(async () => {
      await api("/auth/native/password/change", {
        method: "POST",
        body: { currentPassword: password, newPassword },
      });
      setPassword("");
      setNewPassword("");
      await onLogout();
    });
  const deleteAccount = () =>
    Alert.alert(
      "Удалить аккаунт?",
      "Мониторы, история и подключения будут удалены. Это действие необратимо.",
      [
        { text: "Отмена", style: "cancel" },
        {
          text: "Удалить",
          style: "destructive",
          onPress: () =>
            void run(async () => {
              await api("/me", {
                method: "DELETE",
                body: {
                  currentPassword: deletePassword,
                  expectedAccountId: session()?.account.id,
                },
              });
              setDeletePassword("");
              await onLogout();
            }),
        },
      ],
    );
  const link = (provider: Provider) =>
    void run(async () => {
      const proof = await native.beginOAuth();
      const result = await api<{
        flowId: string;
        authorizationUrl: string;
        expiresAt: string;
      }>(`/auth/native/oauth/${provider}/start`, {
        method: "POST",
        body: {
          mode: "link",
          state: proof.state,
          codeChallenge: proof.codeChallenge,
          password,
        },
      });
      await native.saveOAuth({
        flowId: result.flowId,
        provider,
        mode: "link",
        state: proof.state,
        codeVerifier: "",
        expiresAt: result.expiresAt,
      });
      await native.openCustomTab(result.authorizationUrl);
    });
  const unlink = (item: Identity) =>
    Alert.alert(
      "Отключить способ входа?",
      `${item.provider} больше не будет доступен для входа.`,
      [
        { text: "Отмена", style: "cancel" },
        {
          text: "Отключить",
          style: "destructive",
          onPress: () =>
            void run(async () => {
              const updated = await api<{
                account: Profile["account"];
                session: {
                  id: string;
                  token: string;
                  expiresAt: string;
                  revokeToken?: string;
                };
              }>(`/auth/native/oauth/${item.provider}`, {
                method: "DELETE",
                body: { identityId: item.id, password },
              });
              await acceptSession(updated);
              setPassword("");
              await refresh();
            }),
        },
      ],
    );
  const revoke = (id: string) =>
    Alert.alert("Завершить сеанс?", "", [
      { text: "Отмена", style: "cancel" },
      {
        text: "Завершить",
        onPress: () =>
          void run(async () => {
            await api(`/me/sessions/${id}`, { method: "DELETE" });
            if (id === session()?.session.id) await onLogout();
            else await refresh();
          }),
      },
    ]);
  const togglePush = (enabled: boolean) =>
    void run(async () => {
      if (enabled && Number(Platform.Version) >= 33) {
        const permission = await PermissionsAndroid.request(
          PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS,
        );
        if (permission !== PermissionsAndroid.RESULTS.GRANTED) {
          setNotice("Разрешите уведомления в настройках Android и повторите.");
          return;
        }
      }
      try {
        await native.pushToggle(enabled, false);
      } catch (cause) {
        if (
          enabled &&
          (cause as { code?: string })?.code === "push_account_change_required"
        ) {
          Alert.alert(
            "Привязать телефон к этому аккаунту?",
            "Прежняя привязка телефона будет отозвана.",
            [
              { text: "Отмена", style: "cancel" },
              {
                text: "Привязать",
                onPress: () =>
                  void run(async () => {
                    await native.pushToggle(true, true);
                    setPush(await native.pushStatus());
                  }),
              },
            ],
          );
          return;
        }
        throw cause;
      }
      setPush(await native.pushStatus());
    });
  return (
    <Page title="Аккаунт" refresh={() => void refresh()}>
      <Card>
        <Title>Профиль</Title>
        <Label>
          {profile?.email ?? "—"} ·{" "}
          {profile?.emailVerified
            ? "Email подтверждён"
            : "Email не подтверждён"}
        </Label>
        <Field label="Имя" value={name} onChangeText={setName} />
        <Button
          label="Сохранить имя"
          disabled={busy || !name.trim()}
          onPress={() =>
            void run(async () => {
              await api("/me", {
                method: "PATCH",
                body: { name: name.trim() },
              });
              await refresh();
            })
          }
        />
      </Card>
      <Card>
        <Title>Push на этом телефоне</Title>
        <View
          style={{
            flexDirection: "row",
            justifyContent: "space-between",
            alignItems: "center",
          }}
        >
          <Label>Получать уведомления</Label>
          <Switch
            value={push?.enabled ?? false}
            disabled={busy || !push}
            onValueChange={togglePush}
          />
        </View>
        <Label>
          {push?.provider
            ? `Провайдер: ${push.provider}`
            : "Провайдер не выбран"}{" "}
          · {push?.status ?? "не подключено"}
        </Label>
        <Label>
          При отключении привязка отзывается. Повторное включение требует
          подтверждения разрешения Android.
        </Label>
      </Card>
      <Card>
        <Title>Способы входа</Title>
        {identities.map((item) => (
          <View key={item.id} style={{ gap: 5 }}>
            <Label>
              {item.provider.toUpperCase()} {item.label ?? ""}
            </Label>
            <Button label="Отключить" onPress={() => unlink(item)} secondary />
          </View>
        ))}
        <Button
          label="Подключить Яндекс"
          onPress={() => link("yandex")}
          secondary
        />
        <Button label="Подключить VK" onPress={() => link("vk")} secondary />
      </Card>
      <Card>
        <Title>Пароль</Title>
        <Field
          label="Текущий пароль"
          value={password}
          onChangeText={setPassword}
          secure
        />
        <Field
          label="Новый пароль"
          value={newPassword}
          onChangeText={setNewPassword}
          secure
        />
        <Button
          label="Изменить пароль"
          disabled={busy || !password || !newPassword}
          onPress={changePassword}
        />
        <Label>После изменения пароля потребуется новый вход.</Label>
      </Card>
      <Card>
        <Title>Сеансы</Title>
        {sessions.map((item) => (
          <View key={item.id} style={{ gap: 5 }}>
            <Label>
              {item.current
                ? "Это устройство"
                : item.userAgent || "Другое устройство"}{" "}
              · до {new Date(item.expiresAt).toLocaleString("ru-RU")}
            </Label>
            <Button
              label="Завершить сеанс"
              onPress={() => revoke(item.id)}
              secondary
            />
          </View>
        ))}
        <Button
          label="Завершить все сеансы"
          onPress={() =>
            Alert.alert("Завершить все сеансы?", "", [
              { text: "Отмена" },
              {
                text: "Завершить",
                onPress: () =>
                  void run(async () => {
                    await api("/me/sessions", { method: "DELETE" });
                    await onLogout();
                  }),
              },
            ])
          }
          danger
        />
      </Card>
      <Card>
        <Title>Помощь и документы</Title>
        <Button
          label="Поддержка"
          onPress={() =>
            void native.openCustomTab("https://heartbeat-checker.ru/contacts")
          }
          secondary
        />
        <Button
          label="Условия и оферта"
          onPress={() =>
            void native.openCustomTab("https://heartbeat-checker.ru/terms")
          }
          secondary
        />
        <Button
          label="Персональные данные"
          onPress={() =>
            void native.openCustomTab("https://heartbeat-checker.ru/privacy")
          }
          secondary
        />
        <Button
          label="Cookie"
          onPress={() =>
            void native.openCustomTab("https://heartbeat-checker.ru/cookies")
          }
          secondary
        />
        <Label>
          Версия Android: 2.0.0 (7) · тема:{" "}
          {Appearance.getColorScheme() === "dark" ? "тёмная" : "светлая"}{" "}
          (системная)
        </Label>
      </Card>
      <Card>
        <Button label="Выйти" onPress={logout} secondary />
      </Card>
      <Card>
        <Title>Удаление аккаунта</Title>
        <Label>Введите текущий пароль и подтвердите удаление.</Label>
        <Field
          label="Текущий пароль для удаления"
          value={deletePassword}
          onChangeText={setDeletePassword}
          secure
        />
        <Button
          label="Удалить аккаунт"
          danger
          disabled={busy || !deletePassword}
          onPress={deleteAccount}
        />
      </Card>
      <ErrorNotice message={error} />
      {notice ? <Label>{notice}</Label> : null}
    </Page>
  );
}
