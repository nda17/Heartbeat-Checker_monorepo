package ru.heartbeatchecker.app;

import org.json.JSONArray;
import org.json.JSONException;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

final class DraftStore {
    static final int LIMIT = 20;
    private final File file;

    DraftStore(File directory) {
        file = new File(directory, "monitor-drafts.json");
    }

    List<Draft> load() throws IOException {
        if (!file.exists()) return new ArrayList<>();
        if (file.length() > 100_000) throw new IOException("Хранилище черновиков повреждено");
        try {
            JSONArray json = new JSONArray(new String(Files.readAllBytes(file.toPath()), StandardCharsets.UTF_8));
            if (json.length() > LIMIT) throw new JSONException("Draft limit exceeded");
            List<Draft> drafts = new ArrayList<>();
            Set<String> ids = new HashSet<>();
            for (int i = 0; i < json.length(); i++) {
                Draft draft = Draft.fromJson(json.getJSONObject(i));
                if (!ids.add(draft.id)) throw new JSONException("Duplicate draft");
                drafts.add(draft);
            }
            return drafts;
        } catch (JSONException | IllegalArgumentException e) {
            throw new IOException("Не удалось прочитать черновики. Их можно удалить кнопкой очистки.", e);
        }
    }

    void add(Draft draft) throws IOException {
        List<Draft> drafts = load();
        if (drafts.size() >= LIMIT) throw new IOException("Сохранено 20 черновиков. Сначала удалите ненужные.");
        drafts.add(0, draft);
        save(drafts);
    }

    void delete(String id) throws IOException {
        List<Draft> drafts = load();
        drafts.removeIf(draft -> draft.id.equals(id));
        save(drafts);
    }

    void clear() throws IOException {
        Files.deleteIfExists(file.toPath());
        Files.deleteIfExists(new File(file.getParentFile(), file.getName() + ".new").toPath());
    }

    private void save(List<Draft> drafts) throws IOException {
        JSONArray json = new JSONArray();
        try {
            for (Draft draft : drafts) json.put(draft.toJson());
        } catch (JSONException e) {
            throw new IOException("Не удалось сохранить черновик", e);
        }
        File pending = new File(file.getParentFile(), file.getName() + ".new");
        try {
            try (FileOutputStream output = new FileOutputStream(pending)) {
                output.write(json.toString().getBytes(StandardCharsets.UTF_8));
                output.getFD().sync();
            }
            Files.move(pending.toPath(), file.toPath(), StandardCopyOption.ATOMIC_MOVE,
                    StandardCopyOption.REPLACE_EXISTING);
        } finally {
            Files.deleteIfExists(pending.toPath());
        }
    }
}
