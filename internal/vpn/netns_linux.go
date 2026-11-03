//go:build linux

package vpn

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Pool is a set of live WireGuard exits, each in its own network namespace.
type Pool struct {
	Exits  []Exit
	cancel context.CancelFunc
	tunnel []*tunnel
	dir    string
}

type tunnel struct {
	ns, ifc string
	cmd     *exec.Cmd
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// priv runs a command as root (directly if we already are, else through sudo).
func priv(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	if os.Geteuid() != 0 {
		args = append([]string{"sudo"}, args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	} else if os.Geteuid() != 0 {
		cmd.Stdin = os.Stdin // lets sudo ask for a password
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out, nil
}

func need(bins ...string) error {
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			return fmt.Errorf("%s not found; install wireguard-tools / iproute2 / util-linux", b)
		}
	}
	return nil
}

// Up brings up one namespace + tunnel + proxy per config. Configs that fail are skipped
// with a warning; it errors only if none work.
func Up(ctx context.Context, files []string, logf func(string, ...any)) (*Pool, error) {
	if err := need("ip", "wg", "setpriv"); err != nil {
		return nil, err
	}
	if _, err := priv(ctx, "", "true"); err != nil {
		return nil, fmt.Errorf("--vpn needs root to create network namespaces: %w", err)
	}
	Clean(func(string, ...any) {})
	dir, err := os.MkdirTemp("", "ytm-vpn-")
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(context.Background())
	p := &Pool{cancel: cancel, dir: dir}
	for i, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		t, ex, err := p.bring(ctx, pctx, i, name, f)
		if t != nil {
			p.tunnel = append(p.tunnel, t)
		}
		if err != nil {
			logf("  ✗ %s: %v", name, err)
			continue
		}
		logf("  ✓ %s up", name)
		p.Exits = append(p.Exits, ex)
	}
	if len(p.Exits) == 0 {
		p.Close()
		return nil, errors.New("no VPN tunnel came up")
	}
	return p, nil
}

func (p *Pool) bring(ctx, pctx context.Context, i int, name, file string) (*tunnel, Exit, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, Exit{}, err
	}
	w, err := ParseWG(string(b))
	if err != nil {
		return nil, Exit{}, err
	}
	pid := os.Getpid()
	t := &tunnel{ns: fmt.Sprintf("ytm-%d-%d", pid, i), ifc: fmt.Sprintf("yw%x_%d", pid, i)}
	if len(t.ifc) > 15 {
		t.ifc = t.ifc[:15]
	}
	run := func(stdin string, a ...string) error { _, err := priv(ctx, stdin, a...); return err }

	steps := [][]string{
		{"ip", "netns", "add", t.ns},
		{"ip", "link", "add", t.ifc, "type", "wireguard"},
		{"ip", "link", "set", t.ifc, "netns", t.ns},
	}
	for _, s := range steps {
		if err := run("", s...); err != nil {
			return t, Exit{}, err
		}
	}
	if err := run(w.SetConf(), "ip", "netns", "exec", t.ns, "wg", "setconf", t.ifc, "/dev/stdin"); err != nil {
		return t, Exit{}, err
	}
	for _, a := range w.Addresses {
		if err := run("", "ip", "-n", t.ns, "addr", "add", a.String(), "dev", t.ifc); err != nil {
			return t, Exit{}, err
		}
	}
	mtu := w.MTU
	if mtu == 0 {
		mtu = 1380
	}
	for _, s := range [][]string{
		{"ip", "-n", t.ns, "link", "set", "lo", "up"},
		{"ip", "-n", t.ns, "link", "set", t.ifc, "mtu", strconv.Itoa(mtu), "up"},
		{"ip", "-n", t.ns, "route", "add", "default", "dev", t.ifc},
	} {
		if err := run("", s...); err != nil {
			return t, Exit{}, err
		}
	}
	if w.HasV6() { // otherwise IPv6 has no route at all, which is what we want
		_ = run("", "ip", "-n", t.ns, "-6", "route", "add", "default", "dev", t.ifc)
	}

	// DNS inside the namespace goes through the tunnel; the host's resolver is hidden.
	dns := w.DNS
	if len(dns) == 0 {
		dns = []string{"1.1.1.1"}
	}
	var rc strings.Builder
	for _, d := range dns {
		rc.WriteString("nameserver " + d + "\n")
	}
	nsd := "/etc/netns/" + t.ns
	if err := run("", "mkdir", "-p", nsd); err != nil {
		return t, Exit{}, err
	}
	if err := run(rc.String(), "tee", nsd+"/resolv.conf"); err != nil {
		return t, Exit{}, err
	}
	if nss, err := os.ReadFile("/etc/nsswitch.conf"); err == nil {
		fixed := regexp.MustCompile(`(?m)^hosts:.*$`).ReplaceAllString(string(nss), "hosts: files dns")
		_ = run(fixed, "tee", nsd+"/nsswitch.conf")
	}

	// The proxy runs inside the namespace as the normal user.
	exe, err := os.Executable()
	if err != nil {
		return t, Exit{}, err
	}
	sock := filepath.Join(p.dir, fmt.Sprintf("%d.sock", i))
	tok := make([]byte, 16)
	rand.Read(tok)
	token := hex.EncodeToString(tok)
	args := []string{"ip", "netns", "exec", t.ns, "setpriv",
		"--reuid=" + strconv.Itoa(os.Getuid()), "--regid=" + strconv.Itoa(os.Getgid()), "--clear-groups",
		exe, "vpn-proxy", "--socket", sock}
	if os.Geteuid() != 0 {
		args = append([]string{"sudo", "-n"}, args...)
	}
	t.cmd = exec.CommandContext(pctx, args[0], args[1:]...)
	t.cmd.Stdin = strings.NewReader(token + "\n")
	t.cmd.Stderr = os.Stderr
	t.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := t.cmd.Start(); err != nil {
		return t, Exit{}, err
	}
	for k := 0; ; k++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if k > 100 {
			return t, Exit{}, errors.New("proxy did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	addr, err := Relay(pctx, sock)
	if err != nil {
		return t, Exit{}, err
	}
	ex := Exit{Name: name, Proxy: "socks5h://ytm:" + token + "@" + addr}
	if err := probe(ctx, ex.Proxy); err != nil {
		return t, Exit{}, fmt.Errorf("tunnel is up but can't reach YouTube (%v) - expired config or blocked UDP?", err)
	}
	return t, ex, nil
}

// probe checks that traffic really flows through the proxy.
func probe(ctx context.Context, proxy string) error {
	u, _ := url.Parse(proxy)
	pass, _ := u.User.Password()
	var last error
	for try := 0; try < 3; try++ {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		c, err := socksDial(cctx, u.Host, u.User.Username(), pass, "www.youtube.com:443")
		cancel()
		if err == nil {
			c.Close()
			return nil
		}
		last = err
		time.Sleep(time.Second)
	}
	return last
}

func socksDial(ctx context.Context, proxy, user, pass, target string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
	c.Write([]byte{5, 1, 2})
	r := bufio.NewReader(c)
	var b [2]byte
	if _, err := r.Read(b[:]); err != nil || b[1] != 2 {
		return fail(errors.New("proxy handshake failed"))
	}
	msg := append([]byte{1, byte(len(user))}, user...)
	msg = append(msg, byte(len(pass)))
	c.Write(append(msg, pass...))
	if _, err := r.Read(b[:]); err != nil || b[1] != 0 {
		return fail(errors.New("proxy auth failed"))
	}
	host, port, _ := net.SplitHostPort(target)
	pn, _ := strconv.Atoi(port)
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	c.Write(append(req, byte(pn>>8), byte(pn)))
	var rep [10]byte
	if _, err := r.Read(rep[:]); err != nil || rep[1] != 0 {
		return fail(errors.New("proxy could not connect"))
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// Close tears everything down.
func (p *Pool) Close() {
	p.cancel()
	ctx, c := context.WithTimeout(context.Background(), 20*time.Second)
	defer c()
	for _, t := range p.tunnel {
		if t.cmd != nil && t.cmd.Process != nil {
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
			_ = t.cmd.Wait()
		}
		_, _ = priv(ctx, "", "ip", "netns", "del", t.ns)
		_, _ = priv(ctx, "", "rm", "-rf", "/etc/netns/"+t.ns)
	}
	os.RemoveAll(p.dir)
}

// Clean removes namespaces left behind by ytm runs that died.
func Clean(logf func(string, ...any)) {
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	defer c()
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "ytm-") {
			continue
		}
		parts := strings.Split(f[0], "-")
		if len(parts) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(parts[1])
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			continue // owner still alive
		}
		if _, err := priv(ctx, "", "ip", "netns", "del", f[0]); err == nil {
			_, _ = priv(ctx, "", "rm", "-rf", "/etc/netns/"+f[0])
			logf("removed stale namespace %s", f[0])
		}
	}
}

// ProxyMain is the hidden `ytm vpn-proxy` entry point that runs inside a namespace.
func ProxyMain(socket string) error {
	tok, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	tok = strings.TrimSpace(tok)
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	_ = os.Chmod(socket, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return ServeSOCKS5(ctx, l, "ytm", tok)
}
