package ru.heartbeatchecker.app;

import org.junit.Rule;
import org.junit.Test;
import org.junit.rules.TemporaryFolder;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.List;

import static org.junit.Assert.*;

public class DraftStoreTest {
    @Rule
    public TemporaryFolder temporaryFolder = new TemporaryFolder();

    @Test
    public void addPersistsNewestFirstAndDeleteKeepsRemainingDrafts() throws Exception {
        DraftStore store = new DraftStore(temporaryFolder.getRoot());
        Draft first = draft("https://one.example.test/", "one");
        Draft second = draft("https://two.example.test/", "two");
        store.add(first);
        store.add(second);

        List<Draft> loaded = new DraftStore(temporaryFolder.getRoot()).load();
        assertEquals(2, loaded.size());
        assertEquals(second.id, loaded.get(0).id);
        assertEquals(first.id, loaded.get(1).id);

        store.delete(second.id);
        loaded = store.load();
        assertEquals(1, loaded.size());
        assertEquals(first.id, loaded.get(0).id);
    }

    @Test
    public void enforcesTwentyDraftLimitWithoutOverwritingExistingQueue() throws Exception {
        DraftStore store = new DraftStore(temporaryFolder.getRoot());
        for (int i = 0; i < DraftStore.LIMIT; i++) {
            store.add(draft("https://example.test/check/" + i, "draft " + i));
        }
        List<Draft> before = store.load();
        assertEquals(DraftStore.LIMIT, before.size());
        assertThrows(IOException.class, () -> store.add(draft("https://overflow.example.test/", "overflow")));

        List<Draft> after = store.load();
        assertEquals(DraftStore.LIMIT, after.size());
        assertEquals(before.get(0).id, after.get(0).id);
        assertEquals(before.get(DraftStore.LIMIT - 1).id, after.get(DraftStore.LIMIT - 1).id);
    }

    @Test
    public void clearRemovesPersistedAndPendingFiles() throws Exception {
        DraftStore store = new DraftStore(temporaryFolder.getRoot());
        store.add(draft("https://example.test/", "clear me"));
        File persisted = new File(temporaryFolder.getRoot(), "monitor-drafts.json");
        File pending = new File(temporaryFolder.getRoot(), "monitor-drafts.json.new");
        assertTrue(persisted.exists());
        assertTrue(pending.createNewFile());

        store.clear();
        assertFalse(persisted.exists());
        assertFalse(pending.exists());
        assertTrue(store.load().isEmpty());
    }

    @Test
    public void rejectsMalformedDuplicateOversizedAndOverLimitStoredQueues() throws Exception {
        File directory = temporaryFolder.getRoot();
        File file = new File(directory, "monitor-drafts.json");
        Draft duplicate = draft("https://example.test/duplicate", "same id");
        String item = duplicate.toJson().toString();

        write(file, "not json");
        assertThrows(IOException.class, () -> new DraftStore(directory).load());
        write(file, "[" + item + "," + item + "]");
        assertThrows(IOException.class, () -> new DraftStore(directory).load());

        StringBuilder overLimit = new StringBuilder("[");
        for (int i = 0; i <= DraftStore.LIMIT; i++) {
            if (i > 0) overLimit.append(',');
            Draft entry = draft("https://example.test/entry/" + i, "entry " + i);
            overLimit.append(entry.toJson());
        }
        write(file, overLimit.append(']').toString());
        assertThrows(IOException.class, () -> new DraftStore(directory).load());

        write(file, "x".repeat(100_001));
        assertThrows(IOException.class, () -> new DraftStore(directory).load());
    }

    private static Draft draft(String url, String name) {
        return Draft.create(url, name);
    }

    private static void write(File file, String content) throws IOException {
        try (FileOutputStream output = new FileOutputStream(file)) {
            output.write(content.getBytes(StandardCharsets.UTF_8));
        }
    }
}
