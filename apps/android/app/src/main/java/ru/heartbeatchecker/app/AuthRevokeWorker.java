package ru.heartbeatchecker.app;

import android.content.Context;

import androidx.annotation.NonNull;
import androidx.work.Constraints;
import androidx.work.BackoffPolicy;
import androidx.work.ExistingWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.OneTimeWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import org.json.JSONArray;
import org.json.JSONObject;

import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.TimeUnit;

/** Revoke-only capability queue; never reads or sends a bearer access token. */
public final class AuthRevokeWorker extends Worker {
    private static final String NAME = "heartbeat-native-session-revoke";

    public AuthRevokeWorker(@NonNull Context context, @NonNull WorkerParameters params) { super(context, params); }

    static void enqueue(Context context) {
        OneTimeWorkRequest request = new OneTimeWorkRequest.Builder(AuthRevokeWorker.class)
                .setConstraints(new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.SECONDS).build();
        // Replacing a running attempt ensures a newly queued logout is not missed by its earlier snapshot.
        WorkManager.getInstance(context).enqueueUniqueWork(NAME, ExistingWorkPolicy.REPLACE, request);
    }

    @NonNull @Override public Result doWork() {
        AuthStore store = new AuthStore(getApplicationContext());
        try {
            JSONArray queue = store.pendingRevokes();
            for (int i = 0; i < queue.length(); i++) {
                JSONObject item = queue.getJSONObject(i);
                String id = item.getString("sessionId");
                if (!PushContract.uuid(id) || PushContract.instant(item.getString("expiresAt")) <= System.currentTimeMillis()) {
                    store.finishRevoke(id); continue;
                }
                HttpURLConnection connection = (HttpURLConnection) new URL(BuildConfig.API_ORIGIN + "/api/v1/auth/native/revoke").openConnection();
                try {
                    connection.setInstanceFollowRedirects(false);
                    connection.setConnectTimeout(5000); connection.setReadTimeout(5000);
                    connection.setRequestMethod("POST"); connection.setDoOutput(true);
                    connection.setRequestProperty("Content-Type", "application/json");
                    connection.setRequestProperty("Accept", "application/json");
                    connection.setRequestProperty("Cache-Control", "no-store");
                    byte[] body = new JSONObject().put("sessionId", id).put("revokeToken", item.getString("revokeToken"))
                            .toString().getBytes(StandardCharsets.UTF_8);
                    connection.setFixedLengthStreamingMode(body.length);
                    try (java.io.OutputStream output = connection.getOutputStream()) {output.write(body);}
                    int status = connection.getResponseCode();
                    if (status == 204) store.finishRevoke(id);
                    else return Result.retry();
                } finally {connection.disconnect();}
            }
            return Result.success();
        } catch (Exception ignored) {
            return Result.retry();
        }
    }
}
