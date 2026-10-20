package ru.heartbeatchecker.app;

import java.util.List;
import org.json.JSONObject;

import ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException;
import ru.rustore.sdk.pushclient.messaging.model.RemoteMessage;
import ru.rustore.sdk.pushclient.messaging.service.RuStoreMessagingService;

public final class HeartbeatMessagingService extends RuStoreMessagingService {
    @Override public void onNewToken(String token) { PushSdk.rotated(this, token); }

    @Override public void onMessageReceived(RemoteMessage message) {
        // The server uses data-only messages. No payload-derived URL or content is displayed.
        PushStore store = new PushStore(this);
        store.deliveryStage(PushStore.DeliveryStage.RECEIVED);
        JSONObject envelope;
        try { envelope = PushContract.envelope(message, System.currentTimeMillis()); }
        catch (PushContract.EnvelopeException invalid) {
            if (invalid.reason == PushContract.EnvelopeReason.EXPIRED) store.deliveryStage(PushStore.DeliveryStage.EXPIRED);
            else store.deliveryStage(PushStore.DeliveryStage.INVALID_ENVELOPE, invalid.reason);
            return;
        }
        catch (Exception ignored) {
            store.deliveryStage(PushStore.DeliveryStage.INVALID_ENVELOPE, PushContract.EnvelopeReason.SDK_MESSAGE);
            return;
        }
        try { PushWork.delivery(this, envelope); }
        catch (Exception ignored) { store.deliveryStage(PushStore.DeliveryStage.QUEUE_FAILED); }
    }

    @Override public void onDeletedMessages() { /* The cabinet remains the history authority. */ }
    @Override public void onError(List<? extends RuStorePushClientException> errors) {
        // Only the reviewed enum is retained; exception messages/from may contain credentials.
        new PushStore(this).providerIssue(providerIssue(errors));
    }

    static PushStore.ProviderIssue providerIssue(List<? extends RuStorePushClientException> errors) {
        PushStore.ProviderIssue issue = PushStore.ProviderIssue.OTHER;
        if (errors != null) for (RuStorePushClientException error : errors) {
            if (error instanceof RuStorePushClientException.UnauthorizedException) {
                issue = PushStore.ProviderIssue.DISTRIBUTOR_AUTH_REQUIRED; break;
            }
            if (error instanceof RuStorePushClientException.HostAppNotInstalledException)
                issue = PushStore.ProviderIssue.DISTRIBUTOR_MISSING;
            else if (error instanceof RuStorePushClientException.HostAppBackgroundWorkPermissionNotGranted
                    && issue == PushStore.ProviderIssue.OTHER)
                issue = PushStore.ProviderIssue.DISTRIBUTOR_BACKGROUND_RESTRICTED;
        }
        return issue;
    }
}
