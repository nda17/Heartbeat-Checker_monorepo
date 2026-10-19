package ru.heartbeatchecker.app;

import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.text.InputFilter;
import android.text.InputType;
import android.view.View;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.browser.customtabs.CustomTabsCallback;
import androidx.browser.trusted.TrustedWebActivityIntentBuilder;
import androidx.activity.ComponentActivity;
import androidx.activity.OnBackPressedCallback;

import com.google.androidbrowserhelper.trusted.TwaLauncher;

import org.json.JSONException;

import java.io.IOException;
import java.util.List;

public final class MainActivity extends ComponentActivity {
    private DraftStore store;
    private TwaLauncher launcher;
    private LinearLayout content;
    private EditText nameInput;
    private String previewUrl;
    private Draft savedDraft;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        store = new DraftStore(getNoBackupFilesDir());
        launcher = new TwaLauncher(this);
        if (state != null && state.getString("previewUrl") != null) {
            previewUrl = state.getString("previewUrl");
            String savedId = state.getString("savedId");
            if (savedId != null) {
                try {
                    for (Draft draft : store.load()) if (draft.id.equals(savedId)) savedDraft = draft;
                } catch (IOException ignored) {
                    // The list reports storage errors; do not overwrite a damaged file.
                }
            }
            showPreview(previewUrl, state.getString("previewName", ""));
        } else {
            showDrafts(null);
            if (state == null) handleIntent(getIntent());
        }
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                goBack();
            }
        });
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    private void handleIntent(Intent intent) {
        previewUrl = null;
        savedDraft = null;
        if (Intent.ACTION_SEND.equals(intent.getAction())) {
            try {
                CharSequence shared = intent.getCharSequenceExtra(Intent.EXTRA_TEXT);
                String url = shared == null || shared.length() > 8192 ? "" : shared.toString();
                if (!"text/plain".equals(intent.getType()) || intent.hasExtra(Intent.EXTRA_STREAM)
                        || !Draft.validUrl(url)) {
                    showDrafts("Поделитесь одним полным адресом http:// или https:// без текста, логина и фрагмента #.");
                    return;
                }
                showPreview(url, "");
            } catch (RuntimeException e) {
                showDrafts("Не удалось прочитать переданный адрес.");
            }
            return;
        }
        if (Intent.ACTION_VIEW.equals(intent.getAction())) {
            String url = LinkPolicy.appLink(intent.getDataString());
            showDrafts(url == null ? "Эта ссылка не открывается внутри приложения." : null);
            if (url != null) openBrowser(url);
            return;
        }
        showDrafts(null);
        if (Intent.ACTION_MAIN.equals(intent.getAction())) openBrowser(LinkPolicy.HOME);
        // A launcher shortcut or an unknown explicit intent only opens the local list.
    }

    private void showDrafts(String message) {
        previewUrl = null;
        savedDraft = null;
        screen("Heartbeat Checker");
        text("Кабинет мониторинга работает через установленный браузер. Для него нужен интернет.", 16);
        button("Открыть кабинет", () -> openBrowser(LinkPolicy.HOME));
        text("Черновики адресов", 24);
        text("Из браузера выберите «Поделиться» → Heartbeat Checker. Адрес можно сохранить без интернета. Сохранение не создаёт монитор.", 16);
        if (message != null) text(message, 16).setTextColor(Color.rgb(152, 40, 40));
        try {
            List<Draft> drafts = store.load();
            text("Сохранено: " + drafts.size() + " из " + DraftStore.LIMIT, 16);
            if (drafts.isEmpty()) text("Черновиков пока нет.", 16);
            for (Draft draft : drafts) {
                text(draft.name.isEmpty() ? "Адрес для мониторинга" : draft.name, 20);
                text(draft.url, 16).setTextIsSelectable(true);
                button("Открыть черновик", () -> {
                    savedDraft = draft;
                    showPreview(draft.url, draft.name);
                });
                button("Удалить этот черновик", () -> confirmDelete(draft));
            }
        } catch (IOException e) {
            text("Не удалось прочитать черновики. Сохранённые данные не изменены. При необходимости очистите хранилище.", 16);
        }
        button("Очистить все черновики", () -> new AlertDialog.Builder(this)
                .setTitle("Удалить все черновики с устройства?")
                .setMessage("Мониторы в кабинете и данные браузера не изменятся.")
                .setNegativeButton("Отмена", null)
                .setPositiveButton("Удалить", (dialog, which) -> {
                    try {
                        store.clear();
                        showDrafts("Черновики удалены.");
                    } catch (IOException e) {
                        showError("Не удалось очистить черновики. Повторите попытку.");
                    }
                }).show());
        text("Черновики хранятся только в приложении на этом устройстве, без резервного копирования. Они не удаляются после открытия сайта: удалите их сами после создания монитора. Удаление приложения удаляет черновики. Вход и cookies хранит браузер.", 14);
    }

    private void showPreview(String url, String name) {
        previewUrl = url;
        screen(savedDraft == null ? "Сохранить адрес" : "Черновик адреса");
        text(url, 18).setTextIsSelectable(true);
        text("Приложение не запрашивает этот адрес и не проверяет его доступность.", 16);
        if (savedDraft == null) {
            TextView nameLabel = text("Название монитора (необязательно, до 100 символов)", 16);
            nameInput = new EditText(this);
            nameInput.setId(View.generateViewId());
            nameLabel.setLabelFor(nameInput.getId());
            nameInput.setSingleLine(true);
            nameInput.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);
            nameInput.setFilters(new InputFilter[]{new InputFilter.LengthFilter(200)});
            nameInput.setText(name);
            nameInput.setHint("Например, сайт компании");
            content.addView(nameInput);
            button("Сохранить на устройстве", () -> {
                try {
                    Draft draft = Draft.create(url, nameInput.getText().toString());
                    store.add(draft);
                    savedDraft = draft;
                    showPreview(url, draft.name);
                } catch (IllegalArgumentException e) {
                    showError("Проверьте адрес и название: не более 100 символов, без управляющих символов.");
                } catch (IOException e) {
                    showError("Не удалось сохранить черновик. Лимит — 20 адресов; удалите ненужные и повторите попытку.");
                }
            });
        } else {
            if (!name.isEmpty()) text(name, 20);
            text("Сохранено на устройстве. Для продолжения откройте кабинет. На сайте подтвердите перенос черновика, проверьте поля и самостоятельно сохраните монитор.", 16);
            button("Продолжить в кабинете", () -> {
                try {
                    openBrowser(savedDraft.launchUrl());
                } catch (JSONException | IllegalArgumentException e) {
                    showError("Не удалось передать черновик. Сохранённый адрес остаётся на устройстве.");
                }
            });
            button("Удалить этот черновик", () -> confirmDelete(savedDraft));
        }
        button("К списку черновиков", () -> showDrafts(null));
    }

    private void confirmDelete(Draft draft) {
        new AlertDialog.Builder(this).setTitle("Удалить черновик?")
                .setMessage("Это удалит только сохранённый адрес на устройстве.")
                .setNegativeButton("Отмена", null)
                .setPositiveButton("Удалить", (dialog, which) -> {
                    try {
                        store.delete(draft.id);
                        showDrafts(null);
                    } catch (IOException e) {
                        showError("Не удалось удалить черновик. Повторите попытку.");
                    }
                }).show();
    }

    private void openBrowser(String url) {
        try {
            launcher.launch(new TrustedWebActivityIntentBuilder(Uri.parse(url))
                            .setToolbarColor(Color.rgb(0, 127, 115)),
                    new CustomTabsCallback(), null, null,
                    (context, builder, provider, complete) -> {
                        if (provider == null) {
                            showError("Установите или включите браузер с поддержкой Custom Tabs. Черновики доступны без браузера.");
                            return;
                        }
                        TwaLauncher.CCT_FALLBACK_STRATEGY.launch(context, builder, provider, complete);
                    });
        } catch (ActivityNotFoundException | SecurityException e) {
            showError("Установите или включите браузер с поддержкой Custom Tabs. Черновики доступны без браузера.");
        }
    }

    private void screen(String title) {
        nameInput = null;
        ScrollView scroll = new ScrollView(this);
        content = new LinearLayout(this);
        content.setOrientation(LinearLayout.VERTICAL);
        content.setPadding(dp(24), dp(24), dp(24), dp(24));
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
        setContentView(scroll);
        scroll.requestApplyInsets();
        text(title, 28).setAccessibilityHeading(true);
    }

    private TextView text(String value, int size) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(size);
        view.setTextColor(Color.rgb(25, 48, 44));
        view.setPadding(0, dp(10), 0, dp(10));
        content.addView(view);
        return view;
    }

    private void button(String label, Runnable action) {
        Button button = new Button(this);
        button.setText(label);
        button.setAllCaps(false);
        button.setMinHeight(dp(48));
        button.setOnClickListener(view -> action.run());
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        params.setMargins(0, dp(6), 0, dp(6));
        content.addView(button, params);
    }

    private void showError(String message) {
        new AlertDialog.Builder(this).setTitle("Не удалось выполнить действие")
                .setMessage(message).setPositiveButton("Понятно", null).show();
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private void goBack() {
        if (previewUrl != null) showDrafts(null);
        else finish();
    }

    @Override
    protected void onSaveInstanceState(Bundle state) {
        super.onSaveInstanceState(state);
        if (previewUrl != null) {
            state.putString("previewUrl", previewUrl);
            state.putString("previewName", savedDraft != null ? savedDraft.name
                    : nameInput == null ? "" : nameInput.getText().toString());
            if (savedDraft != null) state.putString("savedId", savedDraft.id);
        }
    }

    @Override
    protected void onDestroy() {
        if (launcher != null) launcher.destroy();
        super.onDestroy();
    }
}
