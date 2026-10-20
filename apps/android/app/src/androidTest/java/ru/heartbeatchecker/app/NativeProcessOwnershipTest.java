package ru.heartbeatchecker.app;

import android.content.ComponentName;
import android.content.Context;
import android.content.pm.PackageInfo;
import android.content.pm.PackageManager;
import android.content.pm.ProviderInfo;
import android.content.pm.ActivityInfo;
import android.content.pm.ServiceInfo;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.junit.Test;
import org.junit.runner.RunWith;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;

@RunWith(AndroidJUnit4.class)
public final class NativeProcessOwnershipTest {
    @Test
    public void installedPushCallbacksRunInMainProcessAndHaveNoHuaweiComponents() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        PackageManager packageManager = context.getPackageManager();
        String packageName = context.getPackageName();

        for (String appService : new String[] {
                FirebasePushService.class.getName(),
                HeartbeatMessagingService.class.getName(),
        }) {
            assertEquals(appService + " must access main-process push state",
                    packageName, service(packageManager, packageName, appService).processName);
        }

        PackageInfo installed = packageManager.getPackageInfo(packageName,
                PackageManager.GET_SERVICES | PackageManager.GET_PROVIDERS | PackageManager.GET_RECEIVERS);
        assertNotNull(installed.services);
        for (ServiceInfo item : installed.services) assertNoHuawei(item.name, item.processName);
        if (installed.providers != null) for (ProviderInfo item : installed.providers) assertNoHuawei(item.name, item.processName);
        if (installed.receivers != null) for (ActivityInfo item : installed.receivers) assertNoHuawei(item.name, item.processName);
    }

    private static void assertNoHuawei(String className, String processName) {
        assertFalse("Huawei component remains in release manifest: " + className,
                className != null && className.toLowerCase(java.util.Locale.ROOT).contains("huawei"));
        assertFalse("Huawei component remains in release process: " + processName,
                processName != null && processName.toLowerCase(java.util.Locale.ROOT).contains("huawei"));
        assertFalse("Huawei push process remains: " + processName,
                processName != null && processName.endsWith(":pushservice"));
    }

    private static ServiceInfo service(PackageManager packageManager, String packageName, String className)
            throws PackageManager.NameNotFoundException {
        return packageManager.getServiceInfo(new ComponentName(packageName, className), PackageManager.GET_META_DATA);
    }
}
