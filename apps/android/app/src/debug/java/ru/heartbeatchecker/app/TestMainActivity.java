package ru.heartbeatchecker.app;

import java.util.ArrayList;
import java.util.List;

public final class TestMainActivity extends MainActivity {
    static final List<RecordingLauncher> launchers = new ArrayList<>();

    static void reset() {
        launchers.clear();
    }

    @Override
    BrowserLauncher createBrowserLauncher() {
        RecordingLauncher launcher = new RecordingLauncher();
        launchers.add(launcher);
        return launcher;
    }

    static final class RecordingLauncher implements BrowserLauncher {
        final List<String> urls = new ArrayList<>();
        Runnable completed;
        Runnable unavailable;
        boolean destroyed;

        @Override
        public void launch(String url, Runnable completed, Runnable unavailable) {
            urls.add(url);
            this.completed = completed;
            this.unavailable = unavailable;
        }

        @Override
        public void destroy() {
            destroyed = true;
        }

        void complete() {
            completed.run();
        }

        void fail() {
            unavailable.run();
        }
    }
}
