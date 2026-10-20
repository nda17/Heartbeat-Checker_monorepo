package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"time"

	probe "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

const SourceURL = "https://iptoasn.com/data/ip2asn-combined.tsv.gz"

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Addresses rejects a mixed public/private DNS answer before choosing a bounded,
// deterministic subset. No target address is ever dialed for enrichment.
func Addresses(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	normalized, err := probe.NormalizeHost(host, false)
	if err != nil {
		return nil, err
	}
	host = normalized
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 || len(ips) > 256 {
		return nil, errors.New("invalid DNS answer count")
	}
	for i, ip := range ips {
		if !probe.PublicIP(ip) {
			return nil, errors.New("nonpublic DNS answer")
		}
		ips[i] = ip.Unmap()
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].Compare(ips[j]) < 0 })
	selected := make([]netip.Addr, 0, 4)
	for _, ip := range ips {
		if len(selected) == 0 || selected[len(selected)-1] != ip {
			selected = append(selected, ip)
			if len(selected) == 4 {
				break
			}
		}
	}
	return selected, nil
}

type Cache struct{ Directory string }

func (c Cache) Load() (*Dataset, error) {
	return loadDataset(filepath.Join(c.Directory, "ip2asn.tsv.gz"))
}

// Refresh verifies a complete candidate before atomically replacing the last
// known good compressed snapshot. An interrupted refresh cannot poison startup.
func (c Cache) Refresh(ctx context.Context) (*Dataset, error) {
	if err := os.MkdirAll(c.Directory, 0700); err != nil {
		return nil, err
	}
	ips, err := Addresses(ctx, net.DefaultResolver, "iptoasn.com")
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "iptoasn.com:443" {
				return nil, errors.New("unexpected ASN source host")
			}
			var last error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("ASN source redirect rejected") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxCompressed {
		return nil, errors.New("invalid ASN source response")
	}
	f, err := os.CreateTemp(c.Directory, ".asn-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxCompressed+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n > maxCompressed {
		return nil, errors.New("ASN source exceeds compressed limit")
	}
	// Source timestamp is retained when supplied; otherwise cache verification
	// time is explicit in the version and SHA identifies the exact content.
	date := time.Now().UTC()
	if value, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil && !value.After(date.Add(5*time.Minute)) {
		date = value
	}
	if err = os.Chtimes(path, date, date); err != nil {
		return nil, err
	}
	dataset, err := loadDataset(path)
	if err != nil {
		return nil, err
	}
	if err = os.Rename(path, filepath.Join(c.Directory, "ip2asn.tsv.gz")); err != nil {
		return nil, err
	}
	dir, err := os.Open(c.Directory)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	return dataset, nil
}

func (d *Dataset) Resolve(ips []netip.Addr) ([]string, []int64) {
	names := []string{}
	asns := []int64{}
	for _, ip := range ips {
		asn, name := d.Lookup(ip)
		if asn == 0 || name == "" {
			continue
		}
		found := false
		for _, value := range asns {
			if value == int64(asn) {
				found = true
			}
		}
		if !found {
			asns = append(asns, int64(asn))
		}
		found = false
		for _, value := range names {
			if value == name {
				found = true
			}
		}
		if !found && len(names) < 3 {
			names = append(names, name)
		}
	}
	return names, asns
}
