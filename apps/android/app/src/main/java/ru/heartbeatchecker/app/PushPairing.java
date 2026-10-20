package ru.heartbeatchecker.app;

import android.content.Context;
import android.os.Build;

import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Base64;

/** Native-only PKCE state. Browser cookies and session credentials never enter this store. */
final class PushPairing {
    interface Transport {
        JSONObject request(String method, String path, String credential, JSONObject body) throws java.io.IOException, org.json.JSONException;
    }
    private PushPairing() {}

    static String bootstrap(Context context, String returnUrl) {
        return bootstrap(context, returnUrl, new PushApi()::request);
    }

    static String bootstrap(Context context, String returnUrl, Transport api) {
        return bootstrap(context, returnUrl, api, false);
    }

    static String enable(Context context, String returnUrl) {
        return enable(context, returnUrl, new PushApi()::request);
    }

    static String enable(Context context, String returnUrl, Transport api) {
        return bootstrap(context, returnUrl, api, true);
    }

    private static String bootstrap(Context context, String returnUrl, Transport api, boolean explicitEnable) {
        if (LinkPolicy.appLink(returnUrl) == null) return LinkPolicy.HOME;
        PushStore store = new PushStore(context);
        long revocation = PushWork.localRevocation;
        try {
            if (explicitEnable) {
                if (!PushWork.revokePending(context, true)) return returnUrl;
                synchronized (PushStore.LOCK) {
                    if (revocation != PushWork.localRevocation) return returnUrl;
                    store.update(value -> { value.remove("optedOut"); value.remove("permissionPrompted"); });
                }
            }
            JSONObject state = store.read();
            if (state.optBoolean("optedOut") || (state.optBoolean("permissionPrompted") && !PushNotifications.permitted(context))) return returnUrl;
            if (!PushWork.revokePending(context, false)) return returnUrl;
            String credential = state.optString("credential");
            if (!credential.isEmpty()) {
                try {
                    JSONObject installation = api.request("GET", "/api/v1/push/installation", credential, null);
                    PushWork.accept(context, installation, credential);
                    JSONObject pending = state.optJSONObject("pair");
                    if (pending != null && pending.optBoolean("exchangeAttempted")) {
                        if ("ready".equals(installation.optString("status")) && pending.optString("id").equals(installation.optString("lastPairingId")))
                            accept(context, pending.getString("id"), credential, installation, revocation);
                        else if (PushContract.instant(pending.getString("expiresAt")) > System.currentTimeMillis()) return returnUrl;
                        // After expiry and an authoritative GET, a fresh pairing supersedes
                        // the old proof; the old exchange itself is never posted again.
                    }
                } catch (PushApi.Failure failure) {
                    if (failure.status != 401 && failure.status != 404) return returnUrl;
                    synchronized (PushStore.LOCK) {
                        JSONObject current = store.read();
                        if (revocation != PushWork.localRevocation || current.optBoolean("optedOut")
                                || !credential.equals(current.optString("credential"))) return returnUrl;
                        if (!explicitEnable && !expiredAnonymous(current, System.currentTimeMillis())) {
                            PushWork.disable(context);
                            return returnUrl;
                        }
                        // The server has definitively retired this credential. A deliberate
                        // enable, or an expired identity that never attempted binding, may
                        // start a new anonymous identity without repeating an old exchange.
                        store.update(value -> {
                            value.put("enabled", false);
                            for (String key : new String[]{"credential", "installation", "pair", "pending", "seen", "sequences",
                                    "registeredToken", "registeredPermission", "everExchangeAttempted"}) value.remove(key);
                        });
                        PushWork.blockLocally(context);
                        revocation = PushWork.localRevocation;
                        credential = "";
                    }
                }
            }
            state = store.read();
            if (state.optBoolean("optedOut") || !credential.equals(state.optString("credential"))) return returnUrl;
            // A browser confirmation response may have been lost. A fresh request avoids
            // attempting to recover or reuse its single-issued completion code.
            String verifier = random(); String clientState = random();
            String challenge = Base64.getUrlEncoder().withoutPadding().encodeToString(
                    MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(StandardCharsets.US_ASCII)));
            String label = Build.MANUFACTURER + " " + Build.MODEL;
            if (!PushContract.bounded(label, 256) || label.codePointCount(0, label.length()) > 64) label = "Android";
            JSONObject request = new JSONObject().put("codeChallenge", challenge).put("clientState", clientState)
                    .put("deviceLabel", label).put("appVersion", BuildConfig.VERSION_NAME);
            JSONObject result = api.request("POST", "/api/v1/push/pairings", credential, request);
            JSONObject pairing = result.getJSONObject("pairing"); JSONObject installation = result.getJSONObject("installation");
            String id = pairing.getString("id"); String installationId = installation.getString("id");
            String nextCredential = credential.isEmpty() ? installation.getString("credential") : credential;
            if (!PushContract.uuid(id) || !PushContract.credential(nextCredential, installationId)
                    || (!credential.isEmpty() && installation.has("credential"))
                    || !PushContract.confirmation(pairing.getString("confirmationUrl"), id)) return returnUrl;
            long expires = PushContract.instant(pairing.getString("expiresAt"));
            if (expires <= System.currentTimeMillis() || expires > System.currentTimeMillis() + 330000) return returnUrl;
            JSONObject saved = new JSONObject().put("id", id).put("verifier", verifier).put("clientState", clientState)
                    .put("expiresAt", pairing.getString("expiresAt")).put("confirmationUrl", pairing.getString("confirmationUrl"))
                    .put("returnUrl", returnUrl);
            synchronized (PushStore.LOCK) {
                if (revocation != PushWork.localRevocation) return returnUrl;
                String expectedCredential = credential;
                store.update(value -> {
                    if (value.optBoolean("optedOut") || !expectedCredential.equals(value.optString("credential")))
                        throw new org.json.JSONException("Pairing superseded");
                    value.put("credential", nextCredential).put("pair", saved);
                });
            }
            return pairing.getString("confirmationUrl");
        } catch (Exception ignored) { return returnUrl; }
    }

    private static boolean expiredAnonymous(JSONObject state, long now) throws Exception {
        JSONObject pair = state.optJSONObject("pair"); JSONObject installation = state.optJSONObject("installation");
        return !state.optBoolean("enabled") && !state.optBoolean("everExchangeAttempted") && pair != null
                && !pair.optBoolean("exchangeAttempted") && PushContract.instant(pair.getString("expiresAt")) <= now
                && (installation == null || (installation.has("bindingGeneration") && installation.has("lastPairingId")
                && installation.optLong("bindingGeneration") == 0 && installation.isNull("lastPairingId")));
    }

    static String receive(Context context, String id, String code, String clientState) throws Exception {
        PushStore store = new PushStore(context);
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read(); JSONObject pending = state.optJSONObject("pair");
            if (state.optBoolean("optedOut") || pending == null || !id.equals(pending.optString("id"))
                    || !clientState.equals(pending.optString("clientState")) || !PushContract.secret(code)
                    || (pending.has("completionCode") && !code.equals(pending.optString("completionCode")))
                    || (PushContract.instant(pending.getString("expiresAt")) <= System.currentTimeMillis()
                    && !pending.optBoolean("exchangeAttempted")))
                throw new IllegalArgumentException();
            String returnUrl = pending.optString("returnUrl", LinkPolicy.HOME);
            if (LinkPolicy.appLink(returnUrl) == null) throw new IllegalArgumentException();
            store.update(value -> value.getJSONObject("pair").put("completionCode", code));
            return returnUrl;
        }
    }

    static void exchange(Context context, String id) throws Exception {
        exchange(context, id, new PushApi()::request);
    }

    static void exchange(Context context, String id, Transport api) throws Exception {
        PushStore store = new PushStore(context); long revocation = PushWork.localRevocation;
        JSONObject state = store.read(); JSONObject pending = state.getJSONObject("pair");
        boolean expired = PushContract.instant(pending.getString("expiresAt")) <= System.currentTimeMillis();
        if (state.optBoolean("optedOut") || !id.equals(pending.getString("id")) || !PushNotifications.permitted(context)) return;
        String credential = state.getString("credential"); String token = state.getString("token");
        if (!PushContract.token(token) || !PushContract.secret(pending.optString("completionCode"))) return;
        JSONObject result;
        try {
            result = pending.optBoolean("exchangeAttempted")
                    ? api.request("GET", "/api/v1/push/installation", credential, null) : null;
            boolean reconciled = result != null && "ready".equals(result.optString("status")) && id.equals(result.optString("lastPairingId"));
            if (!reconciled) {
                if (expired || pending.optBoolean("exchangeAttempted")) return;
                synchronized (PushStore.LOCK) {
                    if (revocation != PushWork.localRevocation) return;
                    store.update(value -> {
                        JSONObject current = value.getJSONObject("pair");
                        if (!id.equals(current.getString("id")) || !credential.equals(value.optString("credential")) || value.optBoolean("optedOut"))
                            throw new org.json.JSONException("Pairing superseded");
                        current.put("exchangeAttempted", true);
                        value.put("everExchangeAttempted", true);
                    });
                }
                JSONObject body = new JSONObject().put("codeVerifier", pending.getString("verifier"))
                        .put("completionCode", pending.getString("completionCode")).put("provider", "rustore")
                        .put("token", token).put("permission", "granted").put("appVersion", BuildConfig.VERSION_NAME);
                result = api.request("POST", "/api/v1/push/pairings/" + id + "/exchange", credential, body);
            }
        } catch (java.io.IOException unknown) {
            // A response lost after consumption must be reconciled, never posted again.
            result = api.request("GET", "/api/v1/push/installation", credential, null);
        }
        accept(context, id, credential, result, revocation);
    }

    private static void accept(Context context, String id, String credential, JSONObject result, long revocation) throws Exception {
        JSONObject installation = PushContract.installation(result);
        if (!"ready".equals(installation.getString("status")) || !id.equals(installation.optString("lastPairingId"))) return;
        PushStore store = new PushStore(context);
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read(); JSONObject current = state.optJSONObject("pair");
            if (revocation != PushWork.localRevocation || state.optBoolean("optedOut") || !credential.equals(state.optString("credential"))
                    || current == null || !id.equals(current.optString("id"))) return;
            PushWork.accept(context, installation, credential);
            store.update(value -> {
                value.put("enabled", true); value.remove("pair");
                // A reconciled response cannot prove which token reached the server.
                value.remove("registeredToken"); value.remove("registeredPermission");
            });
            PushWork.locallyBlocked = false;
        }
        PushWork.registration(context);
    }

    private static String random() {
        byte[] value = new byte[32]; new SecureRandom().nextBytes(value);
        return Base64.getUrlEncoder().withoutPadding().encodeToString(value);
    }
}
