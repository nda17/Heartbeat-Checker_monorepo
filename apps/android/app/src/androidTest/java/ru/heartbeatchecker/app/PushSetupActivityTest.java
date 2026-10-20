package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.TextView;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class PushSetupActivityTest {
    private static final String PAIRING = "987e6543-e21b-12d3-a456-426614174000";
    private static final String CODE = "042681";
    private Context context;
    private PushStore store;

    @Before
    public void seedPendingPair() throws Exception {
        context = ApplicationProvider.getApplicationContext();
        store = new PushStore(context);
        PushWork.locallyBlocked = false;
        store.update(state -> {
            Iterator<String> keys = state.keys();
            List<String> remove = new ArrayList<>();
            while (keys.hasNext()) remove.add(keys.next());
            for (String key : remove) state.remove(key);
            state.put("enabled", false)
                    .put("credential", "123e4567-e89b-12d3-a456-426614174000.abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG")
                    .put("pair", new JSONObject()
                            .put("id", PAIRING)
                            .put("verificationCode", CODE)
                            .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 300_000).toString())
                            .put("confirmationUrl", PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING));
        });
    }

    @Test
    public void persistedPairingShowsSixDigitCodeAndWaitsForManualContinue() {
        Intent connect = new Intent(Intent.ACTION_VIEW,
                Uri.parse("https://heartbeat-checker.ru/android/push/connect"))
                .setClass(context, PushSetupActivity.class);
        try (ActivityScenario<PushSetupActivity> scenario = ActivityScenario.launch(connect)) {
            scenario.onActivity(activity -> {
                assertText(activity, "Код для сверки в кабинете: " + CODE);
                assertButton(activity, "Продолжить подтверждение");
                assertFalse("showing a comparison code must not open the browser automatically",
                        activity.isFinishing());
            });
        }
    }

    private static void assertText(PushSetupActivity activity, String expected) {
        if (findText(activity.getWindow().getDecorView(), expected) == null)
            throw new AssertionError("expected visible text: " + expected);
    }

    private static void assertButton(PushSetupActivity activity, String expected) {
        if (findButton(activity.getWindow().getDecorView(), expected) == null)
            throw new AssertionError("expected visible button: " + expected);
    }

    private static TextView findText(View view, String expected) {
        if (view instanceof TextView && expected.contentEquals(((TextView) view).getText())) return (TextView) view;
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
        if (view instanceof Button && expected.contentEquals(((Button) view).getText())) return (Button) view;
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
