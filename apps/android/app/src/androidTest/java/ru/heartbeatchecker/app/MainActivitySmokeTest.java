package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.Intent;
import android.content.res.AssetManager;
import android.net.Uri;
import android.view.View;
import android.view.ViewGroup;
import android.widget.TextView;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import org.junit.Test;
import org.junit.runner.RunWith;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.time.Instant;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class MainActivitySmokeTest {
    private static final String MONITOR = "123e4567-e89b-12d3-a456-426614174000";
    private static final String INCIDENT = "987e6543-e21b-12d3-a456-426614174000";
    private static final String ORDER = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private static final String STATE = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFE";
    private static final String CODE = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private static final String ACCOUNT = "11111111-1111-4111-8111-111111111111";
    private static final String SESSION = "22222222-2222-4222-8222-222222222222";

    @Test
    public void bundledOfflineJavascriptRegistersAndRendersNativeAuthScreen() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        AssetManager assets = context.getAssets();
        byte[] bundle;
        try (InputStream input = assets.open("index.android.bundle"); ByteArrayOutputStream output = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[8192];
            int count;
            while ((count = input.read(buffer)) != -1) output.write(buffer, 0, count);
            bundle = output.toByteArray();
        }
        assertTrue("CI APK must contain a substantial offline React Native bundle", bundle.length > 64 * 1024);

        try (ActivityScenario<MainActivity> scenario = ActivityScenario.launch(MainActivity.class)) {
            assertTrue("a rendered React Native screen is required; Activity launch alone is insufficient",
                    awaitVisibleText(scenario, "Войти через Яндекс"));
        }
    }

    @Test
    public void nativeLinksAcceptOnlyExpectedExactHttpsRoutes() {
        assertEquals("https://heartbeat-checker.ru/app?monitor=" + MONITOR + "&incident=" + INCIDENT,
                normalize("https://heartbeat-checker.ru/app?incident=" + INCIDENT + "&monitor=" + MONITOR));
        assertEquals("https://heartbeat-checker.ru/app/settings",
                normalize("https://heartbeat-checker.ru/app/settings"));
        assertEquals("https://heartbeat-checker.ru/app/billing",
                normalize("https://heartbeat-checker.ru/app/billing"));
        assertEquals("https://heartbeat-checker.ru/android/payments/return?order=" + ORDER,
                normalize("https://heartbeat-checker.ru/android/payments/return?order=" + ORDER));
        assertEquals("https://heartbeat-checker.ru/android/auth/complete?flowId=" + ORDER + "&state=" + STATE + "&code=" + CODE,
                normalize("https://heartbeat-checker.ru/android/auth/complete?flowId=" + ORDER + "&state=" + STATE + "&code=" + CODE));
        assertEquals("https://heartbeat-checker.ru/android/auth/complete?flowId=" + ORDER + "&state=" + STATE + "&code=1234567",
                normalize("https://heartbeat-checker.ru/android/auth/complete?flowId=" + ORDER + "&state=" + STATE + "&code=1234567"));
        assertEquals("https://heartbeat-checker.ru/android/auth/verify?oauth=1#token=" + STATE,
                normalize("https://heartbeat-checker.ru/android/auth/verify?oauth=1#token=" + STATE));
        assertEquals("https://heartbeat-checker.ru/android/auth/reset#token=" + STATE,
                normalize("https://heartbeat-checker.ru/android/auth/reset#token=" + STATE));

        for (String rejected : new String[] {
                "https://heartbeat-checker.ru.evil.test/app",
                "https://heartbeat-checker.ru@evil.test/app",
                "https://heartbeat-checker.ru:443/app",
                "http://heartbeat-checker.ru/app",
                "https://heartbeat-checker.ru/app?monitor=" + MONITOR,
                "https://heartbeat-checker.ru/android/payments/return?order=" + ORDER + "&return=https://evil.test",
                "https://heartbeat-checker.ru/android/auth/complete?flowId=" + ORDER + "&state=" + STATE + "&code=" + CODE + "&state=" + STATE,
                "https://heartbeat-checker.ru/android/auth/reset?token=" + STATE,
                "https://heartbeat-checker.ru/android/auth/verify?oauth=1#token=" + STATE + "&extra=x",
                "https://heartbeat-checker.ru/android/push/complete?pairingId=" + ORDER,
                "https://heartbeat-checker.ru/oauth/callback?code=secret&state=state",
        }) assertNull("unexpected/deprecated link accepted: " + rejected, normalize(rejected));
    }

    @Test
    public void resetAppLinkColdStartRendersFormOnlyAfterDurableLogout() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        AuthStore auth = new AuthStore(context);
        clearAuthStore(auth);
        auth.saveSession(new JSONObject().put("account", new JSONObject().put("id", ACCOUNT))
                .put("session", new JSONObject().put("id", SESSION).put("token", CODE).put("revokeToken", STATE)
                        .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 3_600_000).toString())), auth.epoch());
        long revocationBefore = PushWork.localRevocation;
        Intent reset = new Intent(context, MainActivity.class).setAction(Intent.ACTION_VIEW)
                .setData(Uri.parse("https://heartbeat-checker.ru/android/auth/reset#token=" + STATE));

        try (ActivityScenario<MainActivity> scenario = ActivityScenario.launch(reset)) {
            try {
                assertTrue("cold App Link must be consumed by the rendered React Native reset screen",
                        awaitVisibleText(scenario, "Новый пароль"));
                assertNull("native reset flow durably signs out before rendering the reset form", auth.active());
                assertFalse(auth.read().optBoolean("gate"));
                assertEquals(SESSION, auth.pendingRevokes().getJSONObject(0).getString("sessionId"));
                assertTrue("logout advances the local push fence immediately", PushWork.localRevocation > revocationBefore);
                assertTrue("logout keeps background delivery blocked", PushWork.locallyBlocked);
                scenario.onActivity(activity -> assertNull(
                        "the consumed reset link must be removed from the Activity intent",
                        activity.getIntent().getData()));
            } finally {
                // ActivityScenario 1.7 keeps this same launch Intent and matches it to lifecycle callbacks.
                // The app intentionally clears Activity.getIntent().data after consuming the sensitive link.
                reset.setData(null);
            }
        } finally {
            clearAuthStore(auth);
        }
    }

    @Test
    public void packageKeepsStableUpgradeIdentityAndDoesNotExposeShareTarget() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        assertEquals("ru.heartbeatchecker.app", context.getPackageName());
        int version = context.getPackageManager().getPackageInfo(context.getPackageName(), 0).getLongVersionCode() > Integer.MAX_VALUE
                ? Integer.MAX_VALUE : (int) context.getPackageManager().getPackageInfo(context.getPackageName(), 0).getLongVersionCode();
        assertTrue("update must advance beyond published APK 1.2.0/code 6", version > 6);
        Intent send = new Intent(Intent.ACTION_SEND).setType("text/plain").setPackage(context.getPackageName());
        assertTrue("the app must not register as a share target",
                context.getPackageManager().queryIntentActivities(send, 0).isEmpty());
    }

    private static String normalize(String value) {
        return MainActivity.normalize(new Intent(Intent.ACTION_VIEW, Uri.parse(value)));
    }

    private static void clearAuthStore(AuthStore store) throws Exception {
        java.util.Iterator<String> keys = store.read().keys();
        java.util.List<String> remove = new java.util.ArrayList<>();
        while (keys.hasNext()) remove.add(keys.next());
        for (String key : remove) store.remove(key);
    }

    private static boolean awaitVisibleText(ActivityScenario<MainActivity> scenario, String expected) throws InterruptedException {
        for (int attempt = 0; attempt < 100; attempt++) {
            AtomicBoolean found = new AtomicBoolean();
            scenario.onActivity(activity -> found.set(containsText(activity.getWindow().getDecorView(), expected)));
            if (found.get()) return true;
            Thread.sleep(100);
        }
        return false;
    }

    private static boolean containsText(View view, String expected) {
        if (view instanceof TextView && expected.contentEquals(((TextView) view).getText())) return true;
        if (!(view instanceof ViewGroup)) return false;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            if (containsText(group.getChildAt(index), expected)) return true;
        }
        return false;
    }
}
