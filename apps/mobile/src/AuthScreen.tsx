import React, { useCallback, useEffect, useRef, useState } from "react";
import { View } from "react-native";
import {
  WebView,
  WebViewMessageEvent,
  WebViewNavigation,
} from "react-native-webview";
import { api, errorText } from "./api";
import { native } from "./native";
import type { AuthResult, Provider, Registration } from "./types";
import { Button, Card, ErrorNotice, Field, Label, Page, Title } from "./ui";

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
  useEffect(() => {
    let mounted = true;
    consumed.current = false;
    void native
      .randomToken()
      .then((value) => {
        if (mounted) setRequestId(value);
      })
      .catch(() => {
        if (mounted) setError("Не удалось запустить проверку.");
      });
    return () => {
      mounted = false;
      consumed.current = true;
    };
  }, []);
  const origin = native.CHALLENGE_ORIGIN;
  const url = `${origin}/mobile/challenge?action=${action}&requestId=${encodeURIComponent(requestId)}`;
  const allow = (nav: WebViewNavigation) =>
    nav.url === url || nav.url === "about:blank";
  const receive = (event: WebViewMessageEvent) => {
    if (consumed.current || !requestId) return;
    try {
      const message = JSON.parse(event.nativeEvent.data);
      const mainFrame = (
        event.nativeEvent as typeof event.nativeEvent & {
          isMainFrame?: boolean;
        }
      ).isMainFrame;
      if (
        (event.nativeEvent.url === origin ||
          event.nativeEvent.url === origin + "/" ||
          event.nativeEvent.url === url) &&
        mainFrame !== false &&
        message.type === "turnstile" &&
        message.action === action &&
        message.requestId === requestId &&
        typeof message.token === "string" &&
        message.token.length > 10 &&
        message.token.length <= 4096
      ) {
        consumed.current = true;
        onToken(message.token);
      } else setError("Проверка не прошла. Повторите её.");
    } catch {
      setError("Некорректный ответ проверки.");
    }
  };
  return (
    <View style={{ flex: 1 }}>
      <Page title="Проверка безопасности">
        <Label>Выполните проверку для входа.</Label>
        <ErrorNotice message={error} />
        <Button label="Отмена" onPress={onCancel} secondary />
      </Page>
      {requestId ? (
        <WebView
          source={{ uri: url }}
          originWhitelist={[origin]}
          onShouldStartLoadWithRequest={allow}
          onMessage={receive}
          javaScriptEnabled
          domStorageEnabled={false}
          thirdPartyCookiesEnabled
          sharedCookiesEnabled={false}
          incognito
          setSupportMultipleWindows={false}
          onError={() => setError("Не удалось открыть проверку.")}
        />
      ) : null}
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

  if (captcha)
    return (
      <Captcha
        action={captcha}
        onToken={captchaResult}
        onCancel={() => setCaptcha(null)}
      />
    );
  return (
    <Page title="Heartbeat Checker">
      <Card>
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
              setMode("login");
              setError("");
              if (mode === "confirm") void native.clearRegistration();
            }}
            secondary
          />
        ) : null}
      </Card>
      {mode === "login" ? (
        <Card>
          <Button
            label="Регистрация"
            onPress={() => setMode("register")}
            secondary
          />
          <Button
            label="Забыли пароль?"
            onPress={() => setMode("reset")}
            secondary
          />
          <Button
            label="Войти через Яндекс"
            onPress={() => oauth("yandex")}
            disabled={busy}
            secondary
          />
          <Button
            label="Войти через VK"
            onPress={() => oauth("vk")}
            disabled={busy}
            secondary
          />
        </Card>
      ) : null}
    </Page>
  );
}
