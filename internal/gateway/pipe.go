package gateway

import (
	"context"
	"io"
	"net"
)

// Pipe connects in and out to addr through dial, which is what ssh needs from
// a ProxyCommand: `megh gw nc` runs it inside the gateway container so an ssh
// on the host reaches a worker over the gateway's tailnet identity. When in
// ends, the write side is half-closed so the worker sees EOF; Pipe returns
// once the worker closes its side.
func Pipe(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), addr string, in io.Reader, out io.Writer) error {
	c, err := dial(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() {
		io.Copy(c, in)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	_, err = io.Copy(out, c)
	return err
}
