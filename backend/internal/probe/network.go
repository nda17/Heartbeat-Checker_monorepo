package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func (e *Executor) checkNetwork(ctx context.Context, req contracts.Request, result contracts.Result, started time.Time) (contracts.Result, error) {
	host, _ := contracts.NormalizeHost(req.Host, true)
	release, err := e.acquire("host:" + host)
	if err != nil {
		return result, err
	}
	defer release()
	ips, err := e.resolve(ctx, host)
	if err != nil {
		result.LatencyMS = time.Since(started).Milliseconds()
		if errors.Is(err, ErrUnsupportedTarget) {
			return result, err
		}
		result.Outcome, result.ErrorCode = networkFailure(ctx, req.CheckType, err, true)
		return result, nil
	}
	seen := make(map[netip.Addr]bool)
	var lastErr error
	saturated := false
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.Is4() || seen[ip] {
			continue
		}
		if len(seen) == 4 {
			break
		}
		seen[ip] = true
		releaseIP, err := e.acquire("ip:" + ip.String())
		if errors.Is(err, ErrSaturated) {
			saturated = true
			continue
		}
		if err != nil {
			return result, err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var rtt int64
		if req.CheckType == "tcp" {
			var conn net.Conn
			conn, err = e.dial(attemptCtx, "tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(req.Port)))
			if conn != nil {
				_ = conn.Close()
			}
		} else {
			rtt, err = e.ping(attemptCtx, ip)
		}
		cancel()
		releaseIP()
		result.LatencyMS = time.Since(started).Milliseconds()
		if err == nil {
			result.Outcome = "UP"
			if req.CheckType == "icmp" {
				result.RoundTripMS = &rtt
			}
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil || resourceFailure(err) || socketUnavailable(err) {
			break
		}
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	if saturated {
		return result, ErrSaturated
	}
	if len(seen) == 0 {
		result.ErrorCode = "address_family_unavailable"
		return result, nil
	}
	result.Outcome, result.ErrorCode = networkFailure(ctx, req.CheckType, lastErr, false)
	return result, nil
}

var errICMPUnavailable = errors.New("ICMP socket unavailable")

func socketUnavailable(err error) bool {
	return errors.Is(err, errICMPUnavailable) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT)
}

func networkFailure(ctx context.Context, typ string, err error, dns bool) (string, string) {
	if resourceFailure(err) || socketUnavailable(err) {
		return "UNKNOWN", "probe_unavailable"
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "UNKNOWN", "check_cancelled"
	}
	if dns {
		return "DOWN", "dns_failed"
	}
	var netErr net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "DOWN", typ + "_timeout"
	}
	if typ == "tcp" && errors.Is(err, syscall.ECONNREFUSED) {
		return "DOWN", "tcp_connection_refused"
	}
	return "DOWN", typ + "_failed"
}

// pingIPv4 uses the kernel's unprivileged datagram ping socket. It never opens
// raw sockets or falls back to a privileged command. Linux permits GID 65532 via
// the probe container's net.ipv4.ping_group_range sysctl.
func pingIPv4(ctx context.Context, target netip.Addr) (int64, error) {
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return 0, errors.Join(errICMPUnavailable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return 0, err
		}
	}
	payload := make([]byte, 32)
	if _, err = rand.Read(payload); err != nil {
		return 0, errors.Join(errICMPUnavailable, err)
	}
	message := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: 1, Seq: 1, Data: payload}}
	packet, err := message.Marshal(nil)
	if err != nil {
		return 0, err
	}
	started := time.Now()
	if _, err = conn.WriteTo(packet, &net.UDPAddr{IP: net.IP(target.AsSlice())}); err != nil {
		return 0, err
	}
	buffer := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, err
		}
		address, ok := peer.(*net.UDPAddr)
		if !ok || !address.IP.Equal(net.IP(target.AsSlice())) {
			continue
		}
		reply, err := icmp.ParseMessage(1, buffer[:n])
		if err != nil || reply.Type != ipv4.ICMPTypeEchoReply || reply.Code != 0 {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		// Linux replaces the echo ID with the socket ID. A per-attempt random
		// payload, sequence and pinned peer correlate replies on both platforms.
		if !ok || echo.Seq != 1 || !bytes.Equal(echo.Data, payload) {
			continue
		}
		return time.Since(started).Milliseconds(), nil
	}
}
