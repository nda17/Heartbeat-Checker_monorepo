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
import static org.junit.Assert.assertTrue;

@RunWith(AndroidJUnit4.class)
public final class NativeProcessOwnershipTest {
    @Test
    public void installedPushCallbacksStayOutsideHmsProcessAndMainProcessGuardIsFailClosed() throws Exception {
        Context context = ApplicationProvider.getApplicationContext();
        PackageManager packageManager = context.getPackageManager();
        String packageName = context.getPackageName();

        for (String appService : new String[] {
                FirebasePushService.class.getName(),
                HuaweiPushService.class.getName(),
                HeartbeatMessagingService.class.getName(),
        }) {
            assertEquals(appService + " must access main-process push state",
                    packageName, service(packageManager, packageName, appService).processName);
        }
        assertFalse("Huawei SDK process must not be treated as the React Native main process",
                HeartbeatApplication.isMainProcess(packageName + ":pushservice", packageName));
        assertFalse("unknown process must fail closed", HeartbeatApplication.isMainProcess(null, packageName));
        assertTrue("application process remains the sole main process",
                HeartbeatApplication.isMainProcess(packageName, packageName));

        PackageInfo installed = packageManager.getPackageInfo(packageName,
                PackageManager.GET_SERVICES | PackageManager.GET_PROVIDERS | PackageManager.GET_RECEIVERS | PackageManager.GET_DISABLED_COMPONENTS);
        assertNotNull(installed.services);
        ServiceInfo huawei = service(packageManager, packageName, HuaweiPushService.class.getName());
        assertFalse("Huawei callback service must not be exported", huawei.exported);
        for (ServiceInfo item : installed.services) assertNoHmsWorkInVendorProcess(item.name, item.processName, packageName);
        if (installed.providers != null) for (ProviderInfo item : installed.providers) assertNoHmsWorkInVendorProcess(item.name, item.processName, packageName);
        if (installed.receivers != null) for (ActivityInfo item : installed.receivers) assertNoHmsWorkInVendorProcess(item.name, item.processName, packageName);
    }

    private static void assertNoHmsWorkInVendorProcess(String className, String processName, String packageName) {
        if (processName != null && processName.endsWith(":pushservice")) {
            assertFalse("app-owned component must not run in Huawei's isolated process: " + className,
                    className != null && className.startsWith(packageName + "."));
        }
    }

    private static ServiceInfo service(PackageManager packageManager, String packageName, String className)
            throws PackageManager.NameNotFoundException {
        return packageManager.getServiceInfo(new ComponentName(packageName, className),
                PackageManager.GET_META_DATA | PackageManager.MATCH_DISABLED_COMPONENTS);
    }
}
