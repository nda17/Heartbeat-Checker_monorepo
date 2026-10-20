package ru.heartbeatchecker.app;

import android.content.Context;
import java.util.Map;
import org.json.JSONObject;

/** All transports share the same envelope, binding, expiry and durable dedup. */
final class PushReceiver {
    private PushReceiver() {}
    static void receive(Context context, Map<String, String> data) {
        PushStore store = new PushStore(context);
        store.deliveryStage(PushStore.DeliveryStage.RECEIVED);
        try {
            JSONObject envelope = PushContract.envelope(data, System.currentTimeMillis());
            PushWork.delivery(context, envelope);
        } catch (PushContract.EnvelopeException invalid) {
            if (invalid.reason == PushContract.EnvelopeReason.EXPIRED) store.deliveryStage(PushStore.DeliveryStage.EXPIRED);
            else store.deliveryStage(PushStore.DeliveryStage.INVALID_ENVELOPE, invalid.reason);
        } catch (Exception ignored) { store.deliveryStage(PushStore.DeliveryStage.QUEUE_FAILED); }
    }
}
