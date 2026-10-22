import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  BackHandler,
  Linking,
  Pressable,
  StatusBar,
  Text,
  View,
} from "react-native";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import {
  acceptSession,
  api,
  closeSession,
  errorText,
  onSessionExpired,
  restoreSession,
  session,
  sessionEpoch,
} from "./api";
import { native } from "./native";
import type { AuthResult, OAuthPending } from "./types";
import { AccountScreen } from "./AccountScreen";
import { AuthScreen } from "./AuthScreen";
import { BillingScreen } from "./BillingScreen";
import { MonitorsScreen } from "./MonitorsScreen";
import {
  Button,
  Card,
  ErrorNotice,
  GlassPreferences,
  Icon,
  Label,
  Page,
  useGlassEnabled,
  usePalette,
} from "./ui";

type Tab = "monitors" | "billing" | "account";
type Navigation =
  | { type: "payment"; order: string }
  | { type: "incident"; monitorId: string; incidentId: string }
  | { type: "section"; tab: "billing" | "account" };
type OAuthRegistration = {
  registrationToken: string;
  provider: string;
  email: string;
  name: string;
  expiresAt: string;
};
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const nonce = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;

function onlyLink(raw: string) {
  try {
    const url = new URL(raw);
    if (
      url.protocol !== "https:" ||
      url.hostname !== "heartbeat-checker.ru" ||
      url.port ||
      url.username ||
      url.password
    )
      return null;
    return url;
  } catch {
    return null;
  }
}

function exactKeys(params: URLSearchParams, keys: string[]) {
  return (
    params.size === keys.length &&
    keys.every((key) => params.getAll(key).length === 1)
  );
}

function AppContent() {
  const [ready, setReady] = useState(false);
  const [authed, setAuthed] = useState<AuthResult | null>(null);
  const [tab, setTab] = useState<Tab>("monitors");
  const [error, setError] = useState("");
  const [resetToken, setResetToken] = useState<string | null>(null);
  const [authGeneration, setAuthGeneration] = useState(0);
  const [registration, setRegistration] = useState<OAuthRegistration | null>(
    null,
  );
  const [verifyToken, setVerifyToken] = useState<string | null>(null);
  const [monitorLink, setMonitorLink] = useState<{
    monitorId: string;
    incidentId: string;
  } | null>(null);
  const [returnOrder, setReturnOrder] = useState<string | null>(null);
  const [verificationBusy, setVerificationBusy] = useState(false);
  const restoring = useRef(true);
  const earlyLinks = useRef<string[]>([]);
  const queuedNavigation = useRef<Navigation | null>(null);
  const lastLink = useRef({ raw: "", at: 0 });
  const palette = usePalette();
  const glassEnabled = useGlassEnabled();

  const navigate = useCallback((target: Navigation) => {
    if (!session()) {
      queuedNavigation.current = target;
      setError("Войдите в аккаунт, чтобы открыть ссылку.");
      return;
    }
    queuedNavigation.current = null;
    setError("");
    if (target.type === "payment") {
      setReturnOrder(target.order);
      setTab("billing");
    } else if (target.type === "incident") {
      setMonitorLink({
        monitorId: target.monitorId,
        incidentId: target.incidentId,
      });
      setTab("monitors");
    } else setTab(target.tab);
  }, []);

  const takeSession = useCallback(
    async (value: AuthResult) => {
      await acceptSession(value);
      setAuthed(session());
      setTab("monitors");
      setError("");
      setResetToken(null);
      setRegistration(null);
      setVerifyToken(null);
      if (queuedNavigation.current) navigate(queuedNavigation.current);
    },
    [navigate],
  );

  const logout = useCallback(async () => {
    try {
      await closeSession();
    } finally {
      if (!session()) {
        setAuthed(null);
        setMonitorLink(null);
        setReturnOrder(null);
        setTab("monitors");
        setRegistration(null);
        setVerifyToken(null);
        setResetToken(null);
        queuedNavigation.current = null;
      }
    }
  }, []);

  const handleLink = useCallback(
    async (raw: string) => {
      if (native.DEV_CLIENT) return;
      if (restoring.current) {
        earlyLinks.current.push(raw);
        return;
      }
      const url = onlyLink(raw);
      if (!url) return;
      if (
        lastLink.current.raw === raw &&
        Date.now() - lastLink.current.at < 2000
      )
        return;
      lastLink.current = { raw, at: Date.now() };
      try {
        if (url.pathname === "/android/auth/reset") {
          const hash = new URLSearchParams(url.hash.slice(1));
          const token = hash.get("token");
          if (
            url.search ||
            !exactKeys(hash, ["token"]) ||
            !token ||
            !nonce.test(token)
          )
            return;
          // Durable native logout must finish before the reset form can be shown.
          await closeSession();
          queuedNavigation.current = null;
          setAuthed(null);
          setMonitorLink(null);
          setReturnOrder(null);
          setRegistration(null);
          setVerifyToken(null);
          setResetToken(token);
          setAuthGeneration((value) => value + 1);
          setError("");
          return;
        }
        if (url.pathname === "/android/auth/verify") {
          const hash = new URLSearchParams(url.hash.slice(1));
          const token = hash.get("token");
          if (
            !exactKeys(url.searchParams, ["oauth"]) ||
            url.searchParams.get("oauth") !== "1" ||
            !exactKeys(hash, ["token"]) ||
            !token ||
            !nonce.test(token)
          )
            return;
          if (!session()) setVerifyToken(token);
          return;
        }
        if (url.pathname === "/android/payments/return") {
          const order = url.searchParams.get("order");
          if (
            exactKeys(url.searchParams, ["order"]) &&
            order &&
            uuid.test(order) &&
            !url.hash
          )
            navigate({ type: "payment", order: order.toLowerCase() });
          return;
        }
        if (url.pathname === "/app") {
          const monitorId = url.searchParams.get("monitor");
          const incidentId = url.searchParams.get("incident");
          if (
            exactKeys(url.searchParams, ["monitor", "incident"]) &&
            monitorId &&
            incidentId &&
            uuid.test(monitorId) &&
            uuid.test(incidentId) &&
            !url.hash
          )
            navigate({
              type: "incident",
              monitorId: monitorId.toLowerCase(),
              incidentId: incidentId.toLowerCase(),
            });
          return;
        }
        if (
          url.pathname === "/app/settings" ||
          url.pathname === "/app/billing"
        ) {
          if (url.search || url.hash) return;
          if (session()) {
            setTab(url.pathname === "/app/settings" ? "account" : "billing");
            setError("");
          } else {
            queuedNavigation.current = {
              type: "section",
              tab: url.pathname === "/app/settings" ? "account" : "billing",
            };
            setError("Войдите в аккаунт, чтобы открыть раздел.");
          }
          return;
        }
        if (url.pathname !== "/android/auth/complete") return;
        const flowId = url.searchParams.get("flowId");
        const state = url.searchParams.get("state");
        const code = url.searchParams.get("code");
        const deviceId = url.searchParams.get("deviceId");
        const providerError = url.searchParams.get("error");
        if (
          !flowId ||
          !uuid.test(flowId) ||
          !state ||
          !nonce.test(state) ||
          url.hash
        )
          return;
        if (providerError !== null) {
          if (
            !exactKeys(url.searchParams, ["flowId", "state", "error"]) ||
            !["oauth_cancelled", "oauth_provider_unavailable"].includes(
              providerError,
            )
          )
            return;
          const capturedEpoch = sessionEpoch();
          let pending: OAuthPending;
          try {
            pending = await native.takeOAuth(flowId, state);
          } catch {
            return;
          }
          if (
            sessionEpoch() !== capturedEpoch ||
            Date.parse(pending.expiresAt) <= Date.now() ||
            pending.flowId !== flowId ||
            pending.state !== state ||
            (pending.mode === "link" && !session())
          )
            return;
          await native.clearOAuth(flowId, state);
          if (sessionEpoch() !== capturedEpoch) return;
          setError(
            providerError === "oauth_cancelled"
              ? "Вход отменён. Повторите попытку."
              : "Провайдер входа временно недоступен.",
          );
          return;
        }
        if (
          !exactKeys(
            url.searchParams,
            deviceId === null
              ? ["flowId", "state", "code"]
              : ["flowId", "state", "code", "deviceId"],
          ) ||
          !code ||
          code.length > 8192 ||
          (deviceId !== null && (!deviceId || deviceId.length > 4096))
        )
          return;
        const capturedEpoch = sessionEpoch();
        const pending: OAuthPending = await native.takeOAuth(flowId, state);
        if (
          sessionEpoch() !== capturedEpoch ||
          Date.parse(pending.expiresAt) <= Date.now() ||
          pending.flowId !== flowId ||
          pending.state !== state ||
          (pending.mode === "link" && !session())
        )
          return;
        const result = await api<{
          status: string;
          account?: AuthResult["account"];
          session?: AuthResult["session"];
          registration?: {
            provider: string;
            email: string;
            name: string;
            expiresAt: string;
          };
          registrationToken?: string;
        }>("/auth/native/oauth/exchange", {
          method: "POST",
          auth: false,
          body: {
            flowId,
            state,
            code,
            codeVerifier: pending.codeVerifier,
            ...(deviceId === null ? {} : { deviceId }),
          },
        });
        if (sessionEpoch() !== capturedEpoch) return;
        if (
          result.status === "authenticated" &&
          result.account &&
          result.session
        )
          await takeSession({
            account: result.account,
            session: result.session,
          });
        else if (
          result.status === "email_verification_required" &&
          result.registration &&
          result.registrationToken &&
          !session()
        ) {
          const pendingRegistration = {
            ...result.registration,
            registrationToken: result.registrationToken,
          };
          await native.saveOAuthRegistration(pendingRegistration);
          setRegistration(pendingRegistration);
          setVerifyToken(null);
        }
        await native.clearOAuth(flowId, state);
      } catch (cause) {
        setError(errorText(cause));
      }
    },
    [navigate, takeSession],
  );

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const [saved, pending, link] = await Promise.all([
          restoreSession(),
          native.restoreOAuthRegistration(),
          native.takeInitialLink(),
        ]);
        if (!active) return;
        setAuthed(saved);
        setRegistration(saved ? null : pending);
        setReady(true);
        restoring.current = false;
        const links = [...earlyLinks.current, ...(link ? [link] : [])];
        earlyLinks.current = [];
        for (const item of links) await handleLink(item);
      } catch (cause) {
        if (active) {
          restoring.current = false;
          setError(errorText(cause));
          setReady(true);
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [handleLink]);
  useEffect(() => {
    const listener = Linking.addEventListener("url", (event) => {
      void handleLink(event.url);
    });
    return () => listener.remove();
  }, [handleLink]);
  useEffect(() => {
    onSessionExpired(() => {
      queuedNavigation.current = null;
      setAuthed(null);
      setMonitorLink(null);
      setReturnOrder(null);
      setRegistration(null);
      setVerifyToken(null);
      setResetToken(null);
      setTab("monitors");
      setError("Сессия истекла. Войдите снова.");
    });
    return () => onSessionExpired(null);
  }, []);
  useEffect(() => {
    const listener = BackHandler.addEventListener("hardwareBackPress", () => {
      if (!authed || tab === "monitors") return false;
      setTab("monitors");
      return true;
    });
    return () => listener.remove();
  }, [authed, tab]);

  const confirmOAuthEmail = () => {
    if (!registration || !verifyToken || verificationBusy || session()) return;
    const capturedEpoch = sessionEpoch();
    setVerificationBusy(true);
    setError("");
    void api<AuthResult>("/auth/native/oauth/registration/confirm", {
      method: "POST",
      auth: false,
      body: {
        registrationToken: registration.registrationToken,
        token: verifyToken,
      },
    })
      .then(async (result) => {
        if (sessionEpoch() !== capturedEpoch) return;
        await native.clearOAuthRegistration();
        await takeSession(result);
      })
      .catch((cause) => setError(errorText(cause)))
      .finally(() => setVerificationBusy(false));
  };

  if (!ready)
    return (
      <View
        style={{
          flex: 1,
          justifyContent: "center",
          alignItems: "center",
          backgroundColor: palette.bg,
        }}
      >
        <ActivityIndicator size="large" color={palette.accent} />
      </View>
    );
  if (registration && !authed)
    return (
      <Page title="Подтвердите email">
        <Card>
          <Label>{registration.email}</Label>
          <Label>
            Мы отправили письмо. Откройте ссылку на этом телефоне, затем
            вернитесь в приложение.
          </Label>
          <Button
            label="Подтвердить полученную ссылку"
            onPress={confirmOAuthEmail}
            disabled={!verifyToken || verificationBusy}
          />
          {verifyToken ? (
            <Label>
              Ссылка получена. Подтверждение займёт несколько секунд.
            </Label>
          ) : null}
          <Button
            label="Начать заново"
            secondary
            onPress={() =>
              void native.clearOAuthRegistration().then(() => {
                setRegistration(null);
                setVerifyToken(null);
              })
            }
          />
        </Card>
        <ErrorNotice message={error} />
      </Page>
    );
  if (!authed)
    return (
      <>
        <StatusBar barStyle={palette.dark ? "light-content" : "dark-content"} />
        <AuthScreen
          key={authGeneration}
          onAuthed={takeSession}
          resetToken={resetToken}
        />
        <ErrorNotice message={error} />
      </>
    );
  return (
    <View style={{ flex: 1, backgroundColor: palette.bg }}>
      <StatusBar barStyle={palette.dark ? "light-content" : "dark-content"} />
      <View style={{ flex: 1 }}>
        {tab === "monitors" ? (
          <MonitorsScreen deepLink={monitorLink} />
        ) : tab === "billing" ? (
          <BillingScreen returnOrder={returnOrder} />
        ) : (
          <AccountScreen onLogout={logout} />
        )}
      </View>
      <View
        accessibilityRole="tablist"
        style={{
          flexDirection: "row",
          borderTopWidth: 1,
          borderTopColor: glassEnabled ? palette.glassBorder : palette.border,
          backgroundColor: glassEnabled ? palette.glass : palette.card,
          elevation: 5,
        }}
      >
        {(
          [
            ["monitors", "Мониторы", "activity"],
            ["billing", "Оплата", "payment"],
            ["account", "Аккаунт", "user"],
          ] as const
        ).map(([key, label, icon]) => (
          <Pressable
            key={key}
            accessibilityRole="tab"
            accessibilityState={{ selected: tab === key }}
            onPress={() => setTab(key)}
            style={{
              flex: 1,
              minHeight: 64,
              paddingVertical: 8,
              alignItems: "center",
              justifyContent: "center",
              gap: 3,
            }}
          >
            <Icon
              name={icon}
              color={tab === key ? palette.accentText : palette.subtle}
            />
            <Text
              style={{
                fontSize: 14,
                color: tab === key ? palette.accentText : palette.subtle,
                fontWeight: tab === key ? "700" : "500",
              }}
            >
              {label}
            </Text>
          </Pressable>
        ))}
      </View>
      <ErrorNotice message={error} />
    </View>
  );
}

export default function App() {
  return (
    <SafeAreaProvider>
      <SafeAreaView style={{ flex: 1 }} edges={["top", "bottom"]}>
        <GlassPreferences>
          {native.DEV_CLIENT ? (
            <View style={{ backgroundColor: "#6a4000", padding: 8 }}>
              <Text
                style={{
                  color: "#ffffff",
                  textAlign: "center",
                  fontWeight: "700",
                }}
              >
                DEV · Metro по Wi-Fi · оплата и push выключены
              </Text>
            </View>
          ) : null}
          <AppContent />
        </GlassPreferences>
      </SafeAreaView>
    </SafeAreaProvider>
  );
}
