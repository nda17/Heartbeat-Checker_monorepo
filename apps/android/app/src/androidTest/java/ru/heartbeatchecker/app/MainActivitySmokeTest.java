package ru.heartbeatchecker.app;

import android.app.Activity;
import android.content.ComponentName;
import android.content.Intent;
import android.content.pm.ActivityInfo;
import android.content.pm.PackageManager;
import android.content.pm.ResolveInfo;
import android.net.Uri;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.TextView;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import ru.heartbeatchecker.app.TestMainActivity.RecordingLauncher;

import java.util.List;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class MainActivitySmokeTest {
    private static final String INCIDENT_LINK = "https://heartbeat-checker.ru/app?incident="
            + "987e6543-e21b-12d3-a456-426614174000&monitor="
            + "123e4567-e89b-12d3-a456-426614174000";
    private static final String CANONICAL_INCIDENT_LINK = "https://heartbeat-checker.ru/app?monitor="
            + "123e4567-e89b-12d3-a456-426614174000&incident="
            + "987e6543-e21b-12d3-a456-426614174000";

    @Before
    public void resetLaunchers() {
        TestMainActivity.reset();
    }

    @Test
    public void mainLaunchesCabinetOnceAndBackExits() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(mainIntent())) {
            assertEquals(1, TestMainActivity.launchers.size());
            RecordingLauncher first = TestMainActivity.launchers.get(0);
            assertEquals(java.util.Collections.singletonList(LinkPolicy.HOME), first.urls);
            scenario.onActivity(activity -> {
                assertText(activity, "Открываем кабинет…");
                activity.onNewIntent(mainIntent());
                assertEquals("a pending launch must remain single-flight", 1, TestMainActivity.launchers.size());
                activity.getOnBackPressedDispatcher().onBackPressed();
                assertTrue("Back should close the native launch activity", activity.isFinishing());
            });
        }
    }

    @Test
    public void launcherIconTargetsMainActivityAndNoShareShortcutIsRegistered() throws Exception {
        android.content.Context context = ApplicationProvider.getApplicationContext();
        PackageManager packageManager = context.getPackageManager();
        Intent launcherIntent = new Intent(Intent.ACTION_MAIN)
                .addCategory(Intent.CATEGORY_LAUNCHER)
                .setPackage(context.getPackageName());
        List<ResolveInfo> launchers = packageManager.queryIntentActivities(launcherIntent, 0);
        assertEquals(1, launchers.size());
        assertEquals(MainActivity.class.getName(), launchers.get(0).activityInfo.name);

        Intent sendIntent = new Intent(Intent.ACTION_SEND)
                .setType("text/plain")
                .setPackage(context.getPackageName());
        assertTrue("the application must not register as a share target",
                packageManager.queryIntentActivities(sendIntent, PackageManager.MATCH_DEFAULT_ONLY).isEmpty());

        ActivityInfo info = packageManager.getActivityInfo(
                new ComponentName(context, MainActivity.class), PackageManager.GET_META_DATA);
        assertTrue("the removed drafts shortcut must not remain registered",
                info.metaData == null || !info.metaData.containsKey("android.app.shortcuts"));
    }

    @Test
    public void validViewLinkIsCanonicalizedAndForeignViewIsNeverDispatched() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(viewIntent(INCIDENT_LINK))) {
            assertEquals(1, TestMainActivity.launchers.size());
            assertEquals(java.util.Collections.singletonList(CANONICAL_INCIDENT_LINK),
                    TestMainActivity.launchers.get(0).urls);
        }

        TestMainActivity.reset();
        try (ActivityScenario<TestMainActivity> ignored = ActivityScenario.launch(
                viewIntent("https://heartbeat-checker.ru.attacker.test/app"))) {
            assertTrue("invalid VIEW input must be rejected before launcher construction",
                    TestMainActivity.launchers.isEmpty());
        }
    }

    @Test
    public void warmValidViewIntentDispatchesAfterCurrentAttemptHasFailed() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(mainIntent())) {
            scenario.onActivity(activity -> {
                TestMainActivity.launchers.get(0).fail();
                activity.onNewIntent(viewIntent(INCIDENT_LINK));
                assertEquals(Intent.ACTION_VIEW, activity.getIntent().getAction());
                assertEquals(INCIDENT_LINK, activity.getIntent().getDataString());
                // The new-intent assertion above covers dispatch; restore the launch intent
                // so ActivityScenario.close() can match and destroy its original activity.
                activity.setIntent(mainIntent());
            });

            assertEquals(2, TestMainActivity.launchers.size());
            assertEquals(java.util.Collections.singletonList(CANONICAL_INCIDENT_LINK),
                    TestMainActivity.launchers.get(1).urls);
        }
    }

    @Test
    public void removedShareShortcutAndUnknownActionsFinishWithoutLaunching() {
        Intent send = new Intent(ApplicationProvider.getApplicationContext(), TestMainActivity.class)
                .setAction(Intent.ACTION_SEND)
                .setType("text/plain")
                .putExtra(Intent.EXTRA_TEXT, INCIDENT_LINK);
        assertRejected(send);

        Intent legacyShortcut = new Intent(ApplicationProvider.getApplicationContext(), TestMainActivity.class)
                .setAction("ru.heartbeatchecker.app.DRAFTS")
                .putExtra("payload", INCIDENT_LINK);
        assertRejected(legacyShortcut);

        Intent unknown = new Intent(ApplicationProvider.getApplicationContext(), TestMainActivity.class)
                .setAction("ru.heartbeatchecker.app.UNKNOWN")
                .putExtra("payload", INCIDENT_LINK);
        assertRejected(unknown);
    }

    @Test
    public void pendingLaunchRecreationMakesOneFreshAttemptAndDestroysOldLauncher() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(mainIntent())) {
            RecordingLauncher first = TestMainActivity.launchers.get(0);
            scenario.recreate();
            assertEquals("restoring a pending launch should retry exactly once", 2,
                    TestMainActivity.launchers.size());
            RecordingLauncher second = TestMainActivity.launchers.get(1);
            assertTrue("recreated activity must release the old launcher", first.destroyed);
            assertEquals(java.util.Collections.singletonList(LinkPolicy.HOME), second.urls);

            scenario.onActivity(activity -> {
                first.complete();
                assertFalse("late completion from old activity must not finish the new attempt",
                        activity.isFinishing());
                assertText(activity, "Открываем кабинет…");
            });
        }
    }

    @Test
    public void failureSurvivesRecreationAndManualRetryUsesFreshLauncher() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(mainIntent())) {
            RecordingLauncher first = TestMainActivity.launchers.get(0);
            scenario.onActivity(activity -> {
                first.fail();
                assertText(activity, "Не удалось открыть кабинет");
                assertButton(activity, "Повторить");
                assertButton(activity, "Закрыть");
            });

            scenario.recreate();
            assertEquals("known failure must not automatically retry after recreation", 1,
                    TestMainActivity.launchers.size());
            scenario.onActivity(activity -> {
                assertText(activity, "Не удалось открыть кабинет");
                clickButton(activity, "Повторить");
                assertEquals(2, TestMainActivity.launchers.size());
                RecordingLauncher retry = TestMainActivity.launchers.get(1);
                assertTrue("retry must destroy the previous launcher", first.destroyed);
                first.complete();
                assertFalse("stale completion must not finish the retry attempt", activity.isFinishing());
                retry.fail();
                assertText(activity, "Не удалось открыть кабинет");
                assertEquals("a failed retry must wait for another manual action", 2,
                        TestMainActivity.launchers.size());
            });
        }
    }

    @Test
    public void handoffCompletionFinishesActivity() {
        try (ActivityScenario<TestMainActivity> scenario = ActivityScenario.launch(mainIntent())) {
            scenario.onActivity(activity -> {
                TestMainActivity.launchers.get(0).complete();
                assertTrue("completion means dispatch to the browser and closes the launcher activity",
                        activity.isFinishing());
            });
        }
    }

    private static void assertRejected(Intent intent) {
        try (ActivityScenario<TestMainActivity> ignored = ActivityScenario.launch(intent)) {
            assertTrue("removed or unknown intent must not construct a browser launcher",
                    TestMainActivity.launchers.isEmpty());
        }
    }

    private static Intent mainIntent() {
        return new Intent(ApplicationProvider.getApplicationContext(), TestMainActivity.class)
                .setAction(Intent.ACTION_MAIN);
    }

    private static Intent viewIntent(String url) {
        return new Intent(Intent.ACTION_VIEW, Uri.parse(url))
                .setClass(ApplicationProvider.getApplicationContext(), TestMainActivity.class);
    }

    private static void assertText(Activity activity, String expected) {
        if (findText(activity.getWindow().getDecorView(), expected) == null) {
            throw new AssertionError("expected visible text: " + expected);
        }
    }

    private static void assertButton(Activity activity, String expected) {
        if (findButton(activity.getWindow().getDecorView(), expected) == null) {
            throw new AssertionError("expected visible button: " + expected);
        }
    }

    private static void clickButton(Activity activity, String label) {
        Button button = findButton(activity.getWindow().getDecorView(), label);
        if (button == null || !button.performClick()) {
            throw new AssertionError("could not click button: " + label);
        }
    }

    private static TextView findText(View view, String expected) {
        if (view instanceof TextView && expected.contentEquals(((TextView) view).getText())) {
            return (TextView) view;
        }
        if (view instanceof ViewGroup) {
            ViewGroup group = (ViewGroup) view;
            for (int i = 0; i < group.getChildCount(); i++) {
                TextView found = findText(group.getChildAt(i), expected);
                if (found != null) return found;
            }
        }
        return null;
    }

    private static Button findButton(View view, String expected) {
        if (view instanceof Button && expected.contentEquals(((Button) view).getText())) {
            return (Button) view;
        }
        if (view instanceof ViewGroup) {
            ViewGroup group = (ViewGroup) view;
            for (int i = 0; i < group.getChildCount(); i++) {
                Button found = findButton(group.getChildAt(i), expected);
                if (found != null) return found;
            }
        }
        return null;
    }
}
