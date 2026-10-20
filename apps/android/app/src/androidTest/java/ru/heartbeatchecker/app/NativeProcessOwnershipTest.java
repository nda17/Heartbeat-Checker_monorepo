package ru.heartbeatchecker.app;

import android.content.ComponentName;
import android.content.Context;
import android.content.pm.PackageManager;
import android.content.pm.ServiceInfo;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.junit.Test;
import org.junit.runner.RunWith;

import static org.junit.Assert.assertEquals;

@RunWith(AndroidJUnit4.class)
public final class NativeProcessOwnershipTest {
    private static final String HMS_SERVICE = "com.huawei.hms.support.api.push.service.HmsMsgService";

    @Test
    public void onlyHuaweiSdkReceiverOwnsThePushserviceProcess() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        PackageManager packageManager = context.getPackageManager();
        String packageName = context.getPackageName();

        ServiceInfo hms = service(packageManager, packageName, HMS_SERVICE);
        assertEquals(packageName + ":pushservice", hms.processName);
        for (String appReceiver : new String[] {
                HuaweiPushService.class.getName(),
                FirebasePushService.class.getName(),
                HeartbeatMessagingService.class.getName(),
        }) {
            assertEquals(appReceiver + " must access main-process push state",
                    packageName, service(packageManager, packageName, appReceiver).processName);
        }
    }

    private static ServiceInfo service(PackageManager packageManager, String packageName, String className)
            throws PackageManager.NameNotFoundException {
        return packageManager.getServiceInfo(new ComponentName(packageName, className), PackageManager.GET_META_DATA);
    }
}
