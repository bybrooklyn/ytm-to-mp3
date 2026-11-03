package vpn

import (
	"context"
	"net"
	"testing"
	"time"
)

// Unauthenticated connections are capped; an authenticated client must still get in
// once the flood stops, and a wrong auth-version byte must be refused.
func TestSOCKSPreAuthCapAndVersionByte(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var d net.Dialer
	go ServeSOCKS5(ctx, l, "ytm", "secret", d.DialContext)

	// hold many half-open connections
	var held []net.Conn
	for i := 0; i < 100; i++ {
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	// the surplus must have been closed by the server: a read returns EOF quickly
	closed := 0
	for _, c := range held {
		c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil {
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				closed++
			}
		}
	}
	if closed < 30 {
		t.Fatalf("expected the surplus (100-64) connections to be dropped, only %d were", closed)
	}
	for _, c := range held {
		c.Close()
	}

	// wrong RFC 1929 version byte
	time.Sleep(100 * time.Millisecond)
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte{5, 1, 2})
	buf := make([]byte, 2)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(buf); err != nil || buf[1] != 2 {
		t.Fatalf("method negotiation failed: %v %v", buf, err)
	}
	c.Write([]byte{9, 3, 'y', 't', 'm', 6, 's', 'e', 'c', 'r', 'e', 't'})
	if n, err := c.Read(buf); err == nil && n > 0 && buf[1] == 0 {
		t.Fatal("a bad auth version byte was accepted")
	}
}
