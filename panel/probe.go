package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"time"
)

// probeResult is what a latency test reports back to the dashboard.
type probeResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// probeTargetHost is a single fixed, well-known endpoint used for every
// probe — nothing user-supplied, so there's no way to point a probe at an
// arbitrary internal address.
const probeTargetHost = "example.com"

const probeTimeout = 10 * time.Second

// socks5Connect performs a minimal SOCKS5 CONNECT handshake (no auth, the
// only mode Psiphon's local proxy speaks) through the proxy at socksAddr to
// host:port, returning the established connection. Written by hand instead
// of pulling in a SOCKS client library — the CONNECT handshake is small
// enough that a dependency isn't worth it for three read-only probe modes.
func socks5Connect(ctx context.Context, socksAddr, host string, port int) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	greetResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, greetResp); err != nil {
		conn.Close()
		return nil, err
	}
	if greetResp[0] != 0x05 || greetResp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 proxy rejected the no-auth handshake")
	}

	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 CONNECT failed (code %d)", head[1])
	}
	var addrLen int
	switch head[3] {
	case 0x01:
		addrLen = 4
	case 0x04:
		addrLen = 16
	case 0x03:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			conn.Close()
			return nil, err
		}
		addrLen = int(lb[0])
	default:
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 proxy returned an unknown address type")
	}
	if _, err := io.ReadFull(conn, make([]byte, addrLen+2)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// probeTCP measures only the time to open a TCP connection through the
// tunnel to the probe target — the cheapest, fastest check, good for a
// quick "is this location's proxy alive at all" read.
func probeTCP(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 443)
	elapsed := time.Since(start)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	conn.Close()
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

// probeHTTP sends a real plain-HTTP GET through the tunnel and waits for a
// response, measuring the full request/response round trip (not just the
// connection setup).
func probeHTTP(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 80)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	req := "GET / HTTP/1.1\r\nHost: " + probeTargetHost + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return probeResult{Error: err.Error()}
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		return probeResult{Error: err.Error()}
	}
	elapsed := time.Since(start)
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

// probeReal measures the full cost of actually loading something over
// HTTPS through the tunnel — SOCKS handshake, TCP connect, and TLS
// handshake together — the closest of the three modes to what a real user
// would experience opening a page.
func probeReal(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 443)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: probeTargetHost})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return probeResult{Error: err.Error()}
	}
	req := "GET / HTTP/1.1\r\nHost: " + probeTargetHost + "\r\nConnection: close\r\n\r\n"
	if _, err := tlsConn.Write([]byte(req)); err != nil {
		return probeResult{Error: err.Error()}
	}
	if _, err := bufio.NewReader(tlsConn).ReadString('\n'); err != nil {
		return probeResult{Error: err.Error()}
	}
	elapsed := time.Since(start)
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

func runProbe(mode, socksAddr string) probeResult {
	switch mode {
	case "tcp":
		return probeTCP(socksAddr)
	case "http":
		return probeHTTP(socksAddr)
	case "real":
		return probeReal(socksAddr)
	default:
		return probeResult{Error: "unknown probe mode"}
	}
}
