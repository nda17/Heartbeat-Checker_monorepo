package ru.heartbeatchecker.app;

import android.Manifest;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.graphics.drawable.StateListDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.view.Gravity;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.activity.ComponentActivity;
import androidx.activity.OnBackPressedCallback;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.browser.customtabs.CustomTabsCallback;
import androidx.browser.customtabs.CustomTabsService;
import androidx.browser.trusted.TrustedWebActivityIntentBuilder;
import androidx.work.WorkInfo;
import androidx.work.WorkManager;

import com.google.androidbrowserhelper.trusted.TwaLauncher;

import org.json.JSONObject;

/** Narrow browser return handler; login stays in the browser and device proof stays encrypted. */
public final class PushSetupActivity extends ComponentActivity {
    private final Handler handler = new Handler(Looper.getMainLooper());
    private PushStore store;
    private TwaLauncher browser;
    private volatile boolean destroyed;
    private boolean busy;
    private volatile boolean opening;
    private boolean awaitingPermission;
    private volatile int operation;
    private String pairingId;
    private String returnUrl = LinkPolicy.HOME;
    private String problem = "";
    private final ActivityResultLauncher<String> permission = registerForActivityResult(
            new ActivityResultContracts.RequestPermission(), granted -> {
                awaitingPermission = false;
                if (granted) tokenAndExchange();
                else { recordStatus("permission_denied"); openCabinet(); }
            });

    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        store = new PushStore(this);
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override public void handleOnBackPressed() { finish(); }
        });
        waiting();
        if (state != null && state.getBoolean("awaitingPermission")) {
            pairingId = state.getString("pairingId");
            returnUrl = state.getString("returnUrl", LinkPolicy.HOME);
            if (!PushContract.uuid(pairingId) || LinkPolicy.appLink(returnUrl) == null) { finish(); return; }
            awaitingPermission = true; busy = true;
            return;
        }
        handle(getIntent());
    }

    @Override protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        if (busy || opening) return;
        setIntent(intent);
        handle(intent);
    }

    @Override protected void onResume() {
        super.onResume();
        try { PushWork.permissionsChanged(getApplicationContext()); } catch (RuntimeException ignored) { }
    }

    private void handle(Intent intent) {
        if (intent == null || !Intent.ACTION_VIEW.equals(intent.getAction())) { finish(); return; }
        String action = PushContract.setupPair(intent.getDataString());
        if (action == null) { finish(); return; }
        if (action.isEmpty()) { controls(); return; }
        try {
            Bundle extras = intent.getExtras();
            if (extras == null) throw new IllegalArgumentException();
            Object id = extras.get("pairingId"); Object code = extras.get("completionCode"); Object state = extras.get("clientState");
            if (!(id instanceof String) || !(code instanceof String) || !(state instanceof String)
                    || !PushContract.uuid((String) id) || !PushContract.secret((String) code) || !PushContract.secret((String) state))
                throw new IllegalArgumentException();
            pairingId = (String) id; busy = true; int request = ++operation;
            handler.postDelayed(() -> { if (current(request) && !awaitingPermission) openCabinet(); }, 8000);
            PushWork.IO.execute(() -> {
                try {
                    String target = PushPairing.receive(this, (String) id, (String) code, (String) state);
                    if (PushNotifications.permitted(getApplicationContext()))
                        PushWork.pairing(getApplicationContext(), (String) id);
                    handler.post(() -> {
                        if (!current(request)) return;
                        returnUrl = target;
                        requestPermission();
                    });
                } catch (Exception ignored) { handler.post(() -> { if (current(request)) openCabinet(); }); }
            });
        } catch (RuntimeException invalid) { finish(); }
    }

    private void requestPermission() {
        try {
            JSONObject state = store.read();
            if (state.optBoolean("optedOut")) { openCabinet(); return; }
            if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                if (state.optBoolean("permissionPrompted")) { recordStatus("permission_denied"); openCabinet(); return; }
                store.update(value -> value.put("permissionPrompted", true));
                awaitingPermission = true;
                permission.launch(Manifest.permission.POST_NOTIFICATIONS);
            } else tokenAndExchange();
        } catch (Exception ignored) { openCabinet(); }
    }

    private void tokenAndExchange() {
        if (destroyed || isFinishing() || opening) return;
        if (!PushNotifications.permitted(this)) { recordStatus("permission_denied"); openCabinet(); return; }
        int request = ++operation;
        // The deadline controls navigation only. Persisted proof belongs to WorkManager,
        // so Activity destruction or a slow SDK callback cannot cancel the binding.
        handler.postDelayed(() -> { if (current(request)) openCabinet(); }, 8000);
        try {
            String name = PushWork.pairing(getApplicationContext(), pairingId);
            WorkManager.getInstance(getApplicationContext()).getWorkInfosForUniqueWorkLiveData(name).observe(this, jobs -> {
                if (!current(request) || jobs == null) return;
                for (WorkInfo job : jobs) if (job.getState().isFinished()) { openCabinet(); break; }
            });
        } catch (RuntimeException ignored) { openCabinet(); }
    }

    private boolean current(int request) { return request == operation && !destroyed && !isFinishing() && !opening; }

    private void controls() {
        busy = false;
        LinearLayout page = page();
        text(page, "Уведомления на этом устройстве");
        if (!problem.isEmpty()) text(page, problem);
        try {
            JSONObject state = store.read();
            String status = state.optString("lastConnectionStatus");
            if (state.optBoolean("optedOut")) text(page, "Уведомления отключены. Включите их, чтобы подключить устройство заново.");
            else if ("permission_denied".equals(status)) text(page, "Уведомления запрещены в Android. Разрешите их в настройках приложения.");
            else if ("provider_unavailable".equals(status)) text(page, "Push недоступен. Проверьте сеть и сервисы уведомлений на устройстве, затем повторите подключение.");
            else if ("connecting".equals(status)) text(page, "Подключаем устройство в фоне. Можно продолжать работу в кабинете.");
            else if ("connection_failed".equals(status)) text(page, "Подключение не завершено. Проверьте интернет и повторите подключение.");
            else if (PushStore.ready(state, System.currentTimeMillis())) text(page, "Устройство подключено");
            else text(page, "Устройство не подключено");
            diagnostics(page, state);
        } catch (Exception ignored) { text(page, "Защищённые данные подключения недоступны."); }
        button(page, "Включить уведомления", () -> {
            if (busy) return;
            busy = true; waiting(); int request = ++operation;
            handler.postDelayed(() -> { if (current(request)) openCabinet(); }, 8000);
            PushWork.IO.execute(() -> {
                try {
                    // Only this local action may recover a remotely revoked bound identity.
                    String next = PushPairing.enable(this, LinkPolicy.HOME);
                    handler.post(() -> { if (current(request)) openBrowser(next); });
                } catch (Exception ignored) { handler.post(() -> { if (current(request)) openCabinet(); }); }
            });
        });
        button(page, "Отключить уведомления", () -> {
            if (busy) return;
            busy = true; PushWork.blockLocally(this); waiting();
            PushWork.IO.execute(() -> {
                try { PushWork.disable(this); handler.post(this::openCabinet); }
                catch (Exception ignored) {
                    handler.post(() -> {
                        if (destroyed || isFinishing()) return;
                        problem = "Не удалось сохранить отключение. Запретите уведомления в настройках Android и повторите действие.";
                        controls();
                    });
                }
            });
        });
        button(page, "Открыть кабинет", this::openCabinet);
        button(page, "Настройки уведомлений", () -> {
            try { startActivity(new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName())); }
            catch (ActivityNotFoundException | SecurityException ignored) { }
        });
        setPage(page);
    }

    private void openCabinet() { openBrowser(returnUrl); }

    private void diagnostics(LinearLayout page, JSONObject state) {
        JSONObject provider = state.optJSONObject("providerDiagnostic");
        String providerMessage = providerDiagnosticMessage(state);
        if (provider != null && !providerMessage.isEmpty())
            text(page, "Последняя ошибка push (" + diagnosticTime(provider.optLong("at")) + "): " + providerMessage);
        JSONObject delivery = state.optJSONObject("deliveryDiagnostic");
        if (delivery == null) {
            text(page, "Получение сигнала приложением пока не зафиксировано. Разрешение Android само по себе не подтверждает доставку.");
            return;
        }
        String message;
        switch (delivery.optString("code")) {
            case "RECEIVED": message = "Сигнал получен приложением."; break;
            case "QUEUED": message = "Сигнал ожидает обработки при доступной сети."; break;
            case "FETCH_STARTED": message = "Приложение начало запрашивать событие с сервера."; break;
            case "FETCHED": message = "Событие получено с сервера."; break;
            case "DISPLAY_SUBMITTED": message = "Уведомление передано Android. Это не подтверждает появление баннера или видимость в шторке."; break;
            case "PERMISSION_DENIED": message = "Показ запрещён в настройках приложения или канала «Инциденты»."; break;
            case "LOCALLY_BLOCKED": message = "Уведомления отключены на этом устройстве."; break;
            case "EXPIRED": message = "Сигнал пришёл после истечения срока действия и не показан."; break;
            case "BINDING_MISMATCH": message = "Сигнал не относится к текущему подключению устройства."; break;
            case "INVALID_ENVELOPE": message = invalidEnvelopeMessage(delivery.optString("reason")); break;
            case "FETCH_UNAUTHORIZED": message = "Доступ к событию прекращён. Подключите устройство заново."; break;
            case "FETCH_NOT_FOUND": message = "Событие больше недоступно на сервере."; break;
            case "FETCH_TRANSIENT": message = "Не удалось получить событие. Проверьте сеть."; break;
            case "DISPLAY_FAILED": message = "Android не принял уведомление. Проверьте настройки приложения и канала «Инциденты»."; break;
            default: message = "Обработка сигнала не завершена. Проверьте сеть и настройки уведомлений.";
        }
        text(page, "Последняя обработка (" + diagnosticTime(delivery.optLong("at")) + "): " + message);
        if (state.optLong("lastReceivedAt") > 0)
            text(page, "Последний push-сигнал: " + diagnosticTime(state.optLong("lastReceivedAt")));
    }

    static String providerDiagnosticMessage(JSONObject state) {
        JSONObject diagnostic = state.optJSONObject("providerDiagnostic");
        if (diagnostic == null) return "";
        String code = diagnostic.optString("code");
        // An old RuStore diagnostic, or a late callback from its SDK, must not
        // describe a currently selected Firebase/Huawei route.
        if (code.startsWith("DISTRIBUTOR_") && !"rustore".equals(PushSdk.provider(state))) return "";
        switch (code) {
            case "DISTRIBUTOR_MISSING": return "Установите или включите RuStore, затем повторите подключение.";
            case "DISTRIBUTOR_AUTH_REQUIRED": return "Откройте RuStore и войдите в аккаунт, затем повторите подключение.";
            case "DISTRIBUTOR_BACKGROUND_RESTRICTED": return "Разрешите RuStore работу в фоне в настройках Android, затем повторите подключение.";
            default: return "Проверьте сеть и доступность сервисов уведомлений на устройстве, затем повторите подключение.";
        }
    }

    private String invalidEnvelopeMessage(String reason) {
        switch (reason) {
            case "NULL_MESSAGE": return "Push-сервис передал пустое сообщение; сигнал отклонён.";
            case "NOTIFICATION_CONTENT": return "Сигнал содержит непустые поля уведомления; он отклонён.";
            case "DATA_KEYS": return "Набор полей данных сигнала не совпадает с ожидаемым; он отклонён.";
            case "VERSION": return "Версия сигнала не поддерживается; он отклонён.";
            case "DELIVERY_ID": return "Идентификатор доставки в сигнале некорректен; он отклонён.";
            case "INSTALLATION_ID": return "Идентификатор установки в сигнале некорректен; он отклонён.";
            case "GENERATION": return "Поколение привязки в сигнале некорректно; он отклонён.";
            case "EXPIRY_FORMAT": return "Срок действия сигнала записан некорректно; он отклонён.";
            case "SDK_MESSAGE": return "Не удалось прочитать push-сообщение; сигнал отклонён.";
            default: return "Получен некорректный сигнал; он отклонён.";
        }
    }

    private String diagnosticTime(long at) {
        return java.text.DateFormat.getDateTimeInstance(java.text.DateFormat.SHORT, java.text.DateFormat.SHORT)
                .format(new java.util.Date(at));
    }

    private void recordStatus(String status) {
        try { store.update(value -> value.put("lastConnectionStatus", status)); } catch (Exception ignored) { }
    }

    private void openBrowser(String url) {
        if (destroyed || isFinishing() || opening) return;
        if (LinkPolicy.appLink(url) == null) {
            try {
                JSONObject pending = store.read().getJSONObject("pair");
                if (!PushContract.confirmation(url, pending.getString("id"))) url = LinkPolicy.HOME;
            } catch (Exception ignored) { url = LinkPolicy.HOME; }
        }
        opening = true; busy = false; awaitingPermission = false; ++operation; waiting();
        if (browser != null) try { browser.destroy(); } catch (IllegalArgumentException ignored) { }
        try {
            TwaLauncher current = new TwaLauncher(this); browser = current;
            current.launch(new TrustedWebActivityIntentBuilder(Uri.parse(url)).setToolbarColor(Color.rgb(0, 127, 115)),
                    new CustomTabsCallback(), null, () -> { if (!destroyed && browser == current) finish(); },
                    (context, builder, provider, complete) -> {
                        if (destroyed || isFinishing() || browser != current) return;
                        try {
                            if (provider == null || getPackageManager().resolveService(
                                    new Intent(CustomTabsService.ACTION_CUSTOM_TABS_CONNECTION).setPackage(provider), 0) == null) {
                                browserUnavailable(); return;
                            }
                            TwaLauncher.CCT_FALLBACK_STRATEGY.launch(context, builder, provider, complete);
                        } catch (ActivityNotFoundException | SecurityException ignored) { browserUnavailable(); }
                    });
        } catch (ActivityNotFoundException | SecurityException ignored) { browserUnavailable(); }
    }

    private void browserUnavailable() {
        opening = false;
        LinearLayout page = page(); text(page, "Установите или включите браузер с поддержкой Custom Tabs.");
        button(page, "Повторить", this::openCabinet); button(page, "Закрыть", this::finish); setPage(page);
    }

    private LinearLayout page() {
        LinearLayout page = new LinearLayout(this); page.setOrientation(LinearLayout.VERTICAL); page.setGravity(Gravity.CENTER);
        int pad = Math.round(24 * getResources().getDisplayMetrics().density); page.setPadding(pad, pad, pad, pad);
        return page;
    }
    private void waiting() {
        LinearLayout page = page(); text(page, "Открываем кабинет…"); page.addView(new ProgressBar(this)); setPage(page);
    }
    private void setPage(LinearLayout page) {
        ScrollView scroll = new ScrollView(this); scroll.setFillViewport(true);
        scroll.setBackgroundColor(getColor(R.color.native_background)); scroll.addView(page);
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            if (Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
                view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            } else view.setPadding(insets.getSystemWindowInsetLeft(), insets.getSystemWindowInsetTop(),
                    insets.getSystemWindowInsetRight(), insets.getSystemWindowInsetBottom());
            return insets;
        });
        setContentView(scroll); scroll.requestApplyInsets();
    }
    private void text(LinearLayout page, String value) {
        TextView text = new TextView(this); text.setText(value); text.setTextSize(18); text.setGravity(Gravity.CENTER);
        text.setTextColor(getColor(R.color.native_ink)); text.setPadding(0, dp(12), 0, dp(12)); page.addView(text);
    }
    private void button(LinearLayout page, String label, Runnable action) {
        Button button = new Button(this); button.setText(label); button.setTextSize(16); button.setAllCaps(false);
        button.setSingleLine(false); button.setMinHeight(dp(48)); button.setPadding(dp(16), dp(12), dp(16), dp(12));
        boolean primary = "Включить уведомления".equals(label);
        int fill = getColor(primary ? R.color.native_accent : R.color.native_surface);
        int border = getColor(primary ? R.color.native_accent : R.color.native_border);
        StateListDrawable background = new StateListDrawable();
        background.addState(new int[]{-android.R.attr.state_enabled}, surface(getColor(R.color.native_background), getColor(R.color.native_border), 1));
        background.addState(new int[]{android.R.attr.state_focused}, surface(fill, getColor(R.color.native_accent_text), 3));
        background.addState(new int[]{}, surface(fill, border, 1));
        button.setBackground(new RippleDrawable(ColorStateList.valueOf(getColor(R.color.native_ripple)), background, surface(Color.WHITE, Color.WHITE, 0)));
        button.setBackgroundTintList(null);
        int ink = primary ? Color.WHITE : getColor("Отключить уведомления".equals(label) ? R.color.native_danger : R.color.native_ink);
        button.setTextColor(new ColorStateList(new int[][]{{-android.R.attr.state_enabled}, {}}, new int[]{getColor(R.color.native_muted), ink}));
        button.setOnClickListener(view -> action.run()); LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2);
        params.setMargins(0, dp(6), 0, dp(6)); page.addView(button, params);
    }
    private GradientDrawable surface(int fill, int border, int stroke) {
        GradientDrawable drawable = new GradientDrawable(); drawable.setColor(fill); drawable.setCornerRadius(dp(12));
        drawable.setStroke(dp(stroke), border); return drawable;
    }
    private int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }

    @Override protected void onSaveInstanceState(Bundle state) {
        super.onSaveInstanceState(state);
        state.putBoolean("awaitingPermission", awaitingPermission);
        state.putString("pairingId", pairingId); state.putString("returnUrl", returnUrl);
    }
    @Override protected void onDestroy() {
        destroyed = true; ++operation;
        handler.removeCallbacksAndMessages(null);
        if (browser != null) try { browser.destroy(); } catch (IllegalArgumentException ignored) { }
        super.onDestroy();
    }
}
