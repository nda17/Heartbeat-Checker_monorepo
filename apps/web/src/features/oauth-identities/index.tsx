"use client";
import { notify } from "@/shared/notifications";

import Link from "next/link";
import { FormEvent, useCallback, useEffect, useState } from "react";
import { useSession } from "@/entities/session";
import {
  api,
  errorMessage,
  oauthAuthorizationUrl,
  oauthCallbackMessage,
} from "@/shared/api";
import {
  dateTime,
  Loading,
  Notice,
  PasswordInput,
  OAuthIcon,
} from "@/shared/ui";

type ProviderId = "yandex" | "vk";
type Provider = { id: ProviderId; name: string; enabled: boolean };
type Identity = { id: string; provider: ProviderId; connectedAt: string };
const names: Record<ProviderId, string> = { yandex: "Яндекс", vk: "VK" };

export function OAuthIdentities({
  hasPassword,
  isAdmin,
  onChange,
}: {
  hasPassword: boolean;
  isAdmin: boolean;
  onChange: () => Promise<void>;
}) {
  const { beginSessionTransition, endSessionTransition } = useSession();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [identities, setIdentities] = useState<Identity[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async (signal?: AbortSignal) => {
    const [available, linked] = await Promise.all([
      api<{ providers: Provider[] }>("/auth/oauth/providers", { signal }),
      api<{ identities: Identity[] }>("/me/identities", { signal }),
    ]);
    if (signal?.aborted) return;
    setProviders(available.providers);
    setIdentities(linked.identities);
  }, []);
  useEffect(() => {
    const url = new URL(window.location.href);
    const linked = url.searchParams.get("oauth") === "linked";
    const failure = url.searchParams.get("oauthError");
    if (linked || failure) {
      // Capture the one-time callback result before removing it from the browser URL.
      if (linked) notify.success("Способ входа подключён.");
      if (failure) notify.error(oauthCallbackMessage(failure));
      window.history.replaceState(null, "", "/app/settings");
      if (linked)
        void onChange().catch((cause) =>
          setError(
            `Способ входа подключён, но профиль не обновился: ${errorMessage(cause)}`,
          ),
        );
    }
  }, [onChange]);
  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoadError("");
    const timer = window.setTimeout(() => {
      controller.abort();
      setLoadError(
        "Загрузка способов входа заняла слишком много времени. Повторите попытку.",
      );
    }, 10_000);
    void load(controller.signal)
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setLoadError(errorMessage(cause));
          controller.abort();
        }
      })
      .finally(() => window.clearTimeout(timer));
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [load, loadAttempt]);

  async function connect(
    event: FormEvent<HTMLFormElement>,
    provider: ProviderId,
  ) {
    event.preventDefault();
    if (busy || !hasPassword) return;
    const password = new FormData(event.currentTarget).get("password");
    setBusy(provider);
    setError("");

    try {
      const result = await api<{ authorizationUrl: string }>(
        `/auth/oauth/${provider}/start`,
        {
          method: "POST",
          authenticated: true,
          body: { mode: "link", password },
        },
      );
      window.location.assign(
        oauthAuthorizationUrl(provider, result.authorizationUrl),
      );
    } catch (cause) {
      notify.error(errorMessage(cause));
      setBusy("");
    }
  }

  async function disconnect(
    event: FormEvent<HTMLFormElement>,
    identity: Identity,
  ) {
    event.preventDefault();
    if (busy || !hasPassword) return;
    const form = event.currentTarget;
    const password = new FormData(form).get("password");
    setBusy(identity.id);
    setError("");

    try {
      beginSessionTransition();
      try {
        await api(`/me/identities/${identity.provider}`, {
          method: "DELETE",
          authenticated: true,
          body: { identityId: identity.id, password },
        });
      } finally {
        endSessionTransition();
      }
      form.reset();
      setIdentities(
        (current) => current?.filter((item) => item.id !== identity.id) ?? null,
      );
      notify.success(`${names[identity.provider]} отключён от аккаунта.`);
      try {
        await Promise.all([load(), onChange()]);
      } catch (cause) {
        setError(
          `${names[identity.provider]} отключён, но данные не обновились: ${errorMessage(cause)}`,
        );
      }
    } catch (cause) {
      notify.error(errorMessage(cause));
    } finally {
      setBusy("");
    }
  }

  return (
    <section
      className="panel oauth-identities-panel"
      aria-labelledby="oauth-identities-title"
    >
      <div className="panel-heading">
        <span className="panel-index">03</span>
        <div>
          <h2 id="oauth-identities-title">Способы входа</h2>
          <p>Подключите Яндекс или VK к этому аккаунту без создания дубля.</p>
        </div>
      </div>
      {error && <Notice>{error}</Notice>}

      {!hasPassword && (
        <p className="small">
          Чтобы подключать и отключать способы входа, сначала{" "}
          <Link href="/forgot-password">установите пароль через email</Link>.
        </p>
      )}
      {isAdmin && hasPassword && (
        <p className="small muted">
          Для изменения способов входа администратора сначала подтвердите MFA в{" "}
          <Link href="/app/admin">разделе администрирования</Link>.
        </p>
      )}
      {loadError ? (
        <div className="form-stack">
          <Notice>{loadError}</Notice>
          <button
            type="button"
            className="secondary"
            onClick={() => setLoadAttempt((value) => value + 1)}
          >
            Повторить загрузку способов входа
          </button>
        </div>
      ) : !identities ? (
        <Loading />
      ) : (
        <div className="oauth-identity-list">
          {providers
            .filter((provider) => provider.enabled)
            .map((provider) => {
              const identity = identities.find(
                (item) => item.provider === provider.id,
              );
              return (
                <div className="oauth-identity" key={provider.id}>
                  <div>
                    <strong className="oauth-provider-name">
                      <OAuthIcon provider={provider.id} />
                      {provider.name}
                    </strong>
                    <p className="small muted">
                      {identity
                        ? `Подключён ${dateTime(identity.connectedAt)}`
                        : "Не подключён"}
                    </p>
                  </div>
                  {hasPassword && (
                    <form
                      className="oauth-identity-form"
                      onSubmit={(event) =>
                        identity
                          ? void disconnect(event, identity)
                          : void connect(event, provider.id)
                      }
                    >
                      <label>
                        Текущий пароль
                        <PasswordInput
                          name="password"
                          autoComplete="current-password"
                          required
                          maxLength={1024}
                        />
                      </label>
                      <button
                        className={identity ? "quiet danger-text" : "secondary"}
                        disabled={!!busy}
                      >
                        <OAuthIcon provider={provider.id} />
                        {busy === (identity?.id ?? provider.id)
                          ? "Подождите…"
                          : identity
                            ? "Отключить"
                            : "Подключить"}
                      </button>
                    </form>
                  )}
                </div>
              );
            })}
          {identities
            .filter(
              (identity) =>
                !providers.some(
                  (provider) =>
                    provider.id === identity.provider && provider.enabled,
                ),
            )
            .map((identity) => (
              <div className="oauth-identity" key={identity.id}>
                <div>
                  <strong className="oauth-provider-name">
                    <OAuthIcon provider={identity.provider} />
                    {names[identity.provider]}
                  </strong>
                  <p className="small muted">
                    Подключён {dateTime(identity.connectedAt)} · Вход временно
                    недоступен
                  </p>
                </div>
                {hasPassword && (
                  <form
                    className="oauth-identity-form"
                    onSubmit={(event) => void disconnect(event, identity)}
                  >
                    <label>
                      Текущий пароль
                      <PasswordInput
                        name="password"
                        autoComplete="current-password"
                        required
                        maxLength={1024}
                      />
                    </label>
                    <button className="quiet danger-text" disabled={!!busy}>
                      <OAuthIcon provider={identity.provider} />
                      {busy === identity.id ? "Подождите…" : "Отключить"}
                    </button>
                  </form>
                )}
              </div>
            ))}
          {providers.length === 0 && identities.length === 0 && (
            <p className="small muted">
              Вход через провайдеров пока недоступен.
            </p>
          )}
        </div>
      )}
    </section>
  );
}
