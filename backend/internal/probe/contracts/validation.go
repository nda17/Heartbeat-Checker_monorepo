package contracts

import (
	"errors"
	"net/netip"
	"strings"
)

func EffectiveCheckType(value string) string {
	if value == "" {
		return "http"
	}
	return value
}

// PublicIP excludes private, special-purpose and documentation destinations.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip)) {
		return false
	}
	for _, prefix := range excludedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var excludedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"), netip.MustParsePrefix("3fff::/20"),
}

// NormalizeHost accepts a plain public IP or ASCII DNS name, never a URL or port.
func NormalizeHost(raw string, ipv4Only bool) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	invalid := errors.New("invalid host")
	if host == "" || len(host) > 253 || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", invalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicIP(ip) || (ipv4Only && !ip.Is4()) {
			return "", invalid
		}
		return ip.Unmap().String(), nil
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", invalid
			}
		}
	}
	return host, nil
}

// ValidResult fences protocol-specific measurements at both transport and storage.
func ValidResult(req Request, result Result) bool {
	typ := EffectiveCheckType(req.CheckType)
	if EffectiveCheckType(result.CheckType) != typ || (typ != "http" && typ != "tcp" && typ != "icmp") {
		return false
	}
	if result.HTTPStatus < 0 || result.HTTPStatus > 599 || (result.HTTPStatus > 0 && result.HTTPStatus < 100) || (typ != "http" && result.HTTPStatus != 0) {
		return false
	}
	if result.RoundTripMS != nil && (typ != "icmp" || result.Outcome != "UP" || *result.RoundTripMS < 0 || *result.RoundTripMS > result.LatencyMS) {
		return false
	}
	if result.Outcome == "UP" && (result.ErrorCode != "" || (typ == "http" && result.HTTPStatus != req.ExpectedStatus) || (typ == "icmp" && result.RoundTripMS == nil)) {
		return false
	}
	switch result.ErrorCode {
	case "":
		return result.Outcome == "UP"
	case "check_cancelled", "probe_unavailable":
		return result.Outcome == "UNKNOWN"
	case "address_family_unavailable":
		return typ != "http" && result.Outcome == "UNKNOWN"
	case "dns_failed":
		return typ != "http" && result.Outcome == "DOWN"
	case "tcp_connection_refused", "tcp_timeout", "tcp_failed":
		return typ == "tcp" && result.Outcome == "DOWN"
	case "icmp_timeout", "icmp_failed":
		return typ == "icmp" && result.Outcome == "DOWN"
	case "request_failed", "request_timeout", "tls_certificate_invalid", "tls_handshake_failed", "redirect_limit", "invalid_redirect", "response_too_large", "unexpected_status", "body_mismatch":
		return typ == "http" && result.Outcome == "DOWN"
	default:
		return false
	}
}
