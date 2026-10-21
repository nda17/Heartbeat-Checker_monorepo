// Package fontcheck verifies the committed OG font and its source provenance.
// It does not convert WOFF2 or regenerate font artifacts.
package fontcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font/sfnt"
)

const TTFSHA256 = "c6fd6051dd3675fdf8a5473a588c967f4b7994f0e2dce6c4d641f4661312c026"
const LatinSHA256 = "f8d71091110f0113ba8bd16bee2ac68b51dd6491c5b9404e9fbb82b20ae632f1"
const CyrillicSHA256 = "2f175b8fc40ed4e826742879f92ec903256fc2612d8d95c7c287232fd7138e4b"
const LicenseSHA256 = "ff532f9e8789f09a9fdffc3c0954eedfb0a48be77b2e2eb90f5f82e4f347f50c"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("font-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("web-root", "", "web application root (default: discover apps/web)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "font-check accepts flags only")
		return 2
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "OG font verification failed: cancelled")
		return 1
	}
	if err := Check(*root); err != nil {
		fmt.Fprintf(stderr, "OG font verification failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "OG font verification passed: committed TTF, source hashes, OFL and Latin/Cyrillic glyph coverage.")
	return 0
}
func discoverWebRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		for _, candidate := range []string{filepath.Join(directory, "apps", "web"), directory} {
			if info, err := os.Stat(filepath.Join(candidate, "public", "fonts", "golos-text-og.ttf")); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("web root not found")
		}
		directory = parent
	}
}
func Check(root string) error {
	if root == "" {
		var err error
		root, err = discoverWebRoot()
		if err != nil {
			return err
		}
	}
	fonts := filepath.Join(root, "public", "fonts")
	files := []struct{ name, hash string }{{"golos-text-og.ttf", TTFSHA256}, {"golos-text-latin.woff2", LatinSHA256}, {"golos-text-cyrillic.woff2", CyrillicSHA256}, {"OFL-Golos-Text.txt", LicenseSHA256}}
	var ttf, license []byte
	for _, file := range files {
		path := filepath.Join(fonts, file.name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing regular font provenance file: %s", file.name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read font provenance file: %s", file.name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != file.hash {
			return fmt.Errorf("font provenance checksum mismatch: %s", file.name)
		}
		switch file.name {
		case "golos-text-og.ttf":
			ttf = data
		case "OFL-Golos-Text.txt":
			license = data
		}
	}
	if !strings.Contains(string(license), "Copyright 2019 The Golos Text Project Authors") || !strings.Contains(string(license), "SIL OPEN FONT LICENSE Version 1.1") {
		return errors.New("Golos Text OFL provenance is missing")
	}
	font, err := sfnt.Parse(ttf)
	if err != nil {
		return errors.New("committed OG font is not valid SFNT")
	}
	return CheckGlyphCoverage(font)
}

// CheckGlyphCoverage requires printable ASCII Latin and the Russian Cyrillic
// alphabet, including both forms of Yo. Missing characters cannot pass as .notdef.
func CheckGlyphCoverage(font *sfnt.Font) error {
	var buffer sfnt.Buffer
	for _, span := range []struct{ first, last rune }{{0x20, 0x7e}, {0x410, 0x44f}, {0x401, 0x401}, {0x451, 0x451}} {
		for character := span.first; character <= span.last; character++ {
			index, err := font.GlyphIndex(&buffer, character)
			if err != nil || index == 0 {
				return fmt.Errorf("OG font is missing glyph U+%04X", character)
			}
		}
	}
	return nil
}
