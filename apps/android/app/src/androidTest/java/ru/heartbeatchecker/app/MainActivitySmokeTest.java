package ru.heartbeatchecker.app;

import android.app.Instrumentation;
import android.content.Context;
import android.content.Intent;
import android.content.res.AssetManager;
import android.graphics.Bitmap;
import android.graphics.Rect;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.accessibility.AccessibilityNodeInfo;
import android.widget.TextView;
import android.widget.EditText;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.runner.lifecycle.ActivityLifecycleCallback;
import androidx.test.runner.lifecycle.ActivityLifecycleMonitor;
import androidx.test.runner.lifecycle.ActivityLifecycleMonitorRegistry;
import androidx.test.runner.lifecycle.Stage;

import org.junit.Test;
import org.junit.runner.RunWith;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;

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
    private static final String[] ANIMATION_SCALE_SETTINGS = {
            "window_animation_scale", "transition_animation_scale", "animator_duration_scale"
    };

    @Test
    public void bundledOfflineJavascriptRendersNativePublicLandingBeforeLogin() throws Exception {
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
                    awaitVisibleText(scenario, "Круглосуточный мониторинг ваших сайтов и сервисов."));
            assertTrue("the full public landing must offer registration", awaitVisibleText(scenario, "Создать аккаунт"));
            waitForBundledLandingImages();
            saveScreenshot(scenario, "Круглосуточный мониторинг ваших сайтов и сервисов.", "Войти через Яндекс", "public-landing.png");

            scrollToText(scenario, "API магазина");
            tapText(scenario, "API магазина");
            String failureLegend = "Сбои: Ср — 6, Вс — 2. Число неудачных проверок за день.";
            scrollToText(scenario, failureLegend);
            assertTrue("selected API demo must show the day-level failure counts", awaitVisibleText(scenario, failureLegend));
            saveScreenshot(scenario, failureLegend, "Email", "public-api-failed-days.png");

            scrollToTop(scenario);
            tapText(scenario, "Возможности");
            assertTrue("the landing navigation must reach the native features section", awaitVisibleText(scenario, "Три типа проверки"));
            saveScreenshot(scenario, "Три типа проверки", "Войти через Яндекс", "public-features.png");

            scrollToTop(scenario);
            tapText(scenario, "Войти");
            assertTrue("public login action must open the native authentication screen", awaitVisibleText(scenario, "Войти через Яндекс"));
            saveScreenshot(scenario, "Войти", "Мониторы", "login.png");
        }
    }

    @Test
    public void bundledSignalMotionChangesNativeFramesAndHonorsReducedMotion() throws Exception {
        Map<String, String> originalScales = readAnimationScales();
        ActivityScenario<MainActivity> scenario = null;
        try {
            setAnimationScales("1");
            assertEquals("1", readAnimationScale("transition_animation_scale"));
            scenario = ActivityScenario.launch(MainActivity.class);
            assertTrue(awaitVisibleText(scenario, "Круглосуточный мониторинг ваших сайтов и сервисов."));
            waitForBundledLandingImages();
            assertEquals("1", readAnimationScale("transition_animation_scale"));

            List<Bitmap> activeFrames = new ArrayList<>();
            List<Rect> activeBounds = new ArrayList<>();
            long captureStart = android.os.SystemClock.elapsedRealtime();
            long[] offsetsMs = {0, 211, 518, 937, 1460, 2067, 2806, 3693, 4800};
            for (long offset : offsetsMs) {
                sleepUntil(captureStart + offset);
                activeFrames.add(captureSignalRegion(scenario, "capture active ECG frame", activeBounds));
            }
            assertStableCropBounds(activeBounds);
            int visiblyDistinctFrames = countVisiblyDistinctFrames(activeFrames);
            int changedPixels = maxChangedPixelsFromFirst(activeFrames);
            saveMotionFrames(activeFrames, activeBounds, "signal-motion-active", offsetsMs,
                    "transition_animation_scale=1; native rendered ECG sweep");
            assertTrue("native ECG sweep must commit visibly different frames; visibly distinct=" + visiblyDistinctFrames
                            + ", max changed pixels=" + changedPixels,
                    visiblyDistinctFrames >= 5 && changedPixels >= 24);
            recycle(activeFrames);
            activeFrames.clear();

            scenario.close();
            scenario = null;
            setAnimationScale("transition_animation_scale", "0");
            assertEquals("0", readAnimationScale("transition_animation_scale"));
            scenario = ActivityScenario.launch(MainActivity.class);
            assertTrue(awaitVisibleText(scenario, "Круглосуточный мониторинг ваших сайтов и сервисов."));
            waitForBundledLandingImages();
            Thread.sleep(800); // Allow MotionPreferences to consume Android's reduced-motion setting.

            List<Bitmap> reducedFrames = new ArrayList<>();
            List<Rect> reducedBounds = new ArrayList<>();
            long reducedStart = android.os.SystemClock.elapsedRealtime();
            long[] reducedOffsetsMs = {0, 503, 1237};
            for (long offset : reducedOffsetsMs) {
                sleepUntil(reducedStart + offset);
                reducedFrames.add(captureSignalRegion(scenario, "capture reduced-motion ECG frame", reducedBounds));
            }
            assertStableCropBounds(reducedBounds);
            int reducedChangedPixels = maxChangedPixelsFromFirst(reducedFrames);
            saveMotionFrames(reducedFrames, reducedBounds, "signal-motion-reduced", reducedOffsetsMs,
                    "transition_animation_scale=0; stable reduced-motion ECG trace");
            assertTrue("reduced motion must leave the opaque ECG crop stable; changed pixels="
                            + reducedChangedPixels,
                    reducedChangedPixels <= 4);
            recycle(reducedFrames);
        } finally {
            try {
                if (scenario != null) scenario.close();
            } finally {
                restoreAnimationScales(originalScales);
            }
        }
    }

    @Test
    public void authModesAndPasswordVisibilityRenderInTheBundledReactNativeScreen() throws Exception {
        try (ActivityScenario<MainActivity> scenario = ActivityScenario.launch(MainActivity.class)) {
            assertTrue(awaitVisibleText(scenario, "Круглосуточный мониторинг ваших сайтов и сервисов."));
            tapText(scenario, "Войти");
            assertTrue(awaitVisibleText(scenario, "Войти через Яндекс"));

            tapDescription(scenario, "Показать пароль");
            assertTrue("password control must expose its visible state accessibly",
                    awaitVisibleDescription(scenario, "Скрыть пароль"));
            saveScreenshot(scenario, "Войти", "Мониторы", "password-visible.png");

            tapText(scenario, "Регистрация");
            assertTrue("registration screen must render its primary action", awaitVisibleText(scenario, "Создать аккаунт"));
            saveScreenshot(scenario, "Создать аккаунт", "Мониторы", "register.png");

            tapText(scenario, "Ко входу");
            assertTrue(awaitVisibleText(scenario, "Войти через Яндекс"));
            tapText(scenario, "Забыли пароль?");
            assertTrue("reset request mode must render its primary action", awaitVisibleText(scenario, "Отправить ссылку"));
            saveScreenshot(scenario, "Отправить ссылку", "Мониторы", "reset-request.png");

            tapText(scenario, "Ко входу");
            assertTrue(awaitVisibleText(scenario, "Войти через Яндекс"));
            setInputText(scenario, "Email", "person@example.com");
            setInputText(scenario, "Пароль", "qa-password-for-screen-only");
            assertTrue("login action must become enabled after React Native accepts both fields",
                    awaitEnabledControl(scenario, "Войти"));
            tapText(scenario, "Войти");
            assertTrue(awaitVisibleText(scenario, "Проверка безопасности"));
            saveScreenshot(scenario, "Проверка безопасности", "Мониторы", "captcha-login.png");
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
                        awaitVisibleText(scenario, "Сохранить пароль"));
                assertNull("native reset flow durably signs out before rendering the reset form", auth.active());
                assertFalse(auth.read().optBoolean("gate"));
                assertEquals(SESSION, auth.pendingRevokes().getJSONObject(0).getString("sessionId"));
                assertTrue("logout advances the local push fence immediately", PushWork.localRevocation > revocationBefore);
                assertTrue("logout keeps background delivery blocked", PushWork.locallyBlocked);
                saveScreenshot(scenario, "Сохранить пароль", "Мониторы", "reset-confirm.png");
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
    public void emailVerificationColdStartStoresPendingLinkAndScrubsActivityIntent() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        AuthStore auth = new AuthStore(context);
        clearAuthStore(auth);
        String token = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
        Intent verification = new Intent(Intent.ACTION_VIEW).setClass(context, MainActivity.class)
                .setData(Uri.parse("https://heartbeat-checker.ru/app/settings#verify-email=" + token));
        AtomicBoolean created = new AtomicBoolean();
        AtomicReference<MainActivity> activityAtCreate = new AtomicReference<>();
        AtomicReference<String> intentDataAtCreate = new AtomicReference<>();
        ActivityLifecycleMonitor lifecycleMonitor = ActivityLifecycleMonitorRegistry.getInstance();
        ActivityLifecycleCallback captureSafeIntent = (candidate, stage) -> {
            if (candidate instanceof MainActivity && stage == Stage.CREATED) {
                activityAtCreate.set((MainActivity) candidate);
                intentDataAtCreate.set(candidate.getIntent().getDataString());
                created.set(true);
            }
        };
        MainActivity activity = null;
        boolean callbackRegistered = false;
        try {
            lifecycleMonitor.addLifecycleCallback(captureSafeIntent);
            callbackRegistered = true;
            activity = launchMainActivity(verification);
            MainActivity launchedActivity = activity;
            assertTrue("cold-start intent snapshot must be captured after onCreate", created.get());
            assertSame("snapshot must belong to the launched MainActivity", launchedActivity, activityAtCreate.get());
            assertEquals("onCreate must replace the secret link with the reviewed settings route before RN boot",
                    "https://heartbeat-checker.ru/app/settings", intentDataAtCreate.get());
            JSONObject pending = auth.emailVerification(null, false);
            assertNotNull("cold email link is durably retained before login", pending);
            assertEquals(token, pending.getString("token"));
            assertNull("verification link never creates an authenticated session", auth.active());
            assertTrue("initially unauthenticated token remains unbound", pending.isNull("accountId") && pending.isNull("sessionId"));
            AtomicReference<String> activityIntentData = new AtomicReference<>();
            InstrumentationRegistry.getInstrumentation().runOnMainSync(() ->
                    activityIntentData.set(launchedActivity.getIntent().getDataString()));
            assertTrue("after RN consumes the initial handoff, Activity keeps no URI or only the safe route",
                    activityIntentData.get() == null
                            || "https://heartbeat-checker.ru/app/settings".equals(activityIntentData.get()));
            assertFalse("consumed email token is scrubbed from Activity intent",
                    activityIntentData.get() != null && activityIntentData.get().contains(token));
        } finally {
            try {
                try {
                    finishMainActivity(activity);
                } finally {
                    clearAuthStore(auth);
                }
            } finally {
                if (callbackRegistered) lifecycleMonitor.removeLifecycleCallback(captureSafeIntent);
            }
        }
    }

    @Test
    public void warmEmailLinkReplacesUnconsumedColdHandoffForTheNewToken() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        AuthStore auth = new AuthStore(context);
        clearAuthStore(auth);
        String coldToken = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
        String warmToken = "BCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefgh";
        Intent cold = new Intent(Intent.ACTION_VIEW).setClass(context, MainActivity.class)
                .setData(Uri.parse("https://heartbeat-checker.ru/app/settings#verify-email=" + coldToken));
        MainActivity activity = null;
        try {
            activity = launchMainActivity(cold);
            MainActivity launchedActivity = activity;
            Intent warm = new Intent(Intent.ACTION_VIEW, Uri.parse(
                    "https://heartbeat-checker.ru/app/settings#verify-email=" + warmToken));
            AtomicReference<String> staleHandoff = new AtomicReference<>();
            AtomicReference<String> activityIntentData = new AtomicReference<>();
            InstrumentationRegistry.getInstrumentation().runOnMainSync(() -> {
                launchedActivity.onNewIntent(warm);
                staleHandoff.set(launchedActivity.takeEmailVerificationLink());
                activityIntentData.set(launchedActivity.getIntent().getDataString());
            });
            JSONObject pending = auth.emailVerification(null, false);
            assertNotNull(pending);
            assertEquals("new warm link owns durable storage", warmToken, pending.getString("token"));
            assertNull("stale cold handoff is cleared before the warm link is forwarded", staleHandoff.get());
            assertEquals("Activity intent retains only the reviewed settings route",
                    "https://heartbeat-checker.ru/app/settings", activityIntentData.get());
        } finally {
            try {
                finishMainActivity(activity);
            } finally {
                clearAuthStore(auth);
            }
        }
    }

    private static MainActivity launchMainActivity(Intent intent) {
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TASK);
        return (MainActivity) InstrumentationRegistry.getInstrumentation().startActivitySync(intent);
    }

    private static void finishMainActivity(MainActivity activity) {
        if (activity == null) return;
        Instrumentation instrumentation = InstrumentationRegistry.getInstrumentation();
        instrumentation.runOnMainSync(activity::finish);
        instrumentation.waitForIdleSync();
        AtomicBoolean destroyed = new AtomicBoolean();
        long deadline = android.os.SystemClock.uptimeMillis() + 3_000;
        while (android.os.SystemClock.uptimeMillis() < deadline) {
            instrumentation.runOnMainSync(() -> destroyed.set(activity.isDestroyed()));
            if (destroyed.get()) return;
            android.os.SystemClock.sleep(25);
        }
        instrumentation.runOnMainSync(() -> destroyed.set(activity.isDestroyed()));
        assertTrue("email-link Activity must reach DESTROYED after finish", destroyed.get());
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
            scenario.onActivity(activity -> found.set(containsVisibleText(activity.getWindow().getDecorView(), expected)));
            if (found.get()) return true;
            Thread.sleep(100);
        }
        return false;
    }

    private static boolean awaitVisibleDescription(ActivityScenario<MainActivity> scenario, String expected) throws InterruptedException {
        for (int attempt = 0; attempt < 100; attempt++) {
            AtomicBoolean found = new AtomicBoolean();
            scenario.onActivity(activity -> found.set(findDescription(activity.getWindow().getDecorView(), expected) != null));
            if (found.get()) return true;
            Thread.sleep(100);
        }
        return false;
    }

    private static boolean awaitEnabledControl(ActivityScenario<MainActivity> scenario, String label) throws InterruptedException {
        for (int attempt = 0; attempt < 100; attempt++) {
            AtomicBoolean enabled = new AtomicBoolean();
            scenario.onActivity(activity -> enabled.set(isEnabledControl(activity.getWindow().getDecorView(), label)));
            if (enabled.get()) return true;
            Thread.sleep(50);
        }
        return false;
    }

    private static boolean isEnabledControl(View root, String label) {
        View found = findTextView(root, label);
        if (found == null) return false;
        View current = found;
        while (current != null) {
            if (!current.isEnabled() || current.getAlpha() < 0.99f) return false;
            if (current == root || !(current.getParent() instanceof View)) return true;
            current = (View) current.getParent();
        }
        return false;
    }

    private static void tapText(ActivityScenario<MainActivity> scenario, String text) throws InterruptedException {
        tapMatchingView(scenario, text, false);
    }

    private static void scrollToTop(ActivityScenario<MainActivity> scenario) throws InterruptedException {
        scenario.onActivity(activity -> {
            View scroll = findScrollView(activity.getWindow().getDecorView());
            assertNotNull("public landing must expose its scrolling content", scroll);
            scroll.scrollTo(0, 0);
        });
        InstrumentationRegistry.getInstrumentation().waitForIdleSync();
        Thread.sleep(100);
    }

    private static void scrollToText(ActivityScenario<MainActivity> scenario, String text) throws InterruptedException {
        scenario.onActivity(activity -> {
            View root = activity.getWindow().getDecorView();
            View foundScroll = findScrollView(root);
            View target = findTextView(root, text);
            assertTrue("public landing must expose its scroll container", foundScroll instanceof ViewGroup);
            assertNotNull("scroll target is not rendered: " + text, target);
            ViewGroup scroll = (ViewGroup) foundScroll;
            Rect targetBounds = new Rect(0, 0, target.getWidth(), target.getHeight());
            scroll.offsetDescendantRectToMyCoords(target, targetBounds);
            int offset = Math.max(0, targetBounds.top - Math.max(0, (scroll.getHeight() - target.getHeight()) / 2));
            scroll.scrollTo(0, offset);
        });
        InstrumentationRegistry.getInstrumentation().waitForIdleSync();
        Thread.sleep(100);
    }

    private static View findScrollView(View view) {
        for (Class<?> type = view.getClass(); type != null; type = type.getSuperclass()) {
            if (type.getSimpleName().contains("ScrollView")) return view;
        }
        if (!(view instanceof ViewGroup)) return null;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            View found = findScrollView(group.getChildAt(index));
            if (found != null) return found;
        }
        return null;
    }

    private static void tapDescription(ActivityScenario<MainActivity> scenario, String description) throws InterruptedException {
        tapMatchingView(scenario, description, true);
    }

    private static void tapMatchingView(ActivityScenario<MainActivity> scenario, String value, boolean description) throws InterruptedException {
        AtomicReference<float[]> coordinates = new AtomicReference<>();
        scenario.onActivity(activity -> {
            View root = activity.getWindow().getDecorView();
            View found = description ? findDescription(root, value) : findTextView(root, value);
            assertNotNull("visible control not found: " + value, found);
            int[] location = new int[2];
            found.getLocationOnScreen(location);
            float x = location[0] + found.getWidth() / 2f;
            float y = location[1] + found.getHeight() / 2f;
            coordinates.set(new float[] {x, y});
        });
        float[] point = coordinates.get();
        assertNotNull(point);
        awaitAppInputWindow(scenario, "tap '" + value + "'");
        long downTime = android.os.SystemClock.uptimeMillis();
        MotionEvent down = MotionEvent.obtain(downTime, downTime, MotionEvent.ACTION_DOWN, point[0], point[1], 0);
        down.setSource(InputDevice.SOURCE_TOUCHSCREEN);
        try {
            assertAppInputWindow(scenario, "before DOWN for '" + value + "'");
            InstrumentationRegistry.getInstrumentation().sendPointerSync(down);
        } finally {
            down.recycle();
        }
        MotionEvent up = MotionEvent.obtain(downTime, android.os.SystemClock.uptimeMillis(), MotionEvent.ACTION_UP, point[0], point[1], 0);
        up.setSource(InputDevice.SOURCE_TOUCHSCREEN);
        try {
            assertAppInputWindow(scenario, "before UP for '" + value + "'");
            InstrumentationRegistry.getInstrumentation().sendPointerSync(up);
        } finally {
            up.recycle();
        }
        Thread.sleep(100);
    }

    private static void awaitAppInputWindow(ActivityScenario<MainActivity> scenario, String action)
            throws InterruptedException {
        long deadline = android.os.SystemClock.uptimeMillis() + 3_000;
        String lastState;
        do {
            lastState = appInputWindowState(scenario);
            if ("ready".equals(lastState)) return;
            android.os.SystemClock.sleep(100);
        } while (android.os.SystemClock.uptimeMillis() < deadline);
        lastState = appInputWindowState(scenario);
        assertEquals("Refusing to inject " + action + "; app must own the focused, visible input window. " + lastState,
                "ready", lastState);
    }

    private static void assertAppInputWindow(ActivityScenario<MainActivity> scenario, String action) {
        String state = appInputWindowState(scenario);
        assertEquals("Refusing to inject " + action + "; focused window changed. " + state, "ready", state);
    }

    private static String appInputWindowState(ActivityScenario<MainActivity> scenario) {
        AtomicBoolean activityReady = new AtomicBoolean();
        scenario.onActivity(activity -> {
            View decor = activity.getWindow().getDecorView();
            activityReady.set(activity.getLifecycle().getCurrentState()
                    .isAtLeast(androidx.lifecycle.Lifecycle.State.RESUMED)
                    && decor.hasWindowFocus() && isActuallyVisible(decor));
        });
        AccessibilityNodeInfo activeRoot = InstrumentationRegistry.getInstrumentation()
                .getUiAutomation().getRootInActiveWindow();
        String activePackage = "<no-active-window>";
        if (activeRoot != null) {
            try {
                CharSequence packageName = activeRoot.getPackageName();
                activePackage = packageName == null ? "<unknown-package>" : packageName.toString();
            } finally {
                activeRoot.recycle();
            }
        }
        return activityReady.get() && "ru.heartbeatchecker.app".equals(activePackage)
                ? "ready"
                : "activityResumedVisibleAndFocused=" + activityReady.get() + ", activeWindowPackage=" + activePackage;
    }

    private static void setInputText(ActivityScenario<MainActivity> scenario, String label, String value) {
        AtomicBoolean changed = new AtomicBoolean();
        scenario.onActivity(activity -> {
            View field = findDescription(activity.getWindow().getDecorView(), label);
            assertTrue("React Native field must expose an accessibility label: " + label, field instanceof EditText);
            ((EditText) field).setText(value);
            changed.set(true);
        });
        assertTrue(changed.get());
    }

    private static View findTextView(View view, String text) {
        if (view instanceof TextView && text.contentEquals(((TextView) view).getText())) return view;
        if (!(view instanceof ViewGroup)) return null;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            View found = findTextView(group.getChildAt(index), text);
            if (found != null) return found;
        }
        return null;
    }

    private static View findDescription(View view, String description) {
        CharSequence contentDescription = view.getContentDescription();
        if (contentDescription != null && description.contentEquals(contentDescription)) return view;
        if (!(view instanceof ViewGroup)) return null;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            View found = findDescription(group.getChildAt(index), description);
            if (found != null) return found;
        }
        return null;
    }

    private static void saveScreenshot(ActivityScenario<MainActivity> scenario, String expected, String forbidden, String name)
            throws InterruptedException {
        android.app.Instrumentation instrumentation = InstrumentationRegistry.getInstrumentation();
        awaitAppInputWindow(scenario, "capture " + name);
        instrumentation.waitForIdleSync();
        assertScreenMarkers(scenario, expected, forbidden);

        CountDownLatch frameCommitted = new CountDownLatch(1);
        scenario.onActivity(activity -> {
            View decor = activity.getWindow().getDecorView();
            decor.postOnAnimation(() -> {
                decor.getViewTreeObserver().registerFrameCommitCallback(frameCommitted::countDown);
                decor.invalidate();
            });
        });
        assertTrue("React Native screen frame was not committed before screenshot: " + name,
                frameCommitted.await(5, TimeUnit.SECONDS));
        instrumentation.waitForIdleSync();
        assertAppInputWindow(scenario, "capture " + name);
        assertScreenMarkers(scenario, expected, forbidden);

        String additionalTestOutputDir = InstrumentationRegistry.getArguments().getString("additionalTestOutputDir");
        if (additionalTestOutputDir == null || additionalTestOutputDir.isEmpty()
                || !new File(additionalTestOutputDir).isAbsolute()) {
            throw new AssertionError("Android Gradle Plugin did not provide additionalTestOutputDir");
        }
        File directory = new File(additionalTestOutputDir, "auth-qa");
        if (!directory.isDirectory() && !directory.mkdirs()) throw new AssertionError("could not create auth screenshot directory");
        Bitmap screenshot = instrumentation.getUiAutomation().takeScreenshot();
        if (screenshot == null) throw new AssertionError("could not capture React Native screen: " + name);
        try {
            assertAppInputWindow(scenario, "after capture " + name);
            File file = new File(directory, name);
            assertScreenMarkers(scenario, expected, forbidden);
            try (FileOutputStream output = new FileOutputStream(file)) {
                if (!screenshot.compress(Bitmap.CompressFormat.PNG, 100, output)) {
                    throw new AssertionError("could not encode screenshot: " + name);
                }
            } catch (Exception failure) {
                throw new AssertionError("could not save auth screenshot: " + name, failure);
            }
        } finally {
            screenshot.recycle();
        }
    }

    private static Bitmap captureSignalRegion(ActivityScenario<MainActivity> scenario, String action,
            List<Rect> sampledBounds)
            throws InterruptedException {
        Instrumentation instrumentation = InstrumentationRegistry.getInstrumentation();
        awaitAppInputWindow(scenario, action);
        CountDownLatch frameCommitted = new CountDownLatch(1);
        scenario.onActivity(activity -> {
            View decor = activity.getWindow().getDecorView();
            decor.postOnAnimation(() -> {
                decor.getViewTreeObserver().registerFrameCommitCallback(frameCommitted::countDown);
                decor.invalidate();
            });
        });
        assertTrue("React Native frame did not commit before " + action,
                frameCommitted.await(5, TimeUnit.SECONDS));
        awaitAppInputWindow(scenario, action);

        AtomicReference<Rect> bounds = new AtomicReference<>();
        scenario.onActivity(activity -> {
            View region = findTestId(activity.getWindow().getDecorView(), "signal-motion-region");
            assertNotNull("fixed ECG wave must expose its native testID", region);
            Rect visible = new Rect();
            assertTrue("ECG test region must be on screen", region.getGlobalVisibleRect(visible));
            assertTrue("ECG test region must have a rendered area", visible.width() >= 100 && visible.height() >= 50);
            assertEquals("ECG crop must include the full native test region", region.getWidth(), visible.width());
            assertEquals("ECG crop must include the full native test region", region.getHeight(), visible.height());
            bounds.set(visible);
        });

        Bitmap screen = instrumentation.getUiAutomation().takeScreenshot();
        if (screen == null) throw new AssertionError("could not capture native ECG frame");
        try {
            Rect crop = bounds.get();
            assertNotNull("ECG crop bounds must be available", crop);
            assertTrue("ECG crop must stay within the captured screen", crop.left >= 0 && crop.top >= 0
                    && crop.right <= screen.getWidth() && crop.bottom <= screen.getHeight());
            awaitAppInputWindow(scenario, action + " after screenshot");
            sampledBounds.add(new Rect(crop));
            return Bitmap.createBitmap(screen, crop.left, crop.top, crop.width(), crop.height());
        } finally {
            screen.recycle();
        }
    }

    private static View findTestId(View view, String testId) {
        Object value = view.getTag(com.facebook.react.R.id.react_test_id);
        if (testId.equals(value)) return view;
        if (!(view instanceof ViewGroup)) return null;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            View found = findTestId(group.getChildAt(index), testId);
            if (found != null) return found;
        }
        return null;
    }

    private static int countDistinctFrames(List<Bitmap> frames) {
        List<Integer> hashes = new ArrayList<>();
        for (Bitmap frame : frames) {
            int[] pixels = pixels(frame);
            int hash = Arrays.hashCode(pixels);
            if (!hashes.contains(hash)) hashes.add(hash);
        }
        return hashes.size();
    }

    private static int countVisiblyDistinctFrames(List<Bitmap> frames) {
        List<int[]> representatives = new ArrayList<>();
        for (Bitmap frame : frames) {
            int[] candidate = pixels(frame);
            boolean distinct = true;
            for (int[] previous : representatives) {
                if (countChangedPixels(previous, candidate) < 24) {
                    distinct = false;
                    break;
                }
            }
            if (distinct) representatives.add(candidate);
        }
        return representatives.size();
    }

    private static int countChangedPixels(int[] first, int[] current) {
        if (current.length != first.length) throw new AssertionError("ECG crop dimensions changed between frames");
        int changed = 0;
        for (int pixel = 0; pixel < first.length; pixel++) {
            int a = first[pixel], b = current[pixel];
            if (Math.max(Math.max(Math.abs(((a >> 16) & 255) - ((b >> 16) & 255)),
                            Math.abs(((a >> 8) & 255) - ((b >> 8) & 255))),
                    Math.abs((a & 255) - (b & 255))) >= 32) changed++;
        }
        return changed;
    }

    private static void assertStableCropBounds(List<Rect> bounds) {
        assertTrue("at least one full ECG crop is required", !bounds.isEmpty());
        Rect expected = bounds.get(0);
        for (int index = 1; index < bounds.size(); index++) {
            assertEquals("native ECG crop must not move between frames", expected, bounds.get(index));
        }
    }

    private static int maxChangedPixelsFromFirst(List<Bitmap> frames) {
        if (frames.size() < 2) return 0;
        int[] first = pixels(frames.get(0));
        int maximum = 0;
        for (int index = 1; index < frames.size(); index++) {
            int[] current = pixels(frames.get(index));
            if (current.length != first.length) throw new AssertionError("ECG crop dimensions changed between frames");
            int changed = 0;
            for (int pixel = 0; pixel < first.length; pixel++) {
                int a = first[pixel], b = current[pixel];
                if (Math.max(Math.max(Math.abs(((a >> 16) & 255) - ((b >> 16) & 255)),
                                Math.abs(((a >> 8) & 255) - ((b >> 8) & 255))),
                        Math.abs((a & 255) - (b & 255))) >= 32) changed++;
            }
            maximum = Math.max(maximum, changed);
        }
        return maximum;
    }

    private static int[] pixels(Bitmap bitmap) {
        int[] result = new int[bitmap.getWidth() * bitmap.getHeight()];
        bitmap.getPixels(result, 0, bitmap.getWidth(), 0, 0, bitmap.getWidth(), bitmap.getHeight());
        return result;
    }

    private static void saveMotionFrames(List<Bitmap> frames, List<Rect> bounds,
            String prefix, long[] offsetsMs, String mode)
            throws IOException {
        String outputPath = InstrumentationRegistry.getArguments().getString("additionalTestOutputDir");
        if (outputPath == null || outputPath.isEmpty() || !new File(outputPath).isAbsolute())
            throw new AssertionError("Android Gradle Plugin did not provide additionalTestOutputDir");
        File directory = new File(outputPath, "motion-qa");
        if (!directory.isDirectory() && !directory.mkdirs()) throw new IOException("could not create motion QA output directory");
        Rect crop = bounds.get(0);
        StringBuilder metrics = new StringBuilder("mode=").append(mode)
                .append("\ncrop=").append(crop.left).append(',').append(crop.top).append(',')
                .append(crop.width()).append(',').append(crop.height())
                .append("\nunique_frame_hashes=").append(countDistinctFrames(frames))
                .append("\nvisibly_distinct_frames=").append(countVisiblyDistinctFrames(frames))
                .append("\nmax_changed_pixels_from_first=").append(maxChangedPixelsFromFirst(frames)).append('\n');
        int[] first = pixels(frames.get(0));
        for (int index = 0; index < frames.size(); index++) {
            Bitmap frame = frames.get(index);
            File file = new File(directory, prefix + "-" + index + ".png");
            try (FileOutputStream output = new FileOutputStream(file)) {
                if (!frame.compress(Bitmap.CompressFormat.PNG, 100, output))
                    throw new IOException("could not encode ECG QA frame " + file.getName());
            }
            int[] current = pixels(frame);
            int changed = 0;
            for (int pixel = 0; pixel < first.length; pixel++) {
                int a = first[pixel], b = current[pixel];
                if (Math.max(Math.max(Math.abs(((a >> 16) & 255) - ((b >> 16) & 255)),
                                Math.abs(((a >> 8) & 255) - ((b >> 8) & 255))),
                        Math.abs((a & 255) - (b & 255))) >= 32) changed++;
            }
            metrics.append(file.getName()).append(" offset_ms=").append(offsetsMs[index])
                    .append(" changed_pixels_from_first=").append(changed)
                    .append(" hash=").append(Arrays.hashCode(current)).append('\n');
        }
        try (FileOutputStream output = new FileOutputStream(new File(directory, prefix + "-metrics.txt"))) {
            output.write(metrics.toString().getBytes(StandardCharsets.UTF_8));
        }
    }

    private static void recycle(List<Bitmap> frames) {
        for (Bitmap frame : frames) frame.recycle();
    }

    private static void sleepUntil(long deadlineElapsedMs) throws InterruptedException {
        while (true) {
            long remaining = deadlineElapsedMs - android.os.SystemClock.elapsedRealtime();
            if (remaining <= 0) return;
            Thread.sleep(Math.min(remaining, 100));
        }
    }

    private static Map<String, String> readAnimationScales() throws IOException {
        Map<String, String> values = new LinkedHashMap<>();
        for (String key : ANIMATION_SCALE_SETTINGS) values.put(key, readAnimationScale(key));
        return values;
    }

    private static String readAnimationScale(String key) throws IOException {
        assertTrue("unexpected Android animation scale setting", Arrays.asList(ANIMATION_SCALE_SETTINGS).contains(key));
        String value = shell("settings get global " + key).trim();
        return "null".equals(value) ? null : value;
    }

    private static void setAnimationScales(String value) throws IOException {
        for (String key : ANIMATION_SCALE_SETTINGS) setAnimationScale(key, value);
    }

    private static void setAnimationScale(String key, String value) throws IOException {
        assertTrue("unexpected Android animation scale setting", Arrays.asList(ANIMATION_SCALE_SETTINGS).contains(key));
        if (value == null) {
            shell("settings delete global " + key);
        } else {
            assertTrue("animation scale must be a numeric setting", value.matches("[0-9]+(?:\\.[0-9]+)?"));
            shell("settings put global " + key + " " + value);
        }
        assertEquals("Android animation setting did not update: " + key, value, readAnimationScale(key));
    }

    private static void restoreAnimationScales(Map<String, String> values) throws IOException {
        for (String key : ANIMATION_SCALE_SETTINGS) setAnimationScale(key, values.get(key));
    }

    private static String shell(String command) throws IOException {
        ParcelFileDescriptor descriptor = InstrumentationRegistry.getInstrumentation()
                .getUiAutomation().executeShellCommand(command);
        try (InputStream input = new ParcelFileDescriptor.AutoCloseInputStream(descriptor);
                ByteArrayOutputStream output = new ByteArrayOutputStream()) {
            input.transferTo(output);
            return output.toString(StandardCharsets.UTF_8);
        }
    }

    private static void waitForBundledLandingImages() throws InterruptedException {
        android.app.Instrumentation instrumentation = InstrumentationRegistry.getInstrumentation();
        instrumentation.waitForIdleSync();
        Thread.sleep(500);
        instrumentation.waitForIdleSync();
    }

    private static void assertScreenMarkers(ActivityScenario<MainActivity> scenario, String expected, String forbidden) {
        AtomicBoolean expectedVisible = new AtomicBoolean();
        AtomicBoolean forbiddenVisible = new AtomicBoolean();
        scenario.onActivity(activity -> {
            View root = activity.getWindow().getDecorView();
            expectedVisible.set(containsVisibleText(root, expected));
            forbiddenVisible.set(containsVisibleText(root, forbidden));
        });
        assertTrue("expected visible screen marker missing: " + expected, expectedVisible.get());
        assertFalse("unexpected screen marker is visible: " + forbidden, forbiddenVisible.get());
    }

    private static boolean containsVisibleText(View view, String expected) {
        if (view instanceof TextView && isActuallyVisible(view)
                && expected.contentEquals(((TextView) view).getText())) return true;
        if (!(view instanceof ViewGroup)) return false;
        ViewGroup group = (ViewGroup) view;
        for (int index = 0; index < group.getChildCount(); index++) {
            if (containsVisibleText(group.getChildAt(index), expected)) return true;
        }
        return false;
    }

    private static boolean isActuallyVisible(View view) {
        Rect visible = new Rect();
        return view.isAttachedToWindow() && view.isShown() && view.getWidth() > 0 && view.getHeight() > 0
                && view.getGlobalVisibleRect(visible) && !visible.isEmpty();
    }
}
