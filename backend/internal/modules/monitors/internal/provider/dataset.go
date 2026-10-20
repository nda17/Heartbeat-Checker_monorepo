// Package provider resolves network/ASN ownership from a locally cached IPtoASN
// snapshot. It does not infer the physical location of a monitored service.
package provider

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCompressed   = 32 << 20
	maxUncompressed = 256 << 20
	maxRows         = 2_000_000
	maxNames        = 131_072
	maxNameBytes    = 8 << 20
	minSnapshotRows = 100_000
)

// Separate compact arrays avoid per-IP objects or repeated ASN descriptions.
type ipRange struct {
	start, end [16]byte
	asn        uint32
}

type ipv4Range struct{ start, end, asn uint32 }

type Dataset struct {
	v4      []ipv4Range
	v6      []ipRange
	names   map[uint32]string
	Version string
}

func (d *Dataset) Lookup(ip netip.Addr) (uint32, string) {
	ip = ip.Unmap()
	if ip.Is4() {
		raw := ip.As4()
		key := binary.BigEndian.Uint32(raw[:])
		i := sort.Search(len(d.v4), func(i int) bool { return d.v4[i].end >= key })
		if i == len(d.v4) || d.v4[i].start > key {
			return 0, ""
		}
		r := d.v4[i]
		return r.asn, d.names[r.asn]
	}
	key := ip.As16()
	i := sort.Search(len(d.v6), func(i int) bool { return bytes.Compare(d.v6[i].end[:], key[:]) >= 0 })
	if i == len(d.v6) || bytes.Compare(d.v6[i].start[:], key[:]) > 0 {
		return 0, ""
	}
	r := d.v6[i]
	return r.asn, d.names[r.asn]
}

func cleanName(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// Recognize only explicit source names; an arbitrary ASN/substring must never
// turn into a more familiar brand. Everything else keeps its source description.
func displayName(value string) string {
	value = cleanName(value)
	if value == "" {
		return ""
	}
	token := strings.ToUpper(strings.Fields(value)[0])
	switch token {
	case "CLOUDFLARENET", "CLOUDFLARE":
		return "Cloudflare"
	case "TIMEWEB", "TIMEWEB-AS", "TIMEWEB-CLOUD":
		return "Timeweb"
	}
	return value
}

func loadDataset(path string) (*Dataset, error) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxCompressed {
		return nil, errors.New("invalid ASN cache size")
	}
	// Validation/counting happens before allocation. The second pass allocates
	// exactly once; refreshing never grows a range slice alongside the old one.
	v4, v6 := 0, 0
	count, err := scanDataset(path, func(r ipRange, _ string) error {
		if netip.AddrFrom16(r.start).Is4In6() {
			v4++
		} else {
			v6++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if count < minSnapshotRows {
		return nil, errors.New("incomplete ASN snapshot")
	}
	d := &Dataset{v4: make([]ipv4Range, 0, v4), v6: make([]ipRange, 0, v6), names: make(map[uint32]string)}
	nameBytes := 0
	_, err = scanDataset(path, func(r ipRange, name string) error {
		if netip.AddrFrom16(r.start).Is4In6() {
			d.v4 = append(d.v4, ipv4Range{binary.BigEndian.Uint32(r.start[12:]), binary.BigEndian.Uint32(r.end[12:]), r.asn})
		} else {
			d.v6 = append(d.v6, r)
		}
		if r.asn != 0 {
			if _, exists := d.names[r.asn]; !exists {
				name = displayName(name)
				nameBytes += len(name)
				if len(d.names) >= maxNames || nameBytes > maxNameBytes {
					return errors.New("ASN descriptions exceed limit")
				}
				d.names[r.asn] = name
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return nil, err
	}
	d.Version = "iptoasn:" + info.ModTime().UTC().Format("2006-01-02T15:04:05Z") + ":sha256:" + hex.EncodeToString(hash.Sum(nil))
	return d, nil
}

func scanDataset(path string, consume func(ipRange, string) error) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: maxUncompressed + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1024)
	var previous [2][16]byte
	var seen [2]bool
	count := 0
	for scanner.Scan() {
		fields := bytes.Split(scanner.Bytes(), []byte{'\t'})
		if len(fields) != 5 || len(fields[4]) > 256 || !utf8.Valid(fields[4]) || (len(fields[3]) != 2 && string(fields[3]) != "None" && string(fields[3]) != "Unknown") {
			return 0, errors.New("invalid ASN TSV row")
		}
		start, e1 := netip.ParseAddr(string(fields[0]))
		end, e2 := netip.ParseAddr(string(fields[1]))
		asn, e3 := strconv.ParseUint(string(fields[2]), 10, 32)
		if e1 != nil || e2 != nil || e3 != nil || start.Zone() != "" || end.Zone() != "" || start.Is4() != end.Is4() || start.Compare(end) > 0 {
			return 0, errors.New("invalid ASN range")
		}
		r := ipRange{start: start.As16(), end: end.As16(), asn: uint32(asn)}
		family := 0
		if start.Is6() {
			family = 1
		}
		if seen[family] && bytes.Compare(previous[family][:], r.start[:]) >= 0 {
			return 0, errors.New("unordered or overlapping ASN ranges")
		}
		previous[family] = r.end
		seen[family] = true
		count++
		if count > maxRows {
			return 0, errors.New("too many ASN ranges")
		}
		if asn != 0 && cleanName(string(fields[4])) == "" {
			return 0, errors.New("empty ASN name")
		}
		if consume != nil {
			if err := consume(r, string(fields[4])); err != nil {
				return 0, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read ASN snapshot: %w", err)
	}
	if limited.N <= 0 {
		return 0, errors.New("ASN snapshot exceeds decompressed limit")
	}
	return count, nil
}
