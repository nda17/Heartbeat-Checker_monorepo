package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.ContextWrapper;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import com.facebook.react.bridge.Arguments;
import com.facebook.react.bridge.BridgeReactContext;
import com.facebook.react.bridge.Callback;
import com.facebook.react.bridge.Promise;
import com.facebook.react.bridge.PromiseImpl;
import com.facebook.react.bridge.ReadableType;
import com.facebook.react.bridge.ReactApplicationContext;
import com.facebook.react.bridge.WritableArray;

import org.junit.After;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.File;
import java.lang.reflect.Field;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class HeartbeatBridgePromiseTest {
    private Context applicationContext;
    private File testNoBackupDir;
    private ReactApplicationContext reactContext;
    private HeartbeatBridge bridge;

    @Before
    public void setUp() throws Exception {
        applicationContext = ApplicationProvider.getApplicationContext();
        testNoBackupDir = new File(applicationContext.getNoBackupFilesDir(),
                "heartbeat-bridge-promise-test-" + java.util.UUID.randomUUID());
        assertTrue("test auth directory must be isolated", testNoBackupDir.mkdirs());
        assertEquals(applicationContext.getNoBackupFilesDir(), testNoBackupDir.getParentFile());
        Context isolated = new ContextWrapper(applicationContext) {
            @Override public File getNoBackupFilesDir() { return testNoBackupDir; }
            @Override public Context getApplicationContext() { return this; }
        };
        reactContext = new BridgeReactContext(isolated);
        assertEquals(testNoBackupDir, reactContext.getNoBackupFilesDir());
        bridge = new HeartbeatBridge(reactContext);
    }

    @After
    public void tearDown() throws Exception {
        if (bridge != null) {
            shutdownExecutor(bridge, "io");
            shutdownExecutor(bridge, "pushIo");
        }
        deleteRecursively(testNoBackupDir);
    }

    @Test
    public void authEpochResolvesAsReactNativeSupportedNumberAndTracksStateChanges() throws Exception {
        assertEquals("baseline is read from the isolated native store", 0L, new AuthStore(reactContext).epoch());
        assertEquals(0.0, callAuthEpoch(), 0.0);

        new AuthStore(reactContext).logout();

        assertEquals(1L, new AuthStore(reactContext).epoch());
        assertEquals(1.0, callAuthEpoch(), 0.0);
    }

    private double callAuthEpoch() throws Exception {
        CountDownLatch finished = new CountDownLatch(1);
        AtomicReference<WritableArray> converted = new AtomicReference<>();
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Callback resolve = args -> {
            try {
                // PromiseImpl invokes the actual callback API; this is the conversion used by RN's Java bridge.
                converted.set(Arguments.fromJavaArgs(args));
            } catch (Throwable thrown) {
                failure.set(thrown);
            } finally {
                finished.countDown();
            }
        };
        Callback reject = args -> {
            failure.set(new AssertionError("native promise rejected: " + java.util.Arrays.toString(args)));
            finished.countDown();
        };
        Promise promise = new PromiseImpl(resolve, reject);
        bridge.authEpoch(promise);

        assertTrue("native bridge promise must settle", finished.await(5, TimeUnit.SECONDS));
        if (failure.get() != null) throw new AssertionError("authEpoch must cross RN's argument conversion", failure.get());
        assertNotNull(converted.get());
        assertEquals(1, converted.get().size());
        assertEquals(ReadableType.Number, converted.get().getType(0));
        return converted.get().getDouble(0);
    }

    private static void shutdownExecutor(Object target, String name) throws Exception {
        Field field = target.getClass().getDeclaredField(name);
        field.setAccessible(true);
        ((java.util.concurrent.ExecutorService) field.get(target)).shutdownNow();
    }

    private static void deleteRecursively(File file) {
        if (file == null || !file.exists()) return;
        File[] children = file.listFiles();
        if (children != null) for (File child : children) deleteRecursively(child);
        // The per-test directory contains only this fixture's AtomicFile state.
        file.delete();
    }
}
