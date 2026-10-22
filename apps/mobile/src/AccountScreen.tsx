import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  PermissionsAndroid,
  Platform,
  Switch,
  View,
} from "react-native";
import { acceptSession, api, errorText, session, sessionEpoch } from "./api";
import { native } from "./native";
import type { Provider } from "./types";
import {
  Button,
  Card,
  ErrorNotice,
  Field,
  Icon,
  Label,
  Page,
  Title,
} from "./ui";

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
type PushStatus = {
  enabled: boolean;
  optedOut: boolean;
  provider: string;
  status: string;
};
type Source = "profile" | "sessions" | "identities" | "push";
type ActionArea = Source | "password" | "help" | "delete" | "logout";
const sources: Source[] = ["profile", "sessions", "identities", "push"];
const flags = () => ({
  profile: false,
  sessions: false,
  identities: false,
  push: false,
});
const errors = () => ({ profile: "", sessions: "", identities: "", push: "" });
const actionMessages = () => ({
  profile: "",
  sessions: "",
  identities: "",
  push: "",
  password: "",
  help: "",
  delete: "",
  logout: "",
});

export function AccountScreen({ onLogout }: { onLogout: () => Promise<void> }) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [verifiedSessionId, setVerifiedSessionId] = useState("");
  const [name, setName] = useState("");
  const [sessions, setSessions] = useState<DeviceSession[]>([]);
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [push, setPush] = useState<PushStatus | null>(null);
  const [loading, setLoading] = useState(flags);
  const [loaded, setLoaded] = useState(flags);
  const [sourceError, setSourceError] = useState(errors);
  const [password, setPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [linkPassword, setLinkPassword] = useState("");
  const [deletePassword, setDeletePassword] = useState("");
  const [helpOpen, setHelpOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState(actionMessages);
  const [actionNotice, setActionNotice] = useState(actionMessages);
  const mounted = useRef(false);
  const generations = useRef<Record<Source, number>>({
    profile: 0,
    sessions: 0,
    identities: 0,
    push: 0,
  });
  const busyRef = useRef(false);
  const nameDirty = useRef(false);
  const nameEdit = useRef(0);

  const load = useCallback(
    async <T,>(
      source: Source,
      work: () => Promise<T>,
      apply: (value: T, sessionId: string) => void,
    ) => {
      const current = session();
      if (!current) return;
      const accountId = current.account.id;
      const sessionId = current.session.id;
      const epoch = sessionEpoch();
      const generation = ++generations.current[source];
      const valid = () =>
        mounted.current &&
        generations.current[source] === generation &&
        sessionEpoch() === epoch &&
        session()?.account.id === accountId &&
        session()?.session.id === sessionId;
      setLoading((state) => ({ ...state, [source]: true }));
      setLoaded((state) => ({ ...state, [source]: false }));
      setSourceError((state) => ({ ...state, [source]: "" }));
      if (source === "profile") {
        setProfile(null);
        setVerifiedSessionId("");
      }
      if (source === "push") setPush(null);
      try {
        const value = await work();
        if (!valid()) return;
        apply(value, sessionId);
        setLoaded((state) => ({ ...state, [source]: true }));
      } catch (cause) {
        if (!valid()) return;
        setSourceError((state) => ({ ...state, [source]: errorText(cause) }));
        if (source === "sessions") setSessions([]);
        if (source === "identities") setIdentities([]);
      } finally {
        if (valid()) setLoading((state) => ({ ...state, [source]: false }));
      }
    },
    [],
  );
  const refreshProfile = useCallback(
    () =>
      load(
        "profile",
        () => api<Profile>("/me"),
        (value, sessionId) => {
          setProfile(value);
          setVerifiedSessionId(sessionId);
          if (!nameDirty.current) setName(value.account.name);
        },
      ),
    [load],
  );
  const refreshSessions = useCallback(
    () =>
      load(
        "sessions",
        () => api<{ sessions: DeviceSession[] }>("/me/sessions"),
        (value) => setSessions(value.sessions),
      ),
    [load],
  );
  const refreshIdentities = useCallback(
    () =>
      load(
        "identities",
        () => api<{ identities: Identity[] }>("/me/identities"),
        (value) => setIdentities(value.identities),
      ),
    [load],
  );
  const refreshPush = useCallback(
    () =>
      load(
        "push",
        () => native.pushStatus(),
        (value) => setPush(value),
      ),
    [load],
  );
  const refresh = useCallback(async () => {
    await Promise.all([
      refreshProfile(),
      refreshSessions(),
      refreshIdentities(),
      refreshPush(),
    ]);
  }, [refreshProfile, refreshSessions, refreshIdentities, refreshPush]);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
      for (const source of sources) ++generations.current[source];
    };
  }, [refresh]);

  const showNotice = (area: ActionArea, message: string) =>
    setActionNotice((state) => ({ ...state, [area]: message }));
  const run = async (area: ActionArea, work: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setActionError((state) => ({ ...state, [area]: "" }));
    setActionNotice((state) => ({ ...state, [area]: "" }));
    const epoch = sessionEpoch();
    try {
      await work();
    } catch (cause) {
      if (mounted.current && sessionEpoch() === epoch)
        setActionError((state) => ({ ...state, [area]: errorText(cause) }));
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy(false);
    }
  };
  const openWebsite = (url: string) =>
    void run("help", () => native.openCustomTab(url));
  const active = session();
  const verifiedProfile =
    !!profile &&
    loaded.profile &&
    !loading.profile &&
    profile.account.id === active?.account.id &&
    verifiedSessionId === active?.session.id;
  const isAdmin = verifiedProfile && profile.account.role === "admin";
  const canManageIdentity =
    verifiedProfile &&
    !isAdmin &&
    loaded.identities &&
    !loading.identities &&
    !native.DEV_CLIENT;

  const changePassword = () =>
    void run("password", async () => {
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
            void run("delete", async () => {
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
    void run("identities", async () => {
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
          password: linkPassword,
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
      setLinkPassword("");
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
            void run("identities", async () => {
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
                body: { identityId: item.id, password: linkPassword },
              });
              await acceptSession(updated);
              setLinkPassword("");
              showNotice("identities", "Способ входа отключён.");
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
          void run("sessions", async () => {
            await api(`/me/sessions/${id}`, { method: "DELETE" });
            if (id === session()?.session.id) await onLogout();
            else {
              showNotice("sessions", "Сеанс завершён.");
              await refreshSessions();
            }
          }),
      },
    ]);
  const togglePush = (enabled: boolean) =>
    void run("push", async () => {
      if (enabled && Number(Platform.Version) >= 33) {
        const permission = await PermissionsAndroid.request(
          PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS,
        );
        if (permission !== PermissionsAndroid.RESULTS.GRANTED) {
          showNotice(
            "push",
            "Разрешите уведомления в настройках Android и повторите.",
          );
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
                  void run("push", async () => {
                    await native.pushToggle(true, true);
                    showNotice("push", "Телефон подключён к этому аккаунту.");
                    await refreshPush();
                  }),
              },
            ],
          );
          return;
        }
        throw cause;
      }
      showNotice(
        "push",
        enabled ? "Уведомления включены." : "Уведомления выключены.",
      );
      await refreshPush();
    });

  return (
    <Page title="Аккаунт" refresh={refresh}>
      <Card>
        <Title>Профиль</Title>
        {loading.profile ? <Label muted>Обновляем профиль…</Label> : null}
        <ErrorNotice message={sourceError.profile} />
        {verifiedProfile ? (
          <>
            <Label>
              {profile.email} ·{" "}
              {profile.emailVerified
                ? "Email подтверждён"
                : "Email не подтверждён"}
            </Label>
            <Field
              label="Имя"
              value={name}
              onChangeText={(value) => {
                nameDirty.current = true;
                ++nameEdit.current;
                setName(value);
              }}
            />
            <Button
              label="Сохранить имя"
              disabled={busy || !name.trim()}
              onPress={() =>
                void run("profile", async () => {
                  const submitted = name.trim();
                  const edit = nameEdit.current;
                  await api("/me", {
                    method: "PATCH",
                    body: { name: submitted },
                  });
                  if (edit === nameEdit.current) {
                    nameDirty.current = false;
                    setName(submitted);
                  }
                  showNotice("profile", "Имя сохранено.");
                  await refreshProfile();
                })
              }
            />
          </>
        ) : null}
        <ErrorNotice message={actionError.profile} />
        {actionNotice.profile ? <Label>{actionNotice.profile}</Label> : null}
      </Card>
      <Card>
        <Title>Push на этом телефоне</Title>
        {loading.push ? <Label muted>Проверяем уведомления…</Label> : null}
        <ErrorNotice message={sourceError.push} />
        {loaded.push && push ? (
          <>
            <View
              style={{
                flexDirection: "row",
                justifyContent: "space-between",
                alignItems: "center",
              }}
            >
              <Label>Получать уведомления</Label>
              <Switch
                value={push.enabled}
                disabled={native.DEV_CLIENT || busy}
                onValueChange={togglePush}
              />
            </View>
            {isAdmin && !loading.push ? (
              <Label>
                Провайдер: {push.provider || "не выбран"} ·{" "}
                {push.status || "не подключено"}
              </Label>
            ) : null}
            {!isAdmin ? (
              <Label muted>
                {push.enabled
                  ? "Уведомления включены."
                  : "Уведомления выключены."}
              </Label>
            ) : null}
          </>
        ) : null}
        {native.DEV_CLIENT ? (
          <Label muted>Push в dev-сборке отключён.</Label>
        ) : null}
        <ErrorNotice message={actionError.push} />
        {actionNotice.push ? <Label>{actionNotice.push}</Label> : null}
      </Card>
      <Card>
        <Title>Способы входа</Title>
        {loading.identities ? (
          <Label muted>Проверяем способы входа…</Label>
        ) : null}
        <ErrorNotice message={sourceError.identities} />
        {loaded.identities && !loading.identities ? (
          <>
            {identities.map((item) => (
              <View key={item.id} style={{ gap: 5 }}>
                <View
                  style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
                >
                  <Icon
                    name={
                      item.provider === "vk"
                        ? "vk"
                        : item.provider === "yandex"
                          ? "yandex"
                          : "user"
                    }
                    size={24}
                  />
                  <Label>
                    {item.provider === "vk"
                      ? "VK"
                      : item.provider === "yandex"
                        ? "Яндекс"
                        : item.provider}{" "}
                    {item.label ?? ""}
                  </Label>
                </View>
                {canManageIdentity ? (
                  <Button
                    label="Отключить"
                    onPress={() => unlink(item)}
                    secondary
                    disabled={busy || !profile.hasPassword || !linkPassword}
                  />
                ) : null}
              </View>
            ))}
            {native.DEV_CLIENT ? (
              <Label muted>
                Управление социальным входом доступно в обычном приложении.
              </Label>
            ) : null}
            {isAdmin && !native.DEV_CLIENT ? (
              <Label muted>
                Администратор управляет способами входа в кабинете на сайте
                после подтверждения MFA.
              </Label>
            ) : null}
            {canManageIdentity ? (
              <>
                {!profile.hasPassword ? (
                  <Label muted>
                    Для изменения способов входа сначала задайте пароль через
                    восстановление доступа.
                  </Label>
                ) : (
                  <>
                    <Field
                      label="Пароль для способов входа"
                      value={linkPassword}
                      onChangeText={setLinkPassword}
                      secure
                    />
                    {!identities.some((item) => item.provider === "yandex") ? (
                      <Button
                        label="Подключить Яндекс"
                        iconName="yandex"
                        onPress={() => link("yandex")}
                        disabled={busy || !linkPassword}
                        secondary
                      />
                    ) : null}
                    {!identities.some((item) => item.provider === "vk") ? (
                      <Button
                        label="Подключить VK"
                        iconName="vk"
                        onPress={() => link("vk")}
                        disabled={busy || !linkPassword}
                        secondary
                      />
                    ) : null}
                  </>
                )}
              </>
            ) : null}
          </>
        ) : null}
        <ErrorNotice message={actionError.identities} />
        {actionNotice.identities ? (
          <Label>{actionNotice.identities}</Label>
        ) : null}
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
        <Label muted>После изменения пароля потребуется новый вход.</Label>
        <ErrorNotice message={actionError.password} />
        {actionNotice.password ? <Label>{actionNotice.password}</Label> : null}
      </Card>
      <Card>
        <Title>Сеансы</Title>
        {loading.sessions ? <Label muted>Обновляем сеансы…</Label> : null}
        <ErrorNotice message={sourceError.sessions} />
        {loaded.sessions && !loading.sessions ? (
          <>
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
                  disabled={busy}
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
                      void run("sessions", async () => {
                        await api("/me/sessions", { method: "DELETE" });
                        await onLogout();
                      }),
                  },
                ])
              }
              disabled={busy}
              danger
            />
          </>
        ) : null}
        <ErrorNotice message={actionError.sessions} />
        {actionNotice.sessions ? <Label>{actionNotice.sessions}</Label> : null}
      </Card>
      <Card>
        <Button
          label={helpOpen ? "Назад" : "Помощь и документы"}
          iconName={helpOpen ? "arrow-left" : "info"}
          onPress={() => setHelpOpen((value) => !value)}
          secondary
        />
        {helpOpen ? (
          <>
            <Button
              label="Поддержка"
              iconName="mail"
              onPress={() =>
                openWebsite("https://heartbeat-checker.ru/contacts")
              }
              secondary
            />
            <Button
              label="Условия и оферта"
              iconName="arrow-up-right"
              onPress={() => openWebsite("https://heartbeat-checker.ru/terms")}
              secondary
            />
            <Button
              label="Персональные данные"
              iconName="shield"
              onPress={() =>
                openWebsite("https://heartbeat-checker.ru/privacy")
              }
              secondary
            />
            <Button
              label="Cookie"
              iconName="arrow-up-right"
              onPress={() =>
                openWebsite("https://heartbeat-checker.ru/cookies")
              }
              secondary
            />
            <Label muted>Версия Android: 2.0.2 (9) · тема системная</Label>
          </>
        ) : null}
        <ErrorNotice message={actionError.help} />
      </Card>
      <Card>
        <Title>Опасные действия</Title>
        <Label>Удаление аккаунта</Label>
        {isAdmin ? (
          <Label muted>
            Перед удалением передайте права администратора в кабинете на сайте.
          </Label>
        ) : verifiedProfile ? (
          <>
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
          </>
        ) : (
          <Label muted>Проверяем права аккаунта…</Label>
        )}
        <ErrorNotice message={actionError.delete} />
      </Card>
      <Card>
        <Button
          label="Выйти"
          iconName="logout"
          onPress={() => void run("logout", onLogout)}
          secondary
        />
        <ErrorNotice message={actionError.logout} />
      </Card>
    </Page>
  );
}
