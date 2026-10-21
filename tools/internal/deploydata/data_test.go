package deploydata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestObjectRejectsDuplicateKeysAtEveryDepthAndKeepsNumbersExact(t *testing.T) {
	for _, raw := range []string{
		`{"key":1,"key":2}`,
		`{"outer":{"key":1,"key":2}}`,
		`{"array":[{"key":1,"key":2}]}`,
		`{"value":1} trailing`,
	} {
		if _, err := Object([]byte(raw)); err == nil {
			t.Fatalf("Object accepted invalid JSON: %s", raw)
		}
	}
	value, err := Object([]byte(`{"whole":9007199254740993,"fraction":1.25}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := value["whole"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("integer lost precision: %s", got)
	}
	if got := value["fraction"].(json.Number).String(); got != "1.25" {
		t.Fatalf("fraction changed: %s", got)
	}
}

func TestReadBoundedRejectsDataPastLimit(t *testing.T) {
	if got, err := ReadBounded(strings.NewReader("1234"), 4); err != nil || string(got) != "1234" {
		t.Fatalf("exact boundary rejected: %q, %v", got, err)
	}
	if _, err := ReadBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestPrivateReadRejectsSymlinksAndRequiresMode0600(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "candidate")
	if err := os.WriteFile(regular, []byte("private=$literal"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrivate(regular, 100)
	if err != nil || string(got) != "private=$literal" {
		t.Fatalf("private value changed: %q, %v", got, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(link, 100); err == nil {
		t.Fatal("symlink candidate accepted")
	}
	if err := os.Chmod(regular, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(regular, 100); err == nil {
		t.Fatal("candidate with broad permissions accepted")
	}
}

func TestPrivatePathsRejectSpecialPermissionBits(t *testing.T) {
	dir := t.TempDir()
	privateDir := filepath.Join(dir, "private")
	if err := os.Mkdir(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateDir, 01700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky != 0 {
		if err := PrivateDirectory(privateDir); err == nil {
			t.Fatal("sticky private directory accepted")
		}
	}
	for _, mode := range []os.FileMode{04600, 02600} {
		path := filepath.Join(dir, mode.String())
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		flag := os.ModeSetuid
		if mode == 02600 {
			flag = os.ModeSetgid
		}
		if info.Mode()&flag != 0 {
			if _, err := ReadPrivate(path, 100); err == nil {
				t.Fatalf("special-permission file accepted with mode %v", mode)
			}
		}
	}
}

func TestEnvSeparatesAllowlistedValuesWithoutExpandingLiteralDollars(t *testing.T) {
	input := []byte("# keep\nBACKUP_S3_SECRET_ACCESS_KEY=secret=$HOME;${NAME};$$\nDATABASE_URL=postgres://fixture\n")
	ordinary, values, err := Env(input, map[string]bool{"BACKUP_S3_SECRET_ACCESS_KEY": true})
	if err != nil {
		t.Fatal(err)
	}
	if values["BACKUP_S3_SECRET_ACCESS_KEY"] != "secret=$HOME;${NAME};$$" {
		t.Fatalf("literal secret changed: %q", values["BACKUP_S3_SECRET_ACCESS_KEY"])
	}
	if !reflect.DeepEqual(ordinary, [][]byte{[]byte("# keep"), []byte("DATABASE_URL=postgres://fixture"), []byte("")}) {
		t.Fatalf("unexpected retained env lines: %#v", ordinary)
	}
	for _, duplicate := range [][]byte{[]byte("KEY=one\nKEY=two\n"), []byte("BACKUP_S3_BUCKET=x\nBACKUP_S3_BUCKET=y\n")} {
		if _, _, err := Env(duplicate, map[string]bool{"KEY": true}); err == nil {
			t.Fatal("duplicate environment key accepted")
		}
	}
}
