package gateway

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SOCKS5Dialer dials through the SOCKS5 server at proxyAddr (tailscaled's
// --socks5-server), passing the target by NAME so the server resolves
// MagicDNS itself; the gateway's own resolver knows nothing of the tailnet.
// Only no-auth CONNECT is spoken, which is all tailscaled offers.
func SOCKS5Dialer(proxyAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || len(host) > 255 {
			return nil, fmt.Errorf("socks5: bad target %q", addr)
		}
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		if dl, ok := ctx.Deadline(); ok {
			c.SetDeadline(dl)
		}
		fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
		if _, err := c.Write([]byte{5, 1, 0}); err != nil {
			return fail(err)
		}
		hello := make([]byte, 2)
		if _, err := io.ReadFull(c, hello); err != nil || hello[0] != 5 || hello[1] != 0 {
			return fail(fmt.Errorf("socks5: %s refused no-auth", proxyAddr))
		}
		req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
		req = binary.BigEndian.AppendUint16(req, uint16(port))
		if _, err := c.Write(req); err != nil {
			return fail(err)
		}
		rep := make([]byte, 4)
		if _, err := io.ReadFull(c, rep); err != nil {
			return fail(err)
		}
		if rep[1] != 0 {
			return fail(fmt.Errorf("socks5: connect to %s failed (code %d)", addr, rep[1]))
		}
		skip := 0
		switch rep[3] {
		case 1:
			skip = 4
		case 4:
			skip = 16
		case 3:
			n := make([]byte, 1)
			if _, err := io.ReadFull(c, n); err != nil {
				return fail(err)
			}
			skip = int(n[0])
		}
		if _, err := io.ReadFull(c, make([]byte, skip+2)); err != nil {
			return fail(err)
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
}
