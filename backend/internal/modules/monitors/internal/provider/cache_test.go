package provider

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestAddressesRejectsMixedPrivateAnswersAndReturnsBoundedDeterministicSet(t *testing.T) {
	ctx := context.Background()
	answers := []netip.Addr{
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("1.1.1.2"),
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("9.9.9.9"),
		netip.MustParseAddr("208.67.222.222"),
	}
	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "status.example.test" {
			t.Fatalf("lookup args = (%q, %q)", network, host)
		}
		return append([]netip.Addr(nil), answers...), nil
	})
	got, err := Addresses(ctx, resolver, "status.example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("1.1.1.2"),
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("9.9.9.9"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Addresses() = %v, want sorted unique first four %v", got, want)
	}

	for _, private := range []string{"127.0.0.1", "10.0.0.1", "fc00::1", "169.254.1.1"} {
		t.Run("reject mixed "+private, func(t *testing.T) {
			mixed := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr(private)}, nil
			})
			if ips, err := Addresses(ctx, mixed, "status.example.test"); err == nil || ips != nil {
				t.Fatalf("mixed public/private DNS answer = (%v, %v), want rejection", ips, err)
			}
		})
	}
}

func TestDatasetParserRejectsUnorderedOverlappingAndMalformedRows(t *testing.T) {
	for name, rows := range map[string]string{
		"valid v4 and v6": "1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n2606:4700::\t2606:4700::ffff\t13335\tUS\tCLOUDFLARENET\n",
		"overlap":         "1.1.1.0\t1.1.1.255\t13335\tUS\tCloudflare\n1.1.1.128\t1.1.2.0\t13335\tUS\tCloudflare\n",
		"unordered":       "1.1.2.0\t1.1.2.255\t13335\tUS\tCloudflare\n1.1.1.0\t1.1.1.255\t13335\tUS\tCloudflare\n",
		"invalid country": "1.1.1.0\t1.1.1.255\t13335\tUSA\tCloudflare\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeGzipRows(t, rows)
			count, err := scanDataset(path, nil)
			if name == "valid v4 and v6" {
				if err != nil || count != 2 {
					t.Fatalf("scan valid fixture = (%d, %v), want 2 rows", count, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("scan accepted %s fixture with %d rows", name, count)
			}
		})
	}
}

func TestDisplayNameUsesExplicitProviderLabelsAndPreservesRawNames(t *testing.T) {
	for input, want := range map[string]string{
		"CLOUDFLARENET":     "Cloudflare",
		" Timeweb-AS  Ltd ": "Timeweb",
		"Example Broadband": "Example Broadband",
		"Cloudflareish LLC": "Cloudflareish LLC",
	} {
		if got := displayName(input); got != want {
			t.Errorf("displayName(%q) = %q, want %q", input, got, want)
		}
	}
}

func writeGzipRows(t *testing.T, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.tsv.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	if _, err = z.Write(bytes.TrimSpace([]byte(rows))); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
