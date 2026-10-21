// Package deploydata contains bounded, secret-safe deployment file primitives.
package deploydata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"unicode/utf8"
)

var ErrInvalid = errors.New("deployment input validation failed")
var EnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const OwnerID = "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"

func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrInvalid
	}
	return b, nil
}

// Object rejects duplicate keys at every depth and keeps JSON numbers exact.
func Object(b []byte) (map[string]any, error) {
	if !utf8.Valid(b) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := decode(d, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	return m, nil
}

func decode(d *json.Decoder, depth int) (any, error) {
	if depth > 100 {
		return nil, ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, ok := key.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := m[k]; exists {
				return nil, ErrInvalid
			}
			v, err := decode(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return m, nil
	case json.Delim('['):
		var a []any
		for d.More() {
			v, err := decode(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return a, nil
	case json.Delim('}'), json.Delim(']'):
		return nil, ErrInvalid
	default:
		return t, nil
	}
}

func Exact(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
func String(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func ASCII(b []byte) bool {
	for _, c := range b {
		if c > 127 {
			return false
		}
	}
	return true
}
func Printable(s string, min, max int, forbidden string) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range []byte(s) {
		if c < 33 || c > 126 || bytes.IndexByte([]byte(forbidden), c) >= 0 {
			return false
		}
	}
	return true
}
func PrivateDirectory(path string) error {
	i, err := os.Lstat(path)
	if err != nil || !i.IsDir() || i.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0700 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return nil
}
func ReadPrivate(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0600 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, ErrInvalid
	}
	return ReadBounded(f, limit)
}
func WriteExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrInvalid
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrInvalid
	}
	return nil
}

// Env separates mutable keys without interpreting quotes, dollar signs or escapes.
func Env(data []byte, selected map[string]bool) ([][]byte, map[string]string, error) {
	for _, c := range data {
		if c < 32 && c != '\n' || c == 127 {
			return nil, nil, ErrInvalid
		}
	}
	var ordinary [][]byte
	values := map[string]string{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		trimmed := bytes.Trim(line, " \t\r\n\v\f")
		if len(trimmed) == 0 || trimmed[0] == '#' {
			ordinary = append(ordinary, line)
			continue
		}
		key, value, ok := bytes.Cut(line, []byte{'='})
		if !ok || !EnvKey.Match(key) {
			return nil, nil, ErrInvalid
		}
		k := string(key)
		if _, exists := values[k]; exists {
			return nil, nil, ErrInvalid
		}
		values[k] = string(value)
		if !selected[k] {
			ordinary = append(ordinary, line)
		}
	}
	return ordinary, values, nil
}
func Unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// Candidate replaces only selected keys; every other byte stays in its original order.
func Candidate(workdir string, changed [][2]string) error {
	if PrivateDirectory(workdir) != nil {
		return ErrInvalid
	}
	data, err := ReadPrivate(".env", 256<<10)
	if err != nil {
		return ErrInvalid
	}
	selected := map[string]bool{}
	for _, pair := range changed {
		selected[pair[0]] = true
	}
	untouched, _, err := Env(data, selected)
	if err != nil {
		return ErrInvalid
	}
	lines := make([][]byte, 0, len(changed)+len(untouched))
	for _, pair := range changed {
		lines = append(lines, []byte(pair[0]+"="+pair[1]))
	}
	lines = append(lines, untouched...)
	return WriteExclusive(filepath.Join(workdir, ".env.next"), bytes.Join(lines, []byte{'\n'}))
}
