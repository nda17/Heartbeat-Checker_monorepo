package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestLiveASNCacheRefresh(t *testing.T) {
	if os.Getenv("HEARTBEAT_LIVE_ASN") != "1" {
		t.Skip("live ASN verification is opt-in; CI enables HEARTBEAT_LIVE_ASN=1")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	cache := Cache{Directory: t.TempDir()}
	refreshed, err := cache.Refresh(ctx)
	if err != nil {
		t.Fatalf("live ASN refresh failed: %v", err)
	}
	loaded, err := cache.Load()
	if err != nil {
		t.Fatalf("refreshed ASN cache could not be reloaded: %v", err)
	}
	if loaded.Version == "" || loaded.Version != refreshed.Version {
		t.Fatal("reloaded ASN version differs from refreshed snapshot (ASN_VERSION)")
	}
	path := cache.Directory + string(os.PathSeparator) + "ip2asn.tsv.gz"
	compressed, err := os.ReadFile(path)
	if err != nil || len(compressed) == 0 {
		t.Fatal("refreshed ASN cache file is unavailable (ASN_CACHE_FILE)")
	}
	hash := sha256.Sum256(compressed)
	sha := hex.EncodeToString(hash[:])
	if !regexp.MustCompile(`:sha256:[0-9a-f]{64}$`).MatchString(loaded.Version) || loaded.Version[len(loaded.Version)-64:] != sha {
		t.Fatal("reloaded ASN version does not identify cached compressed bytes (ASN_HASH)")
	}
	if len(loaded.v4) == 0 || len(loaded.v6) == 0 {
		t.Fatal("live ASN snapshot is missing an address family (ASN_FAMILIES)")
	}
	checks := []struct {
		IP   string `json:"ip"`
		ASN  uint32 `json:"asn"`
		Name string `json:"name"`
	}{
		{IP: "1.1.1.1"},
		{IP: "2606:4700:4700::1111"},
	}
	for i := range checks {
		ip := netip.MustParseAddr(checks[i].IP)
		asn, name := loaded.Lookup(ip)
		if asn != 13335 || name == "" {
			t.Fatal("Cloudflare live lookup failed (ASN_LOOKUP)")
		}
		checks[i].ASN, checks[i].Name = asn, name
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(os.Getenv("GITHUB_SHA")) {
		t.Fatal("live ASN receipt requires a full source commit SHA (ASN_SOURCE_SHA)")
	}
	receiptPath := os.Getenv("HEARTBEAT_ASN_RECEIPT")
	if receiptPath == "" {
		t.Fatal("live ASN receipt path is not configured (ASN_RECEIPT_PATH)")
	}
	receipt := struct {
		SourceURL string `json:"sourceUrl"`
		Version   string `json:"version"`
		SHA256    string `json:"sha256"`
		Size      int    `json:"compressedBytes"`
		Rows      struct {
			IPv4  int `json:"ipv4"`
			IPv6  int `json:"ipv6"`
			Total int `json:"total"`
		} `json:"rows"`
		ElapsedMS int64 `json:"elapsedMs"`
		Lookups   []struct {
			IP   string `json:"ip"`
			ASN  uint32 `json:"asn"`
			Name string `json:"name"`
		} `json:"lookups"`
		SourceSHA string `json:"sourceSha"`
	}{
		SourceURL: SourceURL, Version: loaded.Version, SHA256: sha, Size: len(compressed),
		ElapsedMS: time.Since(started).Milliseconds(), Lookups: checks, SourceSHA: os.Getenv("GITHUB_SHA"),
	}
	receipt.Rows.IPv4, receipt.Rows.IPv6 = len(loaded.v4), len(loaded.v6)
	receipt.Rows.Total = receipt.Rows.IPv4 + receipt.Rows.IPv6
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal("could not encode ASN verification receipt (ASN_RECEIPT_ENCODE)")
	}
	if err := os.WriteFile(receiptPath, encoded, 0600); err != nil {
		t.Fatal("could not write ASN verification receipt (ASN_RECEIPT_WRITE)")
	}
}
