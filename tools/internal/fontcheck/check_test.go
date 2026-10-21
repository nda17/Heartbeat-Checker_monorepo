package fontcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/image/font/sfnt"
)

func TestRepositoryFontAssetsMatchPinnedProvenanceAndGlyphCoverage(t *testing.T) {
	if err := Check(filepath.Join(repositoryRoot(t), "apps/web")); err != nil {
		t.Fatalf("committed OG font provenance failed: %v", err)
	}
}

func TestFontCheckRejectsChangedMissingAndSymlinkedAssets(t *testing.T) {
	source := filepath.Join(repositoryRoot(t), "apps/web", "public", "fonts")
	for _, scenario := range []string{"changed-hash", "missing", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "web")
			fonts := filepath.Join(root, "public", "fonts")
			if err := os.MkdirAll(fonts, 0700); err != nil {
				t.Fatal(err)
			}
			entries := []string{"golos-text-og.ttf", "golos-text-latin.woff2", "golos-text-cyrillic.woff2", "OFL-Golos-Text.txt"}
			for _, name := range entries {
				b, err := os.ReadFile(filepath.Join(source, name))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(fonts, name), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "changed-hash":
				if err := os.WriteFile(filepath.Join(fonts, "golos-text-latin.woff2"), []byte("modified"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(filepath.Join(fonts, "OFL-Golos-Text.txt")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filepath.Join(fonts, "golos-text-og.ttf")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(source, "golos-text-og.ttf"), filepath.Join(fonts, "golos-text-og.ttf")); err != nil {
					t.Fatal(err)
				}
			}
			if err := Check(root); err == nil {
				t.Fatal("invalid font provenance accepted")
			}
		})
	}
}

func TestGlyphCoverageRejectsFontWithoutRussianCyrillic(t *testing.T) {
	f, err := sfnt.Parse(mustRead(t, filepath.Join("testdata", "missing-glyph.ttf")))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGlyphCoverage(f); err == nil {
		t.Fatal("Latin-only Go font passed Russian Cyrillic coverage")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../"))
}
