package vpn

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

// ServeSOCKS5 runs a CONNECT-only SOCKS5 server that requires user/password auth.
// One runs on loopback in front of each VPN tunnel so yt-dlp and Go share the exit.
// dial opens the outbound connection (through a tunnel).
func ServeSOCKS5(ctx context.Context, l net.Listener, user, pass string, dial Dialer) error {
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer c.Close()
			_ = handleSOCKS(ctx, c, user, pass, dial)
		}()
	}
}

func handleSOCKS(ctx context.Context, c net.Conn, user, pass string, dial Dialer) error {
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	br := bufio.NewReader(c)
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil || h[0] != 5 {
		return errors.New("not socks5")
	}
	methods := make([]byte, h[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return err
	}
	has := false
	for _, m := range methods {
		has = has || m == 2
	}
	if !has {
		c.Write([]byte{5, 0xff})
		return errors.New("no auth method")
	}
	c.Write([]byte{5, 2})
	// RFC 1929 sub-negotiation
	var v [2]byte
	if _, err := io.ReadFull(br, v[:1]); err != nil {
		return err
	}
	rd := func() (string, error) {
		var n [1]byte
		if _, err := io.ReadFull(br, n[:]); err != nil {
			return "", err
		}
		b := make([]byte, n[0])
		_, err := io.ReadFull(br, b)
		return string(b), err
	}
	u, err := rd()
	if err != nil {
		return err
	}
	p, err := rd()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
		c.Write([]byte{1, 1})
		return errors.New("bad credentials")
	}
	c.Write([]byte{1, 0})

	var rq [4]byte
	if _, err := io.ReadFull(br, rq[:]); err != nil {
		return err
	}
	if rq[1] != 1 {
		reply(c, 7)
		return errors.New("only CONNECT")
	}
	var host string
	switch rq[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return err
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return err
		}
		host = net.IP(b).String()
	case 3:
		h, err := rd()
		if err != nil {
			return err
		}
		host = h
	default:
		reply(c, 8)
		return errors.New("bad address type")
	}
	var pb [2]byte
	if _, err := io.ReadFull(br, pb[:]); err != nil {
		return err
	}
	port := int(pb[0])<<8 | int(pb[1])
	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	defer dcancel()
	up, err := dial(dctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		code := byte(5) // connection refused
		if dctx.Err() != nil {
			code = 6 // TTL expired / timed out
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			code = 4 // host unreachable
		}
		reply(c, code)
		return err
	}
	defer up.Close()
	reply(c, 0)
	_ = c.SetDeadline(time.Time{})
	// When the tunnel is closed, drop live connections too so clients fail fast (and the
	// exit can be replaced) instead of hanging on a dead path.
	stop := context.AfterFunc(ctx, func() { c.Close(); up.Close() })
	defer stop()
	cc, uc := &idleConn{Conn: c}, &idleConn{Conn: up}
	// bytes the client already sent along with its request sit in br; send them first
	pre, _ := br.Peek(br.Buffered())
	src := io.MultiReader(bytes.NewReader(append([]byte(nil), pre...)), cc)
	done := make(chan struct{}, 2)
	go func() { io.Copy(uc, src); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(cc, uc); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
	return nil
}

// idleConn gives a connection a rolling idle deadline: no bytes either way for this long
// means the path is dead (or the peer is wedged).
type idleConn struct{ net.Conn }

const idleLimit = 2 * time.Minute

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(idleLimit))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(idleLimit))
	return c.Conn.Write(p)
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}

func reply(c net.Conn, code byte) {
	c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}

// Dialer opens outbound connections.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
