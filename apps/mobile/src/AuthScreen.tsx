import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  BackHandler,
  Image,
  KeyboardAvoidingView,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";
import {
  WebView,
  WebViewMessageEvent,
  WebViewNavigation,
} from "react-native-webview";
import { api, errorText } from "./api";
import { native } from "./native";
import type { AuthResult, Provider, Registration } from "./types";
import {
  Button,
  Card,
  ErrorNotice,
  Field,
  Label,
  Title,
  usePalette,
} from "./ui";

type Mode = "login" | "register" | "confirm" | "reset" | "reset-confirm";
type CaptchaAction =
  | "login"
  | "register"
  | "registration_confirm"
  | "registration_resend"
  | "reset";
const captchaActions: CaptchaAction[] = [
  "login",
  "register",
  "registration_confirm",
  "registration_resend",
  "reset",
];

function Captcha({
  action,
  onToken,
  onCancel,
}: {
  action: CaptchaAction;
  onToken: (token: string) => void;
  onCancel: () => void;
}) {
  const [requestId, setRequestId] = useState("");
  const [error, setError] = useState("");
  const consumed = useRef(false);
  const live = useRef(true);
  const nonceGeneration = useRef(0);
  const webView = useRef<WebView>(null);
  const palette = usePalette();
  const cancel = useCallback(() => {
    consumed.current = true;
    nonceGeneration.current++;
    onCancel();
  }, [onCancel]);
  useEffect(() => {
    live.current = true;
    consumed.current = false;
    const generation = ++nonceGeneration.current;
    void native
      .randomToken()
      .then((value) => {
        if (live.current && generation === nonceGeneration.current)
          setRequestId(value);
      })
      .catch(() => {
        if (live.current && generation === nonceGeneration.current)
          setError("Не удалось запустить проверку.");
      });
    return () => {
      live.current = false;
      nonceGeneration.current++;
      consumed.current = true;
    };
  }, []);
  useEffect(() => {
    const listener = BackHandler.addEventListener("hardwareBackPress", () => {
      cancel();
      return true;
    });
    return () => listener.remove();
  }, [cancel]);
  const retry = () => {
    consumed.current = true;
    setRequestId("");
    setError("");
    const generation = ++nonceGeneration.current;
    void native
      .randomToken()
      .then((value) => {
        if (!live.current || generation !== nonceGeneration.current) return;
        consumed.current = false;
        setRequestId(value);
      })
      .catch(() => {
        if (live.current && generation === nonceGeneration.current)
          setError("Не удалось запустить проверку.");
      });
  };
  const origin = native.CHALLENGE_ORIGIN;
  const url = `${origin}/mobile/challenge?action=${action}&requestId=${encodeURIComponent(requestId)}`;
  const renderGeneration = nonceGeneration.current;
  const ownDocument = (value: string) =>
    value === url || value === `${origin}/mobile/challenge`;
  const providerResource = (value: string) => {
    try {
      const parsed = new URL(value);
      return (
        parsed.protocol === "https:" &&
        parsed.hostname === "challenges.cloudflare.com" &&
        !parsed.port &&
        !parsed.username &&
        !parsed.password &&
        !parsed.hash
      );
    } catch {
      return false;
    }
  };
  const allow = (nav: WebViewNavigation) =>
    renderGeneration === nonceGeneration.current &&
    (ownDocument(nav.url) ||
      (providerResource(nav.url) &&
        (nav as WebViewNavigation & { isTopFrame?: boolean }).isTopFrame !==
          true) ||
      nav.url === "about:blank" ||
      nav.url === "about:srcdoc");
  const invalidate = (message: string) => {
    consumed.current = true;
    nonceGeneration.current++;
    setRequestId("");
    setError(message);
  };
  const guardTopLevel = (value: string) => {
    if (renderGeneration !== nonceGeneration.current || consumed.current)
      return;
    if (!ownDocument(value) && value !== "about:blank") {
      webView.current?.stopLoading();
      invalidate("Проверка открыла другую страницу. Начните её заново.");
    }
  };
  const receive = (event: WebViewMessageEvent) => {
    if (
      renderGeneration !== nonceGeneration.current ||
      consumed.current ||
      !requestId
    )
      return;
    try {
      const message = JSON.parse(event.nativeEvent.data);
      const mainFrame = (
        event.nativeEvent as typeof event.nativeEvent & {
          isMainFrame?: boolean;
        }
      ).isMainFrame;
      const trustedSource =
        event.nativeEvent.url === origin ||
        event.nativeEvent.url === origin + "/" ||
        ownDocument(event.nativeEvent.url);
      if (
        !trustedSource ||
        mainFrame === false ||
        message.action !== action ||
        message.requestId !== requestId
      ) {
        invalidate("Проверка не прошла. Повторите её.");
        return;
      }
      if (
        message.type === "turnstile_error" &&
        ["unavailable", "failed", "expired", "timeout"].includes(message.code)
      ) {
        setError("Проверка не завершилась. Повторите её.");
        return;
      }
      if (
        message.type === "turnstile" &&
        typeof message.token === "string" &&
        message.token.length > 10 &&
        message.token.length <= 2048
      ) {
        consumed.current = true;
        onToken(message.token);
      } else {
        invalidate("Проверка не прошла. Повторите её.");
      }
    } catch {
      invalidate("Некорректный ответ проверки.");
    }
  };
  return (
    <View style={[styles.screen, { backgroundColor: palette.bg }]}>
      <ScrollView contentContainerStyle={styles.screenContent}>
        <View style={styles.group}>
          <Text style={[styles.challengeTitle, { color: palette.text }]}>
            Проверка безопасности
          </Text>
          <ErrorNotice message={error} />
          {requestId ? (
            <View style={styles.challengeFrame}>
              <WebView
                ref={webView}
                source={{ uri: url }}
                originWhitelist={["*"]}
                onShouldStartLoadWithRequest={allow}
                onNavigationStateChange={(nav) => guardTopLevel(nav.url)}
                onLoadStart={(event) => guardTopLevel(event.nativeEvent.url)}
                onMessage={receive}
                javaScriptEnabled
                domStorageEnabled
                thirdPartyCookiesEnabled
                sharedCookiesEnabled={false}
                setSupportMultipleWindows={false}
                onError={() => {
                  if (
                    renderGeneration === nonceGeneration.current &&
                    !consumed.current
                  )
                    setError("Не удалось открыть проверку.");
                }}
              />
            </View>
          ) : null}
          {error ? (
            <Button label="Повторить проверку" onPress={retry} secondary />
          ) : null}
          <Button label="Отмена" onPress={cancel} secondary />
        </View>
      </ScrollView>
    </View>
  );
}

export function AuthScreen({
  onAuthed,
  resetToken,
}: {
  onAuthed: (value: AuthResult) => Promise<void>;
  resetToken?: string | null;
}) {
  const [mode, setMode] = useState<Mode>(
    resetToken ? "reset-confirm" : "login",
  );
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [code, setCode] = useState("");
  const [registration, setRegistration] = useState<Registration | null>(null);
  const [captcha, setCaptcha] = useState<CaptchaAction | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const palette = usePalette();
  useEffect(() => {
    if (resetToken) return;
    let live = true;
    void native
      .restoreRegistration()
      .then((saved) => {
        if (live && saved) {
          setRegistration(saved);
          setMode("confirm");
          setEmail(saved.challenge.email);
        }
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [resetToken]);

  const run = useCallback(
    async (work: () => Promise<void>) => {
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
    },
    [busy],
  );

  const captchaResult = (token: string) => {
    const action = captcha;
    setCaptcha(null);
    if (!action || !captchaActions.includes(action)) return;
    void run(async () => {
      if (action === "login") {
        const result = await api<AuthResult>("/auth/native/login", {
          method: "POST",
          auth: false,
          body: { email: email.trim(), password, turnstileToken: token },
        });
        await onAuthed(result);
        setPassword("");
        return;
      }
      if (action === "register") {
        const result = await api<Registration>("/auth/native/registration", {
          method: "POST",
          auth: false,
          body: {
            name: name.trim(),
            email: email.trim(),
            password,
            turnstileToken: token,
          },
        });
        await native.saveRegistration(result);
        setPassword("");
        setRegistration(result);
        setMode("confirm");
        setNotice("Код отправлен на email. Введите шесть цифр.");
        return;
      }
      if (action === "registration_confirm" && registration) {
        const result = await api<AuthResult>(
          `/auth/native/registration/${registration.challenge.id}/confirm`,
          {
            method: "POST",
            auth: false,
            body: {
              registrationToken: registration.registrationToken,
              code,
              turnstileToken: token,
            },
          },
        );
        await onAuthed(result);
        setPassword("");
        return;
      }
      if (action === "registration_resend" && registration) {
        const result = await api<{ challenge: Registration["challenge"] }>(
          `/auth/native/registration/${registration.challenge.id}/resend`,
          {
            method: "POST",
            auth: false,
            body: {
              registrationToken: registration.registrationToken,
              turnstileToken: token,
            },
          },
        );
        const updated = { ...registration, challenge: result.challenge };
        await native.saveRegistration(updated);
        setRegistration(updated);
        setNotice("Новый код отправлен. Используйте только последнее письмо.");
        return;
      }
      if (action === "reset") {
        await api("/auth/native/password/reset/request", {
          method: "POST",
          auth: false,
          body: { email: email.trim(), turnstileToken: token },
        });
        setNotice(
          "Если аккаунт существует, письмо со ссылкой отправлено. Откройте ссылку на этом устройстве.",
        );
      }
    });
  };

  const oauth = (provider: Provider) =>
    void run(async () => {
      const proof = await native.beginOAuth();
      const result = await api<{
        flowId: string;
        authorizationUrl: string;
        expiresAt: string;
      }>(`/auth/native/oauth/${provider}/start`, {
        method: "POST",
        auth: false,
        body: {
          mode: "login",
          state: proof.state,
          codeChallenge: proof.codeChallenge,
        },
      });
      await native.saveOAuth({
        flowId: result.flowId,
        provider,
        state: proof.state,
        codeVerifier: "",
        expiresAt: result.expiresAt,
        mode: "login",
      });
      await native.openCustomTab(result.authorizationUrl);
    });

  const changeMode = (next: Mode) => {
    setError("");
    setNotice("");
    setMode(next);
  };

  if (captcha)
    return (
      <Captcha
        action={captcha}
        onToken={captchaResult}
        onCancel={() => setCaptcha(null)}
      />
    );
  return (
    <KeyboardAvoidingView
      style={[styles.screen, { backgroundColor: palette.bg }]}
      behavior="height"
    >
      <ScrollView
        keyboardShouldPersistTaps="handled"
        contentContainerStyle={styles.screenContent}
      >
        <View style={styles.group}>
          <View style={styles.brand}>
            <Image
              source={require("../assets/logo.png")}
              style={styles.logo}
              accessibilityLabel="Логотип Heartbeat Checker"
            />
            <Text style={[styles.brandText, { color: palette.text }]}>
              Heartbeat Checker
            </Text>
          </View>
          <Card style={styles.formCard}>
            <Title>
              {mode === "login"
                ? "Вход"
                : mode === "register"
                  ? "Регистрация"
                  : mode === "confirm"
                    ? "Подтверждение email"
                    : mode === "reset"
                      ? "Восстановление доступа"
                      : "Новый пароль"}
            </Title>
            {mode === "register" ? (
              <Field label="Имя" value={name} onChangeText={setName} />
            ) : null}
            {mode === "login" || mode === "register" || mode === "reset" ? (
              <Field
                label="Email"
                value={email}
                onChangeText={setEmail}
                keyboardType="email-address"
              />
            ) : null}
            {mode === "login" || mode === "register" ? (
              <Field
                label="Пароль"
                value={password}
                onChangeText={setPassword}
                secure
              />
            ) : null}
            {mode === "confirm" ? (
              <>
                <Label>Код для {registration?.challenge.email}</Label>
                <Field
                  label="Шесть цифр"
                  value={code}
                  onChangeText={(value) =>
                    setCode(value.replace(/\D/g, "").slice(0, 6))
                  }
                  keyboardType="number-pad"
                />
              </>
            ) : null}
            {mode === "reset-confirm" ? (
              <Field
                label="Новый пароль"
                value={newPassword}
                onChangeText={setNewPassword}
                secure
              />
            ) : null}
            <ErrorNotice message={error} />
            {notice ? <Label>{notice}</Label> : null}
            {mode === "login" ? (
              <Button
                label="Войти"
                disabled={busy || !email || !password}
                onPress={() => setCaptcha("login")}
              />
            ) : null}
            {mode === "register" ? (
              <Button
                label="Создать аккаунт"
                disabled={busy || !email || !name || !password}
                onPress={() => setCaptcha("register")}
              />
            ) : null}
            {mode === "confirm" ? (
              <>
                <Button
                  label="Подтвердить"
                  disabled={busy || code.length !== 6}
                  onPress={() => setCaptcha("registration_confirm")}
                />
                <Button
                  label="Отправить код снова"
                  disabled={
                    busy ||
                    !registration ||
                    Date.parse(registration.challenge.resendAvailableAt) >
                      Date.now()
                  }
                  onPress={() => setCaptcha("registration_resend")}
                  secondary
                />
              </>
            ) : null}
            {mode === "reset" ? (
              <Button
                label="Отправить ссылку"
                disabled={busy || !email}
                onPress={() => setCaptcha("reset")}
              />
            ) : null}
            {mode === "reset-confirm" ? (
              <Button
                label="Сохранить пароль"
                disabled={busy || !resetToken || !newPassword}
                onPress={() =>
                  void run(async () => {
                    await api("/auth/native/password/reset/confirm", {
                      method: "POST",
                      auth: false,
                      body: { token: resetToken, newPassword },
                    });
                    setNewPassword("");
                    setMode("login");
                    setNotice("Пароль изменён. Войдите с новым паролем.");
                  })
                }
              />
            ) : null}
            {mode !== "login" ? (
              <Button
                label="Ко входу"
                onPress={() => {
                  changeMode("login");
                  if (mode === "confirm") void native.clearRegistration();
                }}
                secondary
              />
            ) : null}
            {mode === "login" ? (
              <View style={styles.authLinks}>
                <Pressable
                  accessibilityRole="button"
                  onPress={() => changeMode("register")}
                  style={styles.authLink}
                >
                  <Text
                    style={[styles.authLinkText, { color: palette.accent }]}
                  >
                    Регистрация
                  </Text>
                </Pressable>
                <Pressable
                  accessibilityRole="button"
                  onPress={() => changeMode("reset")}
                  style={styles.authLink}
                >
                  <Text
                    style={[styles.authLinkText, { color: palette.accent }]}
                  >
                    Забыли пароль?
                  </Text>
                </Pressable>
              </View>
            ) : null}
            {mode === "login" ? (
              <>
                <Button
                  label="Войти через Яндекс"
                  onPress={() => oauth("yandex")}
                  disabled={busy}
                  secondary
                  icon={
                    <Image
                      source={require("../assets/yandex.png")}
                      style={styles.providerIcon}
                    />
                  }
                />
                <Button
                  label="Войти через VK"
                  onPress={() => oauth("vk")}
                  disabled={busy}
                  secondary
                  icon={
                    <Image
                      source={require("../assets/vk.png")}
                      style={styles.providerIcon}
                    />
                  }
                />
              </>
            ) : null}
          </Card>
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  screenContent: {
    flexGrow: 1,
    justifyContent: "center",
    padding: 18,
    paddingBottom: 32,
  },
  group: { width: "100%", maxWidth: 420, alignSelf: "center", gap: 14 },
  brand: { alignItems: "center", gap: 10, marginBottom: 4 },
  logo: { width: 68, height: 68, borderRadius: 18 },
  brandText: { fontSize: 23, fontWeight: "700", textAlign: "center" },
  formCard: { minHeight: 520, justifyContent: "center" },
  authLinks: { flexDirection: "row", gap: 8 },
  authLink: {
    flex: 1,
    minHeight: 48,
    alignItems: "center",
    justifyContent: "center",
  },
  authLinkText: { fontSize: 14, fontWeight: "600", textAlign: "center" },
  providerIcon: { width: 24, height: 24, borderRadius: 5 },
  challengeTitle: { fontSize: 23, fontWeight: "700", textAlign: "center" },
  challengeFrame: { height: 340, overflow: "hidden", borderRadius: 16 },
});
