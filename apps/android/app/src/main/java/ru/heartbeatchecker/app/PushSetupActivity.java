package ru.heartbeatchecker.app;

import android.Manifest;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.graphics.drawable.StateListDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.view.WindowInsets;
import android.view.Gravity;
import android.widget.ImageView;
import android.widget.ProgressBar;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.activity.ComponentActivity;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.browser.customtabs.CustomTabsCallback;
import androidx.browser.customtabs.CustomTabsService;
import androidx.browser.trusted.TrustedWebActivityIntentBuilder;

import com.google.androidbrowserhelper.trusted.TwaLauncher;

import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Base64;

public final class PushSetupActivity extends ComponentActivity {
    private PushStore store;
    private LinearLayout content;
    private TwaLauncher browser;
    private boolean busy;
    private boolean destroyed;
    private boolean browserOpening;
    private String incomingPair;
    private String problem = "";
    private final ActivityResultLauncher<String> permission = registerForActivityResult(
            new ActivityResultContracts.RequestPermission(), granted -> {
                if (granted) prepareToken();
                else { busy = false; problem = "Разрешите уведомления в настройках Android, чтобы подключить устройство."; render(); }
            });

    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        store = new PushStore(this);
        if (!readIntent(getIntent())) { finish(); return; }
        render();
        if (!incomingPair.isEmpty()) completePending(incomingPair, false);
    }

    @Override protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        if (!readIntent(intent)) { finish(); return; }
        setIntent(intent);
        if (!busy && !incomingPair.isEmpty()) completePending(incomingPair, false);
        else render();
    }

    private boolean readIntent(Intent intent) {
        if (intent == null || !Intent.ACTION_VIEW.equals(intent.getAction())) return false;
        String id = PushContract.setupPair(intent.getDataString());
        if (id == null) return false;
        incomingPair = id; return true;
    }

    @Override protected void onResume() {
        super.onResume();
        if (store != null) {
            try { PushWork.permissionsChanged(this); } catch (RuntimeException ignored) { }
            if (!busy) render();
        }
    }

    private void connect() {
        if (busy) return;
        busy = true; problem = ""; render();
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED)
            permission.launch(Manifest.permission.POST_NOTIFICATIONS);
        else prepareToken();
    }

    private void prepareToken() {
        if (destroyed || isFinishing()) return;
        if (!PushNotifications.permitted(this)) { finishOperation("Включите уведомления приложения и канал «Инциденты» в настройках Android."); return; }
        PushSdk.token(this, new PushSdk.TokenResult() {
            @Override public void ready(String token) {
                if (destroyed || isFinishing()) return;
                PushWork.IO.execute(PushSetupActivity.this::createOrResume);
            }
            @Override public void unavailable() {
                finishOperation("RuStore Push сейчас недоступен на устройстве. Проверьте приложение RuStore и разрешение его работы в фоне.");
            }
        });
    }

    private void createOrResume() {
        long revocation = PushWork.localRevocation;
        try {
            if (!PushWork.revokePending(this, true)) {
                finishOperation("Не удалось отозвать прежнее подключение. Проверьте интернет и повторите подключение."); return;
            }
            JSONObject state = store.read(); JSONObject pending = state.optJSONObject("pair");
            if (pending != null && PushContract.instant(pending.getString("expiresAt")) > System.currentTimeMillis()) {
                finishOperation(""); return;
            }
            String credential = state.optString("credential");
            byte[] random = new byte[32]; new SecureRandom().nextBytes(random);
            String verifier = Base64.getUrlEncoder().withoutPadding().encodeToString(random);
            String challenge = Base64.getUrlEncoder().withoutPadding().encodeToString(
                    MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(StandardCharsets.US_ASCII)));
            String label = Build.MANUFACTURER + " " + Build.MODEL;
            if (!PushContract.bounded(label, 256) || label.codePointCount(0, label.length()) > 64) label = "Android";
            JSONObject request = new JSONObject().put("codeChallenge", challenge).put("deviceLabel", label).put("appVersion", BuildConfig.VERSION_NAME);
            JSONObject result = new PushApi().request("POST", "/api/v1/push/pairings", credential, request);
            JSONObject pairing = result.getJSONObject("pairing"); JSONObject installation = result.getJSONObject("installation");
            String id = pairing.getString("id"); String installationId = installation.getString("id");
            String nextCredential = credential.isEmpty() ? installation.getString("credential") : credential;
            if (!PushContract.uuid(id) || !PushContract.credential(nextCredential, installationId)
                    || (!credential.isEmpty() && installation.has("credential"))
                    || !pairing.getString("verificationCode").matches("[0-9]{6}")
                    || !PushContract.confirmation(pairing.getString("confirmationUrl"), id)) throw new IllegalArgumentException();
            long expires = PushContract.instant(pairing.getString("expiresAt"));
            if (expires <= System.currentTimeMillis() || expires > System.currentTimeMillis() + 330000) throw new IllegalArgumentException();
            JSONObject saved = new JSONObject().put("id", id).put("verifier", verifier)
                    .put("expiresAt", pairing.getString("expiresAt")).put("verificationCode", pairing.getString("verificationCode"))
                    .put("confirmationUrl", pairing.getString("confirmationUrl"));
            synchronized (PushStore.LOCK) {
                if (revocation != PushWork.localRevocation) throw new IllegalArgumentException();
                store.update(value -> value.put("credential", nextCredential).put("pair", saved));
            }
            // Show the comparison code before explicit browser handoff can bind an account.
            finishOperation("");
        } catch (PushApi.Failure failure) {
            if (failure.status == 401) {
                try { PushWork.disable(this); } catch (Exception ignored) { }
                finishOperation("Прежнее подключение отозвано. Нажмите «Подключить», чтобы создать новое.");
            } else finishOperation("Не удалось начать подключение. Повторите действие позже.");
        } catch (Exception ignored) {
            finishOperation("Результат подключения неизвестен. Подтверждение не открыто; можно повторить действие.");
        }
    }

    private void completePending(String id, boolean explicitRetry) {
        if (busy) return;
        try {
            JSONObject state = store.read(); JSONObject pending = state.optJSONObject("pair");
            if (pending == null && completed(state, id)) { problem = ""; render(); return; }
            if (pending == null || !id.equals(pending.optString("id"))
                    || (PushContract.instant(pending.getString("expiresAt")) <= System.currentTimeMillis()
                    && !pending.optBoolean("exchangeAttempted"))) {
                problem = "Запрос подключения истёк или не относится к этому устройству."; render(); return;
            }
        } catch (Exception ignored) { problem = "Не удалось прочитать защищённые данные подключения."; render(); return; }
        if (!PushNotifications.permitted(this)) { problem = "Для подключения разрешите уведомления в настройках Android."; render(); return; }
        busy = true; problem = ""; render();
        PushSdk.token(this, new PushSdk.TokenResult() {
            @Override public void ready(String token) {
                if (destroyed || isFinishing()) return;
                PushWork.IO.execute(() -> exchange(id, explicitRetry));
            }
            @Override public void unavailable() { finishOperation("RuStore Push недоступен. Подключение ещё не завершено."); }
        });
    }

    private void exchange(String id, boolean explicitRetry) {
        long revocation = PushWork.localRevocation;
        try {
            JSONObject state = store.read();
            if (!state.has("pair") && completed(state, id)) { finishOperation(""); return; }
            JSONObject pending = state.getJSONObject("pair");
            boolean expired = PushContract.instant(pending.getString("expiresAt")) <= System.currentTimeMillis();
            if (!id.equals(pending.getString("id")) || (expired && !pending.optBoolean("exchangeAttempted")))
                throw new IllegalArgumentException();
            String credential = state.getString("credential");
            String token = state.getString("token");
            if (!PushContract.token(token)) throw new IllegalArgumentException();
            JSONObject result;
            try {
                result = pending.optBoolean("exchangeAttempted")
                        ? new PushApi().request("GET", "/api/v1/push/installation", credential, null) : null;
                boolean reconciled = result != null && "ready".equals(result.optString("status")) && id.equals(result.optString("lastPairingId"));
                if (!reconciled) {
                    if (expired || (pending.optBoolean("exchangeAttempted") && !explicitRetry)) throw new IllegalArgumentException();
                    store.update(value -> {
                        JSONObject current = value.getJSONObject("pair");
                        if (!id.equals(current.getString("id")) || !credential.equals(value.optString("credential")))
                            throw new org.json.JSONException("Pairing superseded");
                        current.put("exchangeAttempted", true);
                    });
                    JSONObject body = new JSONObject().put("codeVerifier", pending.getString("verifier")).put("provider", "rustore")
                            .put("token", token).put("permission", "granted").put("appVersion", BuildConfig.VERSION_NAME);
                    result = new PushApi().request("POST", "/api/v1/push/pairings/" + id + "/exchange", credential, body);
                }
            } catch (java.io.IOException unknown) {
                // An exchange is single use. Reconcile an unknown/consumed result instead of blindly posting again.
                result = new PushApi().request("GET", "/api/v1/push/installation", credential, null);
            }
            JSONObject installation = PushContract.installation(result);
            if (!"ready".equals(installation.getString("status")) || !id.equals(installation.optString("lastPairingId")))
                throw new IllegalArgumentException();
            synchronized (PushStore.LOCK) {
                if (revocation != PushWork.localRevocation) throw new IllegalArgumentException();
                JSONObject currentState = store.read();
                JSONObject currentPair = currentState.optJSONObject("pair");
                if (!credential.equals(currentState.optString("credential")) || currentPair == null
                        || !id.equals(currentPair.optString("id"))) throw new IllegalArgumentException();
                PushWork.accept(this, installation, credential);
                store.update(value -> {
                    JSONObject current = value.optJSONObject("pair");
                    if (credential.equals(value.optString("credential")) && current != null && id.equals(current.optString("id"))) {
                        value.put("enabled", true); value.remove("pair");
                        // A reconciled response cannot prove which token reached the server.
                        value.remove("registeredToken"); value.remove("registeredPermission");
                    }
                });
                PushWork.locallyBlocked = false;
            }
            PushWork.registration(this);
            finishOperation("");
        } catch (Exception ignored) { finishOperation("Подключение не завершено. Подтвердите его в кабинете и повторите проверку до истечения запроса."); }
    }

    private boolean completed(JSONObject state, String id) {
        JSONObject installation = state.optJSONObject("installation");
        return PushStore.ready(state, System.currentTimeMillis()) && installation != null
                && id.equals(installation.optString("lastPairingId"));
    }

    private void openConfirmation(JSONObject pending) {
        runOnUiThread(() -> {
            if (destroyed || isFinishing()) return;
            try {
                String id = pending.getString("id"); String url = pending.getString("confirmationUrl");
                JSONObject currentPair = store.read().optJSONObject("pair");
                if (!PushContract.confirmation(url, id) || currentPair == null || !id.equals(currentPair.optString("id"))
                        || PushContract.instant(currentPair.getString("expiresAt")) <= System.currentTimeMillis()) throw new IllegalArgumentException();
                if (browser != null) {
                    TwaLauncher previous = browser; browser = null;
                    try { previous.destroy(); } catch (IllegalArgumentException ignored) { }
                }
                browserOpening = true;
                TwaLauncher current = new TwaLauncher(this); browser = current;
                Runnable completed = () -> { if (browser == current && !destroyed && browserOpening && !isFinishing()) finish(); };
                current.launch(new TrustedWebActivityIntentBuilder(Uri.parse(url)).setToolbarColor(Color.rgb(0, 127, 115)),
                        new CustomTabsCallback(), null, completed, (context, builder, provider, complete) -> {
                            if (browser != current || destroyed || !browserOpening || isFinishing()) return;
                            try {
                                if (provider == null || getPackageManager().resolveService(
                                        new Intent(CustomTabsService.ACTION_CUSTOM_TABS_CONNECTION).setPackage(provider), 0) == null) {
                                    finishOperation("Установите браузер с поддержкой Custom Tabs для подтверждения в кабинете."); return;
                                }
                                TwaLauncher.CCT_FALLBACK_STRATEGY.launch(context, builder, provider, complete);
                            } catch (ActivityNotFoundException | SecurityException error) {
                                finishOperation("Не удалось открыть подтверждение в браузере. Повторите действие.");
                            }
                        });
            } catch (Exception ignored) { finishOperation("Не удалось открыть подтверждение. Повторите действие."); }
        });
    }

    private void refreshStatus() {
        if (busy) return; busy = true; render();
        PushWork.IO.execute(() -> {
            try {
                String credential = store.read().optString("credential");
                if (!credential.isEmpty()) PushWork.accept(this, new PushApi().request("GET", "/api/v1/push/installation", credential, null), credential);
                finishOperation("");
            } catch (Exception ignored) { finishOperation("Не удалось обновить статус. Повторите действие позже."); }
        });
    }

    private void finishOperation(String message) {
        runOnUiThread(() -> {
            if (destroyed || isFinishing()) return;
            busy = false; browserOpening = false; problem = message; render();
        });
    }

    private void render() {
        if (destroyed || isFinishing()) return;
        ScrollView scroll = new ScrollView(this); LinearLayout page = new LinearLayout(this); content = page;
        scroll.setFillViewport(true); scroll.setBackgroundColor(getColor(R.color.native_background));
        page.setOrientation(LinearLayout.VERTICAL); page.setPadding(dp(20), dp(24), dp(20), dp(24)); scroll.addView(page);
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            if (Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
                view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            } else view.setPadding(insets.getSystemWindowInsetLeft(), insets.getSystemWindowInsetTop(),
                    insets.getSystemWindowInsetRight(), insets.getSystemWindowInsetBottom());
            return insets;
        });
        LinearLayout brand = new LinearLayout(this); brand.setGravity(Gravity.CENTER_VERTICAL);
        ImageView logo = new ImageView(this); logo.setImageResource(R.drawable.ic_heartbeat);
        logo.setImportantForAccessibility(android.view.View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        LinearLayout.LayoutParams logoParams = new LinearLayout.LayoutParams(dp(36), dp(36)); logoParams.setMargins(0, 0, dp(12), 0);
        brand.addView(logo, logoParams);
        TextView name = new TextView(this); name.setText(R.string.app_name); name.setTextSize(18);
        name.setTextColor(getColor(R.color.native_ink)); name.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        brand.addView(name, new LinearLayout.LayoutParams(0, -2, 1)); page.addView(brand);
        TextView title = text("Push-уведомления", 26); title.setTypeface(Typeface.DEFAULT, Typeface.BOLD); title.setAccessibilityHeading(true);
        text("Подключите это устройство к своему аккаунту и выберите уведомления для мониторов в кабинете.", 16);
        LinearLayout card = new LinearLayout(this); card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(20), dp(16), dp(20), dp(16)); card.setBackground(surface(getColor(R.color.native_surface), getColor(R.color.native_border), 1));
        card.setElevation(dp(2)); LinearLayout.LayoutParams cardParams = new LinearLayout.LayoutParams(-1, -2);
        cardParams.setMargins(0, dp(12), 0, dp(16)); page.addView(card, cardParams); content = card;
        if (!problem.isEmpty()) { TextView error = text(problem, 16); error.setTextColor(getColor(R.color.native_danger)); }
        if (busy) {
            LinearLayout waiting = new LinearLayout(this); waiting.setGravity(Gravity.CENTER_VERTICAL);
            ProgressBar progress = new ProgressBar(this); progress.setIndeterminateTintList(ColorStateList.valueOf(getColor(R.color.native_accent_text)));
            LinearLayout.LayoutParams progressParams = new LinearLayout.LayoutParams(dp(24), dp(24)); progressParams.setMargins(0, 0, dp(12), 0);
            waiting.addView(progress, progressParams); TextView label = new TextView(this); label.setText("Подождите…"); label.setTextSize(16);
            label.setTextColor(getColor(R.color.native_muted)); waiting.addView(label); card.addView(waiting);
        }
        try {
            JSONObject state = store.read(); JSONObject installation = state.optJSONObject("installation");
            if (store.nearLimit()) text("Хранилище уведомлений заполнено. Отключите и заново подключите устройство.", 16);
            String status = installation == null ? "pending" : installation.optString("status");
            text(state.optBoolean("enabled")
                    ? (PushNotifications.permitted(this) ? statusLabel(status) : "Уведомления запрещены в Android")
                    : "Устройство не подключено", 18);
            JSONObject pending = state.optJSONObject("pair");
            boolean expired = pending != null && PushContract.instant(pending.getString("expiresAt")) <= System.currentTimeMillis();
            if (pending != null && (!expired || pending.optBoolean("exchangeAttempted"))) {
                if (!expired) {
                    text("Код для сверки в кабинете: " + pending.getString("verificationCode"), 20);
                    primaryButton("Продолжить подтверждение", () -> { if (!busy) { busy = true; render(); openConfirmation(pending); } });
                } else {
                    text("Срок подтверждения истёк. Проверьте результат предыдущей попытки или начните подключение заново.", 16);
                    primaryButton("Начать заново", () -> {
                        if (busy) return;
                        try { store.update(value -> value.remove("pair")); connect(); }
                        catch (Exception ignored) { finishOperation("Не удалось сохранить новый запрос. Данные не изменены."); }
                    });
                }
                button("Проверить подключение", () -> completePending(pending.optString("id"), true));
            } else if (state.optBoolean("enabled") && "ready".equals(status) && PushNotifications.permitted(this)) {
                primaryButton("Готово", this::finish); button("Переподключить", this::connect);
            } else primaryButton(state.optBoolean("enabled") ? "Переподключить" : "Подключить", this::connect);
            if (state.has("credential")) {
                button("Обновить статус", this::refreshStatus);
                button("Отключить", () -> {
                    if (busy) return; busy = true; render();
                    // Stop display before queued storage/network work can run.
                    try { PushWork.blockLocally(this); } catch (RuntimeException ignored) { }
                    PushWork.IO.execute(() -> {
                        try { PushWork.disable(this); finishOperation(""); }
                        catch (Exception ignored) { finishOperation("Не удалось сохранить отключение. Запретите уведомления в настройках Android и повторите действие."); }
                    });
                });
            }
        } catch (Exception ignored) { text("Защищённое хранилище недоступно. Данные не изменены.", 16); }
        content = page;
        button("Настройки уведомлений", () -> {
            try { startActivity(new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName())); }
            catch (ActivityNotFoundException | SecurityException ignored) { finishOperation("Откройте разрешения приложения в настройках Android."); }
        });
        button("Закрыть", this::finish);
        setContentView(scroll); scroll.requestApplyInsets();
    }

    private static String statusLabel(String status) {
        switch (status) {
            case "ready": return "Устройство подключено";
            case "permission_denied": return "Уведомления запрещены в Android";
            case "session_expired": case "session_revoked": return "Переподключить устройство";
            case "invalid_token": return "Подключите уведомления заново";
            case "provider_unavailable": return "RuStore Push недоступен";
            default: return "Подключение не завершено";
        }
    }

    private TextView text(String value, int size) {
        TextView view = new TextView(this); view.setText(value); view.setTextSize(size); view.setLineSpacing(dp(3), 1);
        view.setTextColor(getColor(size >= 18 ? R.color.native_ink : R.color.native_muted));
        view.setPadding(0, dp(10), 0, dp(10)); content.addView(view); return view;
    }
    private void button(String label, Runnable action) {
        styledButton(label, action, false);
    }
    private void primaryButton(String label, Runnable action) { styledButton(label, action, true); }
    private GradientDrawable surface(int fill, int border, int stroke) {
        GradientDrawable drawable = new GradientDrawable(); drawable.setColor(fill); drawable.setCornerRadius(dp(12));
        drawable.setStroke(dp(stroke), border); return drawable;
    }
    private void styledButton(String label, Runnable action, boolean primary) {
        Button button = new Button(this); button.setText(label); button.setTextSize(16); button.setAllCaps(false);
        button.setSingleLine(false); button.setMinHeight(dp(48)); button.setEnabled(!busy || "Закрыть".equals(label));
        button.setPadding(dp(16), dp(12), dp(16), dp(12));
        int fill = getColor(primary ? R.color.native_accent : R.color.native_surface);
        int border = getColor(primary ? R.color.native_accent : R.color.native_border);
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{-android.R.attr.state_enabled}, surface(getColor(R.color.native_background), getColor(R.color.native_border), 1));
        background.addState(new int[]{android.R.attr.state_focused}, surface(fill, getColor(R.color.native_accent_text), 3));
        background.addState(new int[]{}, surface(fill, border, 1));
        button.setBackground(new RippleDrawable(ColorStateList.valueOf(getColor(R.color.native_ripple)), background, surface(Color.WHITE, Color.WHITE, 0)));
        button.setBackgroundTintList(null);
        int ink = primary ? Color.WHITE : getColor("Отключить".equals(label) ? R.color.native_danger : R.color.native_ink);
        button.setTextColor(new ColorStateList(new int[][]{{-android.R.attr.state_enabled}, {}}, new int[]{getColor(R.color.native_muted), ink}));
        button.setOnClickListener(view -> action.run()); LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2);
        params.setMargins(0, dp(6), 0, dp(6)); content.addView(button, params);
    }
    private int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }

    @Override protected void onDestroy() {
        destroyed = true;
        if (browser != null) try { browser.destroy(); } catch (IllegalArgumentException ignored) { }
        super.onDestroy();
    }
}
