import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  BackHandler,
  KeyboardAvoidingView,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  useWindowDimensions,
  View,
} from "react-native";
import {
  WebView,
  WebViewMessageEvent,
  WebViewNavigation,
} from "react-native-webview";
import { api, errorText, ORIGIN } from "./api";
import { native } from "./native";
import type { AuthResult, Provider, Registration } from "./types";
import {
  BrandHeader,
  Button,
  Card,
  ErrorNotice,
  Field,
  GlassBackdrop,
  Label,
  Notice,
  SignalAccent,
  TextAction,
  Title,
  usePalette,
} from "./ui";

type Mode = "login" | "register" | "confirm" | "reset" | "reset-confirm";
type SavedRegistration = Registration & { receivedAt?: number };
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
  initialMode = "login",
  initialEmail = "",
  resumeRegistration = true,
  onBack,
  onHelp,
}: {
  onAuthed: (value: AuthResult) => Promise<void>;
  resetToken?: string | null;
  initialMode?: "login" | "register" | "reset";
  initialEmail?: string;
  resumeRegistration?: boolean;
  onBack?: () => void;
  onHelp?: () => void;
}) {
  const [mode, setMode] = useState<Mode>(
    resetToken ? "reset-confirm" : initialMode,
  );
  const [email, setEmail] = useState(initialEmail);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [code, setCode] = useState("");
  const [registration, setRegistration] = useState<SavedRegistration | null>(
    null,
  );
  const [captcha, setCaptcha] = useState<CaptchaAction | null>(null);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const [busyAction, setBusyAction] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [secondsUntilResend, setSecondsUntilResend] = useState(0);
  const { width } = useWindowDimensions();
  // Width stays stable when the keyboard resizes the window; every auth mode shares this minimum.
  const formMinHeight = Math.max(620, Math.min(760, width * 2));
  const challengeClock = useRef<{ server: number; monotonic: number } | null>(
    null,
  );
  const restorePending = useRef(true);
  const palette = usePalette();
  const rememberRegistration = useCallback(
    (value: SavedRegistration, fresh: boolean) => {
      const server = Date.parse(value.challenge.serverNow);
      const receivedAt = fresh ? Date.now() : value.receivedAt;
      // Keep a server-based clock for this process. Older saved challenges lack
      // a receipt timestamp and fall back to the device's absolute time.
      const elapsed =
        receivedAt && Number.isFinite(receivedAt)
          ? Math.max(0, Date.now() - receivedAt)
          : 0;
      const estimatedServer =
        Number.isFinite(server) && receivedAt ? server + elapsed : Date.now();
      challengeClock.current = {
        server: estimatedServer,
        monotonic: performance.now(),
      };
      setRegistration({ ...value, ...(receivedAt ? { receivedAt } : {}) });
      const deadline = Date.parse(value.challenge.resendAvailableAt);
      setSecondsUntilResend(
        Number.isFinite(deadline)
          ? Math.max(0, Math.ceil((deadline - estimatedServer) / 1000))
          : 0,
      );
    },
    [],
  );
  useEffect(() => {
    if (resetToken) {
      restorePending.current = false;
      setMode("reset-confirm");
      setError("");
      setNotice("");
    }
  }, [resetToken]);
  useEffect(() => {
    if (resetToken || initialMode === "reset" || !resumeRegistration) return;
    let live = true;
    void native
      .restoreRegistration()
      .then((saved) => {
        if (live && restorePending.current && saved) {
          rememberRegistration(saved, false);
          setMode("confirm");
          setEmail(saved.challenge.email);
        }
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [resetToken, initialMode, resumeRegistration, rememberRegistration]);
  useEffect(() => {
    if (mode !== "confirm" || !registration || !challengeClock.current) return;
    const clock = challengeClock.current;
    const deadline = Date.parse(registration.challenge.resendAvailableAt);
    const tick = () =>
      setSecondsUntilResend(
        Number.isFinite(deadline)
          ? Math.max(
              0,
              Math.ceil(
                (deadline -
                  clock.server -
                  (performance.now() - clock.monotonic)) /
                  1000,
              ),
            )
          : 0,
      );
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [mode, registration]);

  const run = useCallback(async (work: () => Promise<void>, action = "") => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setBusyAction(action);
    setError("");
    setNotice("");
    try {
      await work();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      busyRef.current = false;
      setBusy(false);
      setBusyAction("");
    }
  }, []);

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
        const saved = { ...result, receivedAt: Date.now() };
        await native.saveRegistration(saved);
        setPassword("");
        rememberRegistration(saved, true);
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
        const updated = {
          ...registration,
          challenge: result.challenge,
          receivedAt: Date.now(),
        };
        await native.saveRegistration(updated);
        rememberRegistration(updated, true);
        setCode("");
        setNotice("Новый код отправлен. Используйте только последнее письмо.");
        return;
      }
      if (action === "reset") {
        if (native.DEV_CLIENT) return;
        await api("/auth/native/password/reset/request", {
          method: "POST",
          auth: false,
          body: { email: email.trim(), turnstileToken: token },
        });
        setNotice(
          "Если аккаунт существует, письмо со ссылкой отправлено. Откройте ссылку на этом устройстве.",
        );
      }
    }, action);
  };

  const oauth = (provider: Provider) =>
    void run(async () => {
      if (native.DEV_CLIENT) return;
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
    }, provider);

  const changeMode = (next: Mode) => {
    restorePending.current = false;
    setError("");
    setNotice("");
    setPassword("");
    setNewPassword("");
    setCode("");
    setMode(next);
  };
  const openLegal = (path: "/terms" | "/privacy") =>
    void run(() => native.openCustomTab(`${ORIGIN}${path}`), path);
  const submit = () => {
    if (busyRef.current) return;
    restorePending.current = false;
    if (mode === "login" && email.trim() && password) setCaptcha("login");
    else if (mode === "register" && name.trim() && email.trim() && password)
      setCaptcha("register");
    else if (mode === "reset" && email.trim() && !native.DEV_CLIENT)
      setCaptcha("reset");
    else if (mode === "confirm" && code.length === 6 && registration)
      setCaptcha("registration_confirm");
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
      <GlassBackdrop />
      <ScrollView
        keyboardShouldPersistTaps="handled"
        contentContainerStyle={styles.screenContent}
      >
        <View style={styles.group}>
          <BrandHeader centered />
          <Card style={[styles.formCard, { minHeight: formMinHeight }]}>
            <SignalAccent motionKey={mode} />
            {native.DEV_CLIENT ? (
              <Label muted>
                Dev-сборка: вход и регистрация по email. Социальный вход и
                восстановление пароля доступны в обычном приложении.
              </Label>
            ) : null}
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
            <Label muted>
              {mode === "login"
                ? "Войдите, чтобы видеть свои мониторы и инциденты."
                : mode === "register"
                  ? "Подтвердите email, чтобы начать."
                  : mode === "reset"
                    ? "Отправим ссылку для установки нового пароля."
                    : mode === "reset-confirm"
                      ? "Задайте новый пароль для своего аккаунта."
                      : "Проверьте входящие письма и папку «Спам»."}
            </Label>
            {mode === "register" ? (
              <Field
                label="Имя"
                value={name}
                onChangeText={setName}
                autoComplete="name"
                textContentType="name"
                hint="От 2 до 100 символов."
                editable={!busy}
              />
            ) : null}
            {mode === "login" || mode === "register" || mode === "reset" ? (
              <Field
                label="Email"
                value={email}
                onChangeText={setEmail}
                keyboardType="email-address"
                autoComplete="email"
                textContentType="emailAddress"
                placeholder="you@example.com"
                editable={!busy}
                returnKeyType={mode === "reset" ? "send" : "next"}
                onSubmitEditing={mode === "reset" ? submit : undefined}
              />
            ) : null}
            {mode === "login" || mode === "register" ? (
              <Field
                label="Пароль"
                value={password}
                onChangeText={setPassword}
                secure
                autoComplete={
                  mode === "register" ? "new-password" : "current-password"
                }
                textContentType={
                  mode === "register" ? "newPassword" : "password"
                }
                hint={
                  mode === "register"
                    ? "От 12 до 1024 байт UTF-8. Русские буквы — обычно по 2 байта. Спецсимволы не обязательны."
                    : undefined
                }
                editable={!busy}
                returnKeyType="go"
                onSubmitEditing={submit}
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
                  autoComplete="one-time-code"
                  textContentType="oneTimeCode"
                  maxLength={6}
                  editable={!busy}
                  hint="Введите код из последнего письма."
                  returnKeyType="go"
                  onSubmitEditing={submit}
                />
              </>
            ) : null}
            {mode === "reset-confirm" ? (
              <Field
                label="Новый пароль"
                value={newPassword}
                onChangeText={setNewPassword}
                secure
                autoComplete="new-password"
                textContentType="newPassword"
                hint="От 12 до 1024 байт UTF-8: например, 12 латинских символов или 6 русских букв. Используйте уникальную фразу."
                editable={!busy}
              />
            ) : null}
            <ErrorNotice message={error} />
            {notice ? <Notice message={notice} tone="success" /> : null}
            {mode === "login" ? (
              <Button
                label="Войти"
                disabled={busy || !email || !password}
                onPress={submit}
                loading={busyAction === "login"}
                loadingLabel="Входим…"
              />
            ) : null}
            {mode === "register" ? (
              <Button
                label="Создать аккаунт"
                disabled={busy || !email || !name || !password}
                onPress={submit}
                loading={busyAction === "register"}
                loadingLabel="Создаём аккаунт…"
              />
            ) : null}
            {mode === "confirm" ? (
              <>
                <Button
                  label="Подтвердить"
                  disabled={busy || code.length !== 6}
                  onPress={submit}
                  loading={busyAction === "registration_confirm"}
                  loadingLabel="Подтверждаем…"
                />
                <Button
                  label={
                    secondsUntilResend > 0
                      ? `Новый код через ${secondsUntilResend} с`
                      : "Отправить код снова"
                  }
                  disabled={busy || !registration || secondsUntilResend > 0}
                  onPress={() => setCaptcha("registration_resend")}
                  secondary
                  loading={busyAction === "registration_resend"}
                  loadingLabel="Отправляем код…"
                />
              </>
            ) : null}
            {mode === "reset" && !native.DEV_CLIENT ? (
              <Button
                label="Отправить ссылку"
                disabled={busy || !email}
                onPress={submit}
                loading={busyAction === "reset"}
                loadingLabel="Отправляем ссылку…"
              />
            ) : null}
            {mode === "reset-confirm" ? (
              <Button
                label="Сохранить пароль"
                disabled={busy || !resetToken || !newPassword}
                loading={busyAction === "reset-confirm"}
                loadingLabel="Сохраняем пароль…"
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
                  }, "reset-confirm")
                }
              />
            ) : null}
            {mode !== "login" ? (
              <Button
                label="Ко входу"
                disabled={busy}
                onPress={() => {
                  changeMode("login");
                  if (mode === "confirm") {
                    setRegistration(null);
                    challengeClock.current = null;
                    void native
                      .clearRegistration()
                      .catch((cause) => setError(errorText(cause)));
                  }
                }}
                secondary
              />
            ) : null}
            {mode === "login" ? (
              <View style={styles.authLinks}>
                <Pressable
                  accessibilityRole="button"
                  accessibilityState={{ disabled: busy }}
                  disabled={busy}
                  onPress={() => changeMode("register")}
                  style={styles.authLink}
                >
                  <Text
                    style={[styles.authLinkText, { color: palette.accentText }]}
                  >
                    Регистрация
                  </Text>
                </Pressable>
                {!native.DEV_CLIENT ? (
                  <Pressable
                    accessibilityRole="button"
                    accessibilityState={{ disabled: busy }}
                    disabled={busy}
                    onPress={() => changeMode("reset")}
                    style={styles.authLink}
                  >
                    <Text
                      style={[
                        styles.authLinkText,
                        { color: palette.accentText },
                      ]}
                    >
                      Забыли пароль?
                    </Text>
                  </Pressable>
                ) : null}
              </View>
            ) : null}
            {mode === "login" && !native.DEV_CLIENT ? (
              <>
                <Button
                  label="Войти через Яндекс"
                  onPress={() => oauth("yandex")}
                  disabled={busy}
                  secondary
                  iconName="yandex"
                  loading={busyAction === "yandex"}
                  loadingLabel="Открываем Яндекс…"
                />
                <Button
                  label="Войти через VK"
                  onPress={() => oauth("vk")}
                  disabled={busy}
                  secondary
                  iconName="vk"
                  loading={busyAction === "vk"}
                  loadingLabel="Открываем VK…"
                />
              </>
            ) : null}
          </Card>
          <View style={styles.footer}>
            {mode === "register" ? (
              <Label muted>
                Создавая аккаунт, вы принимаете условия использования. Обработка
                данных описана в политике конфиденциальности.
              </Label>
            ) : null}
            <TextAction
              label="Условия использования"
              onPress={() => openLegal("/terms")}
              disabled={busy}
              iconName="arrow-up-right"
            />
            <TextAction
              label="Политика конфиденциальности"
              onPress={() => openLegal("/privacy")}
              disabled={busy}
              iconName="arrow-up-right"
            />
            {onHelp ? (
              <TextAction
                label="Помощь и поддержка"
                onPress={onHelp}
                disabled={busy}
                iconName="info"
              />
            ) : null}
            {onBack ? (
              <TextAction
                label="О сервисе"
                onPress={onBack}
                disabled={busy}
                iconName="arrow-left"
              />
            ) : null}
          </View>
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
  formCard: { paddingVertical: 22, gap: 14 },
  footer: { gap: 0 },
  authLinks: { flexDirection: "row", gap: 8 },
  authLink: {
    flex: 1,
    minHeight: 48,
    alignItems: "center",
    justifyContent: "center",
  },
  authLinkText: { fontSize: 14, fontWeight: "600", textAlign: "center" },
  challengeTitle: { fontSize: 23, fontWeight: "700", textAlign: "center" },
  challengeFrame: { height: 340, overflow: "hidden", borderRadius: 16 },
});
