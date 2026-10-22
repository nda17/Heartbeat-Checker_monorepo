package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const devAPKName = "Heartbeat-Checker-dev.apk"
const devAPKDownloadName = "devHeartbeat Checker.apk"
const devAPKPath = "/api/v1/admin/android-dev/apk"
const maxDevAPKBytes = 150 << 20

var devHex40 = regexp.MustCompile(`^[a-f0-9]{40}$`)
var devHex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var devFingerprint = regexp.MustCompile(`^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$`)

type devAPKManifest struct {
	SchemaVersion            int    `json:"schemaVersion"`
	PackageName              string `json:"packageName"`
	VersionCode              int    `json:"versionCode"`
	VersionName              string `json:"versionName"`
	SourceSHA                string `json:"sourceSha"`
	BuiltAt                  string `json:"builtAt"`
	SizeBytes                int64  `json:"sizeBytes"`
	SHA256                   string `json:"sha256"`
	SigningCertificateSHA256 string `json:"signingCertificateSha256"`
}

type devAPKHandler struct {
	root  string
	slots chan struct{}
}

// RegisterDevAPK uses the ordinary web-admin cookie and MFA gate before any
// manifest lookup, HEAD response, Range handling, or APK byte is served.
func RegisterDevAPK(mux *http.ServeMux, root string, protect func(http.Handler) http.Handler) {
	h := &devAPKHandler{root: root, slots: make(chan struct{}, 2)}
	mux.Handle("GET /api/v1/admin/android-dev", protect(http.HandlerFunc(h.metadata)))
	mux.Handle("GET "+devAPKPath, protect(http.HandlerFunc(h.download)))
}

func (h *devAPKHandler) acquire(w http.ResponseWriter) bool {
	select {
	case h.slots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "10")
		devWrite(w, http.StatusTooManyRequests, map[string]string{"error": "download_busy"})
		return false
	}
}

func (h *devAPKHandler) release() { <-h.slots }

func devWrite(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func validDevManifest(m devAPKManifest) bool {
	return m.SchemaVersion == 1 && m.PackageName == "ru.heartbeatchecker.app.dev" &&
		m.VersionCode > 0 && len(m.VersionName) > 0 && len(m.VersionName) <= 64 &&
		!strings.ContainsAny(m.VersionName, " \t\r\n/\\\x00") &&
		devHex40.MatchString(m.SourceSHA) && devHex64.MatchString(m.SHA256) &&
		devFingerprint.MatchString(m.SigningCertificateSHA256) &&
		m.SizeBytes > 0 && m.SizeBytes < maxDevAPKBytes &&
		validDevTime(m.BuiltAt)
}

func validDevTime(raw string) bool {
	parsed, err := time.Parse(time.RFC3339, raw)
	return err == nil && parsed.UTC().Format(time.RFC3339) == raw
}

func regular(path string, maxSize int64) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, errors.New("invalid private artifact")
	}
	return info, nil
}

func (h *devAPKHandler) openVerified() (*os.File, devAPKManifest, error) {
	var m devAPKManifest
	if h.root == "" || !filepath.IsAbs(h.root) || filepath.Clean(h.root) != h.root {
		return nil, m, errors.New("private artifact unavailable")
	}
	root, err := os.Lstat(h.root)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return nil, m, errors.New("private artifact unavailable")
	}
	manifestPath := filepath.Join(h.root, "current.json")
	if _, err = regular(manifestPath, 4096); err != nil {
		return nil, m, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, m, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF || !validDevManifest(m) {
		return nil, m, errors.New("invalid private manifest")
	}
	releaseDir := filepath.Join(h.root, "releases", m.SHA256)
	dir, err := os.Lstat(releaseDir)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return nil, m, errors.New("invalid release directory")
	}
	path := filepath.Join(releaseDir, devAPKName)
	info, err := regular(path, maxDevAPKBytes)
	if err != nil || info.Size() != m.SizeBytes {
		return nil, m, errors.New("private APK size mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, m, err
	}
	digest := sha256.New()
	_, err = io.Copy(digest, io.LimitReader(f, maxDevAPKBytes))
	if err != nil || hex.EncodeToString(digest.Sum(nil)) != m.SHA256 {
		f.Close()
		return nil, m, errors.New("private APK checksum mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, m, err
	}
	return f, m, nil
}

func (h *devAPKHandler) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	if !h.acquire(w) {
		return
	}
	defer h.release()
	f, m, err := h.openVerified()
	if err != nil {
		devWrite(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	f.Close()
	devWrite(w, http.StatusOK, map[string]any{"release": m, "downloadPath": devAPKPath})
}

func (h *devAPKHandler) download(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	if !h.acquire(w) {
		return
	}
	defer h.release()
	f, m, err := h.openVerified()
	if err != nil {
		devWrite(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	defer f.Close()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute)); err != nil {
		devWrite(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="`+devAPKDownloadName+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+m.SHA256+`"`)
	http.ServeContent(w, r, devAPKName, time.Time{}, f)
}
