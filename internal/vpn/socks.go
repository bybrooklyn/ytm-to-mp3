package vpn

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

// ServeSOCKS5 runs a CONNECT-only SOCKS5 server that requires user/password auth.
// It is what runs inside each VPN namespace.
func ServeSOCKS5(ctx context.Context, l net.Listener, user, pass string) error {
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
			_ = handleSOCKS(ctx, c, user, pass)
		}()
	}
}

func handleSOCKS(ctx context.Context, c net.Conn, user, pass string) error {
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
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		io.ReadFull(br, b)
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
	d := net.Dialer{Timeout: 15 * time.Second}
	up, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		reply(c, 5)
		return err
	}
	defer up.Close()
	reply(c, 0)
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, br); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
	return nil
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}

func reply(c net.Conn, code byte) {
	c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}

// Relay accepts TCP connections on 127.0.0.1 and pipes them to a unix socket.
// It bridges the host (where yt-dlp and ytm live) to the SOCKS server in a namespace;
// the SOCKS server's password is what keeps other local users out.
func Relay(ctx context.Context, sock string) (addr string, err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() { <-ctx.Done(); l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("unix", sock)
				if err != nil {
					return
				}
				defer u.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(u, c); closeWrite(u); done <- struct{}{} }()
				go func() { io.Copy(c, u); closeWrite(c); done <- struct{}{} }()
				<-done
				<-done
			}()
		}
	}()
	return l.Addr().String(), nil
}
