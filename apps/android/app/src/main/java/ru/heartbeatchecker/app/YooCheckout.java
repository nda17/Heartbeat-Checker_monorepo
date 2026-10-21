package ru.heartbeatchecker.app;

import android.app.Activity;
import android.content.Intent;

import com.facebook.react.bridge.Arguments;
import com.facebook.react.bridge.BaseActivityEventListener;
import com.facebook.react.bridge.Promise;
import com.facebook.react.bridge.ReactApplicationContext;
import com.facebook.react.bridge.ReadableMap;
import com.facebook.react.bridge.UiThreadUtil;
import com.facebook.react.bridge.WritableMap;

import org.json.JSONArray;
import org.json.JSONObject;

import java.math.BigDecimal;
import java.net.URI;
import java.util.Collections;
import java.util.Currency;

import ru.yoomoney.sdk.kassa.payments.Checkout;
import ru.yoomoney.sdk.kassa.payments.TokenizationResult;
import ru.yoomoney.sdk.kassa.payments.checkoutParameters.Amount;
import ru.yoomoney.sdk.kassa.payments.checkoutParameters.PaymentMethodType;
import ru.yoomoney.sdk.kassa.payments.checkoutParameters.PaymentParameters;
import ru.yoomoney.sdk.kassa.payments.checkoutParameters.SavePaymentMethod;

/** YooKassa Activity adapter. Payment tokens are only returned in memory to the current JS call. */
final class YooCheckout extends BaseActivityEventListener {
    private final ReactApplicationContext context;
    private final Object lock = new Object();
    private Operation operation;
    private long generation;
    private int nextRequestCode = 6200;

    private static final class Operation {
        final int requestCode; final String orderId, accountId, sessionId, kind;
        final long epoch; final Promise promise;
        Operation(int requestCode, String orderId, String accountId, String sessionId, String kind, long epoch, Promise promise) {
            this.requestCode = requestCode; this.orderId = orderId; this.accountId = accountId;
            this.sessionId = sessionId; this.kind = kind; this.epoch = epoch; this.promise = promise;
        }
    }

    YooCheckout(ReactApplicationContext context) {
        this.context = context;
        context.addActivityEventListener(this);
    }

    JSONObject capabilities() throws Exception {
        return new JSONObject().put("configured", configured()).put("shopId", configured() ? BuildConfig.YOOKASSA_SHOP_ID : "")
                .put("environment", BuildConfig.YOOKASSA_SDK_ENVIRONMENT);
    }

    private static boolean configured() {
        String key = BuildConfig.YOOKASSA_SDK_CLIENT_KEY, shop = BuildConfig.YOOKASSA_SHOP_ID;
        return !shop.isEmpty() && shop.matches("[0-9]{4,16}")
                && ("test".equals(BuildConfig.YOOKASSA_SDK_ENVIRONMENT) && key.startsWith("test_")
                    || "live".equals(BuildConfig.YOOKASSA_SDK_ENVIRONMENT) && key.startsWith("live_"));
    }

    void tokenize(ReadableMap input, Promise promise) {
        long requestGeneration = generation();
        UiThreadUtil.runOnUiThread(() -> {
            try {
                JSONObject value = new JSONObject(input.toHashMap());
                JSONArray methods = value.getJSONArray("allowedPaymentMethods");
                long amountMinor = value.getLong("amountMinor");
                String orderId = value.getString("orderId"), title = value.getString("title"), subtitle = value.getString("subtitle");
                if (!configured() || !PushContract.uuid(orderId) || !BuildConfig.YOOKASSA_SHOP_ID.equals(value.getString("shopId"))
                        || !BuildConfig.YOOKASSA_SDK_ENVIRONMENT.equals(value.getString("environment"))
                        || amountMinor < 100 || amountMinor > 35_000_000 || !"RUB".equals(value.getString("currency"))
                        || !"off".equals(value.getString("savePaymentMethod")) || methods.length() != 1
                        || !"BANK_CARD".equals(methods.getString(0)) || !PushContract.bounded(title, 128)
                        || title.isEmpty() || !PushContract.bounded(subtitle, 256)
                        || PushContract.instant(value.getString("draftExpiresAt")) <= System.currentTimeMillis())
                    throw new IllegalArgumentException();
                PaymentParameters parameters = new PaymentParameters(
                        new Amount(BigDecimal.valueOf(amountMinor, 2), Currency.getInstance("RUB")), title, subtitle,
                        BuildConfig.YOOKASSA_SDK_CLIENT_KEY, BuildConfig.YOOKASSA_SHOP_ID, SavePaymentMethod.OFF,
                        Collections.singleton(PaymentMethodType.BANK_CARD));
                launch(value, "tokenize", Checkout.createTokenizeIntent(context, parameters), requestGeneration, promise);
            } catch (Exception failure) {promise.reject("sdk_unavailable", "Unable to open payment form");}
        });
    }

    void confirm(ReadableMap input, Promise promise) {
        long requestGeneration = generation();
        UiThreadUtil.runOnUiThread(() -> {
            try {
                JSONObject value = new JSONObject(input.toHashMap());
                String orderId = value.getString("orderId"), url = value.getString("confirmationUrl");
                if (!configured() || !PushContract.uuid(orderId) || !"BANK_CARD".equals(value.getString("paymentMethodType"))
                        || !BuildConfig.YOOKASSA_SHOP_ID.equals(value.getString("shopId"))
                        || !BuildConfig.YOOKASSA_SDK_ENVIRONMENT.equals(value.getString("environment"))
                        || !confirmationUrl(url)) throw new IllegalArgumentException();
                launch(value, "confirm", Checkout.createConfirmationIntent(context, url, PaymentMethodType.BANK_CARD,
                        BuildConfig.YOOKASSA_SDK_CLIENT_KEY, BuildConfig.YOOKASSA_SHOP_ID), requestGeneration, promise);
            } catch (Exception failure) {promise.reject("sdk_unavailable", "Unable to open payment confirmation");}
        });
    }

    private long generation() { synchronized (lock) {return generation;} }

    void invalidate() {
        Operation active;
        synchronized (lock) {generation++; active = operation; operation = null;}
        if (active != null) {
            active.promise.reject("session_changed", "Payment session changed");
            UiThreadUtil.runOnUiThread(() -> {
                Activity activity = context.getCurrentActivity();
                if (activity != null) activity.finishActivity(active.requestCode);
            });
        }
    }

    private void launch(JSONObject input, String kind, Intent intent, long requestGeneration, Promise promise) throws Exception {
        Activity activity = context.getCurrentActivity();
        AuthStore store = new AuthStore(context);
        JSONObject auth = store.active();
        String accountId = input.getString("accountId"), sessionId = input.getString("sessionId");
        long expectedEpoch = input.getLong("authEpoch");
        if (!(activity instanceof MainActivity) || auth == null || !PushContract.uuid(accountId)
                || !PushContract.uuid(sessionId) || expectedEpoch < 0 || store.epoch() != expectedEpoch
                || !accountId.equals(auth.getJSONObject("account").getString("id"))
                || !sessionId.equals(auth.getJSONObject("session").getString("id"))) throw new IllegalStateException();
        synchronized (lock) {
            if (operation != null || generation != requestGeneration) throw new IllegalStateException();
            int code = ++nextRequestCode;
            operation = new Operation(code, input.getString("orderId"), accountId, sessionId, kind, expectedEpoch, promise);
            try {activity.startActivityForResult(intent, code);}
            catch (RuntimeException failure) {operation = null; throw failure;}
        }
    }

    @Override public void onActivityResult(Activity activity, int requestCode, int resultCode, Intent data) {
        Operation current;
        synchronized (lock) {
            if (operation == null || operation.requestCode != requestCode) return;
            current = operation; operation = null;
        }
        try {
            AuthStore store = new AuthStore(context);
            JSONObject auth = store.active();
            if (auth == null || store.epoch() != current.epoch
                    || !current.accountId.equals(auth.getJSONObject("account").getString("id"))
                    || !current.sessionId.equals(auth.getJSONObject("session").getString("id"))) {
                current.promise.reject("session_changed", "Payment session changed"); return;
            }
            WritableMap result = Arguments.createMap();
            if (resultCode == Checkout.RESULT_OK) {
                if ("tokenize".equals(current.kind)) {
                    TokenizationResult token = Checkout.createTokenizationResult(data);
                    if (token == null || token.getPaymentToken() == null || token.getPaymentToken().isEmpty()
                            || token.getPaymentMethodType() != PaymentMethodType.BANK_CARD) {
                        result.putString("status", "error");
                    } else {
                        result.putString("status", "tokenized");
                        result.putString("paymentToken", token.getPaymentToken());
                        result.putString("paymentMethodType", "BANK_CARD");
                    }
                } else result.putString("status", "completed");
            } else if (resultCode == Checkout.RESULT_CANCELED) result.putString("status", "canceled");
            else if (resultCode == Checkout.RESULT_ERROR) result.putString("status", "error");
            else result.putString("status", "unknown");
            current.promise.resolve(result);
        } catch (Exception failure) {current.promise.reject("sdk_unavailable", "Payment result unavailable");}
    }

    private static boolean confirmationUrl(String value) {
        if (value == null || value.length() > 2048 || value.codePoints().anyMatch(c -> c < 32 || c == 127 || c == '\\')) return false;
        try {
            URI url = new URI(value);
            String host = url.getHost();
            if (!"https".equals(url.getScheme()) || url.getRawUserInfo() != null || url.getRawFragment() != null
                    || url.getPort() != -1 && url.getPort() != 443 || host == null || host.length() > 253
                    || !host.matches("[A-Za-z0-9.-]+") || !host.contains(".") || host.endsWith(".")
                    || host.matches("[0-9.]+") || host.equals("localhost") || host.endsWith(".localhost")
                    || host.endsWith(".local") || host.endsWith(".internal") || host.endsWith(".invalid")) return false;
            for (String label : host.split("\\.")) if (label.isEmpty() || label.length() > 63 || label.startsWith("-") || label.endsWith("-")) return false;
            return true;
        } catch (Exception ignored) {return false;}
    }
}
