package ru.heartbeatchecker.app;

import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.activity.ComponentActivity;
import androidx.activity.OnBackPressedCallback;
import androidx.browser.customtabs.CustomTabsCallback;
import androidx.browser.customtabs.CustomTabsService;
import androidx.browser.trusted.TrustedWebActivityIntentBuilder;

import com.google.androidbrowserhelper.trusted.TwaLauncher;

public class MainActivity extends ComponentActivity {
    interface BrowserLauncher {
        void launch(String url, Runnable completed, Runnable unavailable);
        void destroy();
    }

    private BrowserLauncher launcher;
    private String targetUrl;
    private boolean launching;
    private boolean handedOff;
    private boolean knownFailed;
    private boolean destroyed;
    private int attempt;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                // Cancel the native launch instead of retaining it in a background task.
                finish();
            }
        });
        if (state != null && state.getBoolean("handedOff")) {
            handedOff = true;
            finish();
            return;
        }
        targetUrl = target(getIntent());
        if (targetUrl == null) {
            finish();
            return;
        }
        knownFailed = state != null && state.getBoolean("knownFailed");
        if (knownFailed) showStatus(true);
        else launch();
    }

    @Override
    protected void onResume() {
        super.onResume();
        try { PushWork.permissionsChanged(getApplicationContext()); }
        catch (RuntimeException ignored) { /* Push availability must not block the cabinet. */ }
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        String next = target(intent);
        if (next == null) {
            finish();
            return;
        }
        if (launching || handedOff || destroyed) return;
        setIntent(intent);
        targetUrl = next;
        launch();
    }

    private String target(Intent intent) {
        if (intent == null) return null;
        if (Intent.ACTION_MAIN.equals(intent.getAction())) return LinkPolicy.HOME;
        if (Intent.ACTION_VIEW.equals(intent.getAction())) return LinkPolicy.appLink(intent.getDataString());
        // Removed share/shortcut intents are rejected without reading their payload.
        return null;
    }

    private void launch() {
        if (launching || handedOff || destroyed || isFinishing()) return;
        cleanupLauncher();
        launching = true;
        knownFailed = false;
        int request = ++attempt;
        showStatus(false);
        try {
            launcher = createBrowserLauncher();
            launcher.launch(targetUrl, () -> {
                if (!current(request)) return;
                handedOff = true;
                launching = false;
                // Completion confirms browser dispatch, not successful page loading.
                finish();
            }, () -> unavailable(request));
        } catch (ActivityNotFoundException | SecurityException e) {
            unavailable(request);
        }
    }

    private boolean current(int request) {
        return request == attempt && launching && !handedOff && !destroyed && !isFinishing();
    }

    private void unavailable(int request) {
        if (!current(request)) return;
        launching = false;
        knownFailed = true;
        showStatus(true);
    }

    BrowserLauncher createBrowserLauncher() {
        return new BrowserLauncher() {
            private final TwaLauncher twa = new TwaLauncher(MainActivity.this);

            @Override
            public void launch(String url, Runnable completed, Runnable unavailable) {
                twa.launch(new TrustedWebActivityIntentBuilder(Uri.parse(url))
                                .setToolbarColor(Color.rgb(0, 127, 115)),
                        new CustomTabsCallback(), null, completed,
                        (context, builder, provider, complete) -> {
                            if (launcher != this || !launching || isFinishing() || destroyed) return;
                            if (provider == null) {
                                unavailable.run();
                                return;
                            }
                            try {
                                if (getPackageManager().resolveService(
                                        new Intent(CustomTabsService.ACTION_CUSTOM_TABS_CONNECTION)
                                                .setPackage(provider), 0) == null) {
                                    unavailable.run();
                                    return;
                                }
                                TwaLauncher.CCT_FALLBACK_STRATEGY.launch(context, builder, provider, complete);
                            } catch (ActivityNotFoundException | SecurityException e) {
                                unavailable.run();
                            }
                        });
            }

            @Override
            public void destroy() {
                twa.destroy();
            }
        };
    }

    private void cleanupLauncher() {
        BrowserLauncher previous = launcher;
        launcher = null;
        if (previous == null) return;
        try {
            previous.destroy();
        } catch (IllegalArgumentException ignored) {
            // ABH may unbind a service whose initial bind was unsuccessful.
        }
    }

    private void showStatus(boolean failed) {
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        LinearLayout content = new LinearLayout(this);
        content.setOrientation(LinearLayout.VERTICAL);
        content.setGravity(Gravity.CENTER);
        content.setPadding(dp(28), dp(32), dp(28), dp(32));
        scroll.addView(content);
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            if (Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
                view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            } else {
                view.setPadding(insets.getSystemWindowInsetLeft(), insets.getSystemWindowInsetTop(),
                        insets.getSystemWindowInsetRight(), insets.getSystemWindowInsetBottom());
            }
            return insets;
        });
        ImageView logo = new ImageView(this);
        logo.setImageResource(R.drawable.ic_heartbeat);
        logo.setImportantForAccessibility(android.view.View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        content.addView(logo, new LinearLayout.LayoutParams(dp(64), dp(64)));
        text(content, getString(R.string.app_name), 24, true);
        text(content, failed ? "Не удалось открыть кабинет" : "Открываем кабинет…", 18, false);
        if (failed) {
            text(content, "Установите или включите браузер с поддержкой Custom Tabs и повторите попытку.", 16, false);
            Button retry = button(content, "Повторить", this::launch);
            retry.setBackgroundTintList(ColorStateList.valueOf(Color.rgb(0, 127, 115)));
            retry.setTextColor(Color.WHITE);
            button(content, "Закрыть", this::finish);
        } else {
            ProgressBar progress = new ProgressBar(this);
            progress.setIndeterminateTintList(ColorStateList.valueOf(Color.rgb(0, 127, 115)));
            content.addView(progress, new LinearLayout.LayoutParams(dp(40), dp(40)));
        }
        setContentView(scroll);
        scroll.requestApplyInsets();
    }

    private void text(LinearLayout parent, String value, int size, boolean heading) {
        TextView text = new TextView(this);
        text.setText(value);
        text.setTextSize(size);
        text.setGravity(Gravity.CENTER);
        text.setPadding(0, dp(16), 0, dp(12));
        if (heading) {
            text.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
            text.setAccessibilityHeading(true);
        }
        parent.addView(text);
    }

    private Button button(LinearLayout parent, String label, Runnable action) {
        Button button = new Button(this);
        button.setText(label);
        button.setTextSize(16);
        button.setAllCaps(false);
        button.setSingleLine(false);
        button.setMinHeight(dp(48));
        button.setOnClickListener(view -> action.run());
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        params.setMargins(0, dp(6), 0, dp(6));
        parent.addView(button, params);
        return button;
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    @Override
    protected void onSaveInstanceState(Bundle state) {
        super.onSaveInstanceState(state);
        state.putBoolean("handedOff", handedOff);
        state.putBoolean("knownFailed", knownFailed);
    }

    @Override
    protected void onDestroy() {
        destroyed = true;
        ++attempt;
        cleanupLauncher();
        super.onDestroy();
    }
}
