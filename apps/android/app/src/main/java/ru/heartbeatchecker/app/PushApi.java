package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.URL;
import java.nio.charset.StandardCharsets;

import javax.net.ssl.HttpsURLConnection;

final class PushApi {
    static final class Failure extends IOException {
        final int status;
        Failure(int status) { super("Push request failed"); this.status = status; }
    }

    JSONObject request(String method, String path, String credential, JSONObject body) throws IOException, JSONException {
        if (!path.matches("/api/v1/push/(pairings(?:/[0-9a-f-]{36}/exchange)?|installation(?:/deliveries/[0-9a-f-]{36})?)"))
            throw new IOException("Invalid push endpoint");
        HttpsURLConnection connection = (HttpsURLConnection) new URL(PushContract.ORIGIN + path).openConnection();
        connection.setInstanceFollowRedirects(false);
        connection.setConnectTimeout(5000);
        connection.setReadTimeout(5000);
        connection.setRequestMethod(method);
        connection.setRequestProperty("Accept", "application/json");
        connection.setRequestProperty("Cache-Control", "no-store");
        // A dedicated connection sends neither browser cookies nor redirects.
        if (credential != null && !credential.isEmpty()) connection.setRequestProperty("Authorization", "Bearer " + credential);
        try {
            if (body != null) {
                byte[] bytes = body.toString().getBytes(StandardCharsets.UTF_8);
                if (bytes.length > PushContract.JSON_LIMIT) throw new IOException("Push request too large");
                connection.setDoOutput(true);
                connection.setRequestProperty("Content-Type", "application/json");
                connection.setFixedLengthStreamingMode(bytes.length);
                try (java.io.OutputStream output = connection.getOutputStream()) { output.write(bytes); }
            }
            int status = connection.getResponseCode();
            if (status < 200 || status >= 300) throw new Failure(status);
            if (status == 204) return new JSONObject();
            if (connection.getContentLengthLong() > PushContract.JSON_LIMIT) throw new IOException("Push response too large");
            try (InputStream input = connection.getInputStream(); ByteArrayOutputStream output = new ByteArrayOutputStream()) {
                byte[] buffer = new byte[1024];
                int count;
                long deadline = android.os.SystemClock.elapsedRealtime() + 10000;
                while ((count = input.read(buffer)) != -1) {
                    if (output.size() + count > PushContract.JSON_LIMIT || android.os.SystemClock.elapsedRealtime() > deadline)
                        throw new IOException("Push response limit");
                    output.write(buffer, 0, count);
                }
                return new JSONObject(output.toString(StandardCharsets.UTF_8.name()));
            }
        } finally { connection.disconnect(); }
    }
}
