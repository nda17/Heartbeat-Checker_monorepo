package ru.heartbeatchecker.app;

import android.app.Activity;
import android.content.Intent;
import android.net.Uri;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.TextView;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.IOException;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class MainActivitySmokeTest {
    private static final String SHORTCUT_ACTION = "ru.heartbeatchecker.app.DRAFTS";
    private static final String SHARED_URL = "https://example.invalid/path";

    @Test
    public void offlineDraftFlowSurvivesRecreationAndBackAndRejectsForeignViewLink() {
        Intent shortcut = new Intent(ApplicationProvider.getApplicationContext(), MainActivity.class)
                .setAction(SHORTCUT_ACTION);
        try (ActivityScenario<MainActivity> scenario = ActivityScenario.launch(shortcut)) {
            scenario.onActivity(activity -> {
                try {
                    new DraftStore(activity.getNoBackupFilesDir()).clear();
                } catch (IOException error) {
                    throw new AssertionError("could not reset isolated draft store", error);
                }
                assertText(activity, "Черновики адресов");
            });
        }

        Intent share = new Intent(ApplicationProvider.getApplicationContext(), MainActivity.class)
                .setAction(Intent.ACTION_SEND)
                .setType("text/plain")
                .putExtra(Intent.EXTRA_TEXT, SHARED_URL);
        try (ActivityScenario<MainActivity> scenario = ActivityScenario.launch(share)) {
            scenario.onActivity(activity -> {
                assertText(activity, SHARED_URL);
                assertText(activity, "Приложение не запрашивает этот адрес и не проверяет его доступность.");
                clickButton(activity, "Сохранить на устройстве");
                assertContainsText(activity, "Сохранено на устройстве.");
                try {
                    assertEquals(1, new DraftStore(activity.getNoBackupFilesDir()).load().size());
                } catch (IOException error) {
                    throw new AssertionError("saved offline draft could not be read", error);
                }
            });

            scenario.recreate();
            scenario.onActivity(activity -> {
                assertText(activity, "Черновик адреса");
                assertText(activity, SHARED_URL);
                clickButton(activity, "К списку черновиков");
                assertText(activity, SHARED_URL);
            });

            scenario.onActivity(activity -> {
                Intent invalidView = new Intent(Intent.ACTION_VIEW, Uri.parse("https://attacker.example/app"))
                        .setClass(activity, MainActivity.class);
                activity.onNewIntent(invalidView);
                assertText(activity, "Эта ссылка не открывается внутри приложения.");
            });

            scenario.onActivity(activity -> {
                activity.onNewIntent(share);
                assertText(activity, SHARED_URL);
                activity.getOnBackPressedDispatcher().onBackPressed();
                assertText(activity, "Черновики адресов");
                assertText(activity, SHARED_URL);
            });
        }
    }

    private static void assertText(Activity activity, String expected) {
        if (findText(activity.getWindow().getDecorView(), expected) == null) {
            throw new AssertionError("expected visible text: " + expected);
        }
    }

    private static void assertContainsText(Activity activity, String expected) {
        if (findContainingText(activity.getWindow().getDecorView(), expected) == null) {
            throw new AssertionError("expected visible text containing: " + expected);
        }
    }

    private static TextView findContainingText(View view, String expected) {
        if (view instanceof TextView && ((TextView) view).getText().toString().contains(expected)) {
            return (TextView) view;
        }
        if (view instanceof ViewGroup) {
            ViewGroup group = (ViewGroup) view;
            for (int i = 0; i < group.getChildCount(); i++) {
                TextView found = findContainingText(group.getChildAt(i), expected);
                if (found != null) return found;
            }
        }
        return null;
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
