package ru.heartbeatchecker.app;

import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;

import com.facebook.react.ReactActivity;
import com.facebook.react.ReactActivityDelegate;
import com.facebook.react.defaults.DefaultReactActivityDelegate;

import java.util.HashSet;
import java.util.Set;

/** Exact App Links only; browser cookies and arbitrary intents never enter React Native. */
public class MainActivity extends ReactActivity {
    private static final String ORIGIN = "https://heartbeat-checker.ru";
    private static final String SECRET = "[A-Za-z0-9_-]{20,512}";

    @Override protected String getMainComponentName() { return "HeartbeatChecker"; }
    @Override protected ReactActivityDelegate createReactActivityDelegate() {
        return new DefaultReactActivityDelegate(this, getMainComponentName());
    }

    @Override protected void onCreate(Bundle state) {
        Intent intent = getIntent();
        if (intent != null && Intent.ACTION_VIEW.equals(intent.getAction())) {
            String link = normalize(intent);
            if (link == null) { super.onCreate(state); finish(); return; }
            intent.setData(Uri.parse(link)); setIntent(intent);
        }
        super.onCreate(state);
    }

    @Override public void onNewIntent(Intent intent) {
        if (intent == null || !Intent.ACTION_VIEW.equals(intent.getAction())) return;
        String link = normalize(intent);
        if (link == null) return;
        intent.setData(Uri.parse(link)); setIntent(intent);
        super.onNewIntent(intent);
    }

    static String normalize(Intent intent) {
        if (BuildConfig.DEV_CLIENT) return null;
        if (intent == null || !Intent.ACTION_VIEW.equals(intent.getAction())) return null;
        Uri uri = intent.getData();
        if (uri == null || !"https".equals(uri.getScheme()) || !"heartbeat-checker.ru".equals(uri.getEncodedAuthority())
                || uri.getUserInfo() != null || uri.getPort() != -1 || uri.toString().length() > 16 * 1024) return null;
        String path = uri.getEncodedPath();
        if (path == null) return null;
        if (path.equals("/app") || path.equals("/app/settings") || path.equals("/app/billing"))
            return LinkPolicy.appLink(uri.toString());
        String token = uri.getFragment();
        if (token == null || token.isEmpty()) {
            String extra = intent.getStringExtra("token");
            if (extra != null) token = "token=" + extra;
        }
        if (path.equals("/android/auth/reset") || path.equals("/android/auth/verify")) {
            if (path.equals("/android/auth/reset") && uri.getQuery() != null) return null;
            if (path.equals("/android/auth/verify") && (!"oauth=1".equals(uri.getEncodedQuery()))) return null;
            if (token == null || !token.matches("token=" + SECRET)) return null;
            return ORIGIN + path + (path.equals("/android/auth/verify") ? "?oauth=1" : "") + "#" + token;
        }
        if (uri.getFragment() != null) return null;
        if (path.equals("/android/payments/return")) {
            if (!keys(uri, "order")) return null;
            String order = uri.getQueryParameter("order");
            return PushContract.uuid(order) ? ORIGIN + path + "?order=" + order : null;
        }
        if (path.equals("/android/auth/complete")) {
            if (keys(uri, "flowId", "state", "error")) {
                String flow = uri.getQueryParameter("flowId"), state = uri.getQueryParameter("state"), error = uri.getQueryParameter("error");
                if (!PushContract.uuid(flow) || state == null || !state.matches("[A-Za-z0-9_-]{43}")
                        || !"oauth_cancelled".equals(error) && !"oauth_provider_unavailable".equals(error)) return null;
                return Uri.parse(ORIGIN + path).buildUpon().appendQueryParameter("flowId", flow)
                        .appendQueryParameter("state", state).appendQueryParameter("error", error).build().toString();
            }
            if (!keys(uri, "flowId", "state", "code", "deviceId") && !keys(uri, "flowId", "state", "code")) return null;
            String flow = uri.getQueryParameter("flowId"), state = uri.getQueryParameter("state"), code = uri.getQueryParameter("code");
            String device = uri.getQueryParameter("deviceId");
            if (!PushContract.uuid(flow) || state == null || !state.matches("[A-Za-z0-9_-]{43}")
                    || code == null || code.isEmpty() || !PushContract.bounded(code, 8192)
                    || device != null && (device.isEmpty() || !PushContract.bounded(device, 4096))) return null;
            Uri.Builder safe = Uri.parse(ORIGIN + path).buildUpon().appendQueryParameter("flowId", flow)
                    .appendQueryParameter("state", state).appendQueryParameter("code", code);
            if (device != null) safe.appendQueryParameter("deviceId", device);
            return safe.build().toString();
        }
        return null;
    }

    private static boolean keys(Uri uri, String... expected) {
        Set<String> keys = uri.getQueryParameterNames();
        if (keys.size() != expected.length) return false;
        Set<String> wanted = new HashSet<>(java.util.Arrays.asList(expected));
        if (!keys.equals(wanted)) return false;
        String query = uri.getEncodedQuery();
        if (query == null || query.isEmpty()) return false;
        for (String key : expected) if (query.split("(?:^|&)" + key + "=", -1).length != 2) return false;
        return true;
    }
}
