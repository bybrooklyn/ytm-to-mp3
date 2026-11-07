package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bybrooklyn/mfetchy/internal/bandcamp"
	"github.com/bybrooklyn/mfetchy/internal/browsercookies"
	"github.com/bybrooklyn/mfetchy/internal/config"
	"github.com/bybrooklyn/mfetchy/internal/jobs"
	"github.com/bybrooklyn/mfetchy/internal/proton"
	"github.com/bybrooklyn/mfetchy/internal/tidal"
	"github.com/bybrooklyn/mfetchy/internal/ui"
	"github.com/bybrooklyn/mfetchy/internal/vpnprovider/mullvad"
)

// rec is a Prompter that answers from a script and writes down every question, so a flow's
// questions can be checked without a terminal or a window. Like a real prompter it runs a
// field's Validate before accepting an answer, and keeps what that said.
type rec struct {
	mu sync.Mutex

	selects  []string            // answers to Select, in order (then the default)
	confirms []bool              // answers to Confirm (then the default)
	forms    []map[string]string // answers to Form; nil means the person backed out
	files    []string            // answers to File; "" means backed out
	inputs   []string

	asked      []string // "confirm: title", "select: title", "form: ...", ...
	questions  []Question
	formSpecs  []FormSpec
	fileReqs   []FileRequest
	infos      []Info
	validation []error // what each Validate answered
	notices    []string
}

func (r *rec) note(s string) { r.asked = append(r.asked, s) }

func (r *rec) Confirm(_ context.Context, q Question) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("confirm: " + q.Title)
	r.questions = append(r.questions, q)
	if len(r.confirms) == 0 {
		return q.Default, nil
	}
	a := r.confirms[0]
	r.confirms = r.confirms[1:]
	return a, nil
}

func (r *rec) Select(_ context.Context, title, detail string, opts []Option, def string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("select: " + title)
	r.questions = append(r.questions, Question{Title: title, Detail: detail})
	if len(r.selects) == 0 {
		return def, nil
	}
	a := r.selects[0]
	r.selects = r.selects[1:]
	if a == "<back>" {
		return def, ErrBack
	}
	return a, nil
}

func (r *rec) Input(_ context.Context, title, _, def string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("input: " + title)
	if len(r.inputs) == 0 {
		return def, nil
	}
	a := r.inputs[0]
	r.inputs = r.inputs[1:]
	return a, nil
}

func (r *rec) Form(_ context.Context, f FormSpec) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("form: " + f.Title)
	r.formSpecs = append(r.formSpecs, f)
	if len(r.forms) == 0 {
		return nil, ErrBack
	}
	a := r.forms[0]
	r.forms = r.forms[1:]
	if a == nil {
		return nil, ErrBack
	}
	for _, fd := range f.Fields {
		if fd.Validate != nil {
			err := fd.Validate(a[fd.Key])
			r.validation = append(r.validation, err)
			if err != nil {
				return nil, err
			}
		}
	}
	return a, nil
}

func (r *rec) File(_ context.Context, q FileRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("file: " + q.Title)
	r.fileReqs = append(r.fileReqs, q)
	if len(r.files) == 0 || r.files[0] == "" {
		if len(r.files) > 0 {
			r.files = r.files[1:]
		}
		return "", ErrBack
	}
	a := r.files[0]
	r.files = r.files[1:]
	if q.Validate != nil {
		err := q.Validate(a)
		r.validation = append(r.validation, err)
		if err != nil {
			return "", err
		}
	}
	return a, nil
}

func (r *rec) Show(_ context.Context, i Info) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.note("show: " + i.Title)
	r.infos = append(r.infos, i)
	return nil
}

func (r *rec) Wait(ctx context.Context, i Info, f func(context.Context) error) error {
	r.mu.Lock()
	r.note("wait: " + i.Title)
	r.infos = append(r.infos, i)
	r.mu.Unlock()
	return f(ctx)
}

func (r *rec) Notice(title, _ string) {
	r.mu.Lock()
	r.notices = append(r.notices, title)
	r.mu.Unlock()
}

func (r *rec) Working(ctx context.Context, title string, f func(context.Context) error) error {
	r.mu.Lock()
	r.note("working: " + title)
	r.mu.Unlock()
	return f(ctx)
}

func (r *rec) did(prefix string) int {
	n := 0
	for _, a := range r.asked {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func logUI() (*ui.UI, *bytes.Buffer) {
	var b bytes.Buffer
	return ui.NewWriter(&b), &b
}

// ---- Mullvad ------------------------------------------------------------------------------

const testRelays = `{
 "locations": {"se-sto": {"country": "Sweden", "city": "Stockholm", "latitude": 59.3, "longitude": 18.0}},
 "wireguard": {"port_ranges": [[4000,33433]], "ipv4_gateway": "10.64.0.1",
  "relays": [{"hostname":"se-sto-wg-001","location":"se-sto","active":true,"owned":true,"provider":"x","ipv4_addr_in":"185.65.135.1","include_in_country":true,"weight":100,"public_key":"ofyfRvMPB0PPIGGItNL+5tNdvTKXuWye5CfjPgPNvQ8=","ipv6_addr_in":"2a03::1"}]}}`

// fakeMullvadAPI knows one account; any other number is "INVALID_ACCOUNT".
func fakeMullvadAPI(t *testing.T, account string) *mullvad.Provider {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/v1/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Account string `json:"account_number"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Account != account {
			w.WriteHeader(400)
			io.WriteString(w, `{"code":"INVALID_ACCOUNT","error":"Invalid account"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expiry": time.Now().Add(time.Hour).Format(time.RFC3339)})
	})
	mux.HandleFunc("/accounts/v1/devices", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) })
	mux.HandleFunc("/public/relays/wireguard/v2", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, testRelays) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := mullvad.New(filepath.Join(t.TempDir(), "mullvad.json"))
	p.Base, p.Meta = srv.URL, srv.URL
	old := mullvadProvider
	mullvadProvider = func() *mullvad.Provider { return p }
	t.Cleanup(func() { mullvadProvider = old })
	return p
}

func TestVPNSignInMullvad(t *testing.T) {
	sandbox(t)
	p := fakeMullvadAPI(t, "1234567812345678")
	u, out := logUI()
	pr := &rec{selects: []string{ProvMullvad}, forms: []map[string]string{{"account": "1234567812345678"}}}
	if err := VPNSignIn(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if !p.SignedIn() || !strings.HasSuffix(p.Who(), "5678") {
		t.Fatalf("not signed in: %q", p.Who())
	}
	if cfg := config.Load(); cfg.Provider != ProvMullvad || cfg.NoVPN {
		t.Errorf("saved choice: %+v", cfg)
	}
	if !strings.Contains(out.String(), "signed in to Mullvad") {
		t.Errorf("log: %q", out.String())
	}
	// the account number is a secret in every prompter
	if f := pr.formSpecs[0].Fields[0]; f.Kind != FieldSecret || strings.Contains(out.String(), "1234567812345678") {
		t.Errorf("account field kind %q, or the number was printed", f.Kind)
	}
}

func TestVPNSignInMullvadWrongNumberThenGivesUp(t *testing.T) {
	sandbox(t)
	p := fakeMullvadAPI(t, "1234567812345678")
	u, _ := logUI()
	pr := &rec{selects: []string{ProvMullvad}, confirms: []bool{false},
		forms: []map[string]string{{"account": "8765432187654321"}}}
	err := VPNSignIn(context.Background(), u, pr)
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if p.SignedIn() {
		t.Error("a wrong number must not leave a sign-in behind")
	}
	// the retry question carries the reason, for a dialog to show
	q := pr.questions[len(pr.questions)-1]
	if q.Title != "Try again?" || !strings.Contains(q.Detail, "account number") {
		t.Errorf("retry question = %+v", q)
	}
	// a malformed number is stopped by the form itself
	pr = &rec{selects: []string{ProvMullvad}, forms: []map[string]string{{"account": "12"}}}
	if err := VPNSignIn(context.Background(), u, pr); err == nil || pr.validation[0] == nil {
		t.Errorf("a short number should fail validation: %v / %v", err, pr.validation)
	}
}

func TestVPNSignInBackingOutChangesNothing(t *testing.T) {
	sandbox(t)
	u, _ := logUI()
	pr := &rec{selects: []string{"<back>"}}
	if err := VPNSignIn(context.Background(), u, pr); !errors.Is(err, ErrBack) {
		t.Fatalf("err = %v", err)
	}
}

// ---- WireGuard ---------------------------------------------------------------------------

func wgConf(endpoint string) string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	pub := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.8.0.2/32\nDNS = 10.8.0.1\n\n[Peer]\nPublicKey = " + pub +
		"\nAllowedIPs = 0.0.0.0/0\nEndpoint = " + endpoint + "\n"
}

func TestVPNSignInWireGuardImportsAFolder(t *testing.T) {
	sandbox(t)
	src := t.TempDir()
	for i, ep := range []string{"203.0.113.5:51820", "203.0.113.6:51820"} {
		if err := os.WriteFile(filepath.Join(src, "exit"+string(rune('a'+i))+".conf"), []byte(wgConf(ep)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	u, out := logUI()
	pr := &rec{selects: []string{ProvWireGuard}, files: []string{src}}
	if err := VPNSignIn(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if !wireguardProvider().SignedIn() || wireguardProvider().Files() != 2 {
		t.Fatalf("imported %d", wireguardProvider().Files())
	}
	if !strings.Contains(out.String(), "imported 2 config files") {
		t.Errorf("log: %q", out.String())
	}
	if r := pr.fileReqs[0]; !r.File || !r.Folder {
		t.Errorf("the request should allow a file or a folder: %+v", r)
	}
}

func TestVPNSignInWireGuardBadFileOffersAnotherTry(t *testing.T) {
	sandbox(t)
	bad := filepath.Join(t.TempDir(), "bad.conf")
	_ = os.WriteFile(bad, []byte("[Interface]\n"), 0o600)
	u, _ := logUI()
	pr := &rec{selects: []string{ProvWireGuard}, files: []string{bad}, confirms: []bool{false}}
	err := VPNSignIn(context.Background(), u, pr)
	if err == nil || !strings.Contains(err.Error(), "no config imported") {
		t.Fatalf("err = %v", err)
	}
	if q := pr.questions[len(pr.questions)-1]; q.Title != "Try another path?" || q.Detail == "" {
		t.Errorf("question = %+v", q)
	}
}

// ---- Proton ------------------------------------------------------------------------------

func fakeProton(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *proton.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(reply))
	t.Cleanup(srv.Close)
	c := proton.New(filepath.Join(t.TempDir(), "proton.json"))
	c.Base = srv.URL
	old := protonClient
	protonClient = func() *proton.Client { return c }
	t.Cleanup(func() { protonClient = old })
	return c
}

func TestProtonWrongPasswordShowsTheReasonAndOffersARetry(t *testing.T) {
	sandbox(t)
	var calls atomic.Int32
	fakeProton(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(422)
		io.WriteString(w, `{"Code":8002,"Error":"Incorrect login credentials"}`)
	})
	u, _ := logUI()
	pr := &rec{selects: []string{ProvProton}, confirms: []bool{true, false},
		forms: []map[string]string{{"user": "me@proton.me", "pass": "wrong"}, {"user": "me@proton.me", "pass": "wrong2"}}}
	err := VPNSignIn(context.Background(), u, pr)
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if pr.did("form:") != 2 {
		t.Errorf("the form should come back once: %v", pr.asked)
	}
	fd := pr.formSpecs[0].Fields
	if fd[0].Kind == FieldSecret || fd[1].Kind != FieldSecret || fd[1].Key != "pass" {
		t.Errorf("the password, and only it, is secret: %+v", fd)
	}
	// the second time the username is filled in again
	if got := pr.formSpecs[1].Fields[0].Default; got != "me@proton.me" {
		t.Errorf("username not kept: %q", got)
	}
	if q := pr.questions[1]; q.Title != "Try again?" || !strings.Contains(q.Detail, "Incorrect login credentials") {
		t.Errorf("retry question = %+v", q)
	}
}

func TestProtonRateLimitStopsWithoutAskingAgain(t *testing.T) {
	sandbox(t)
	fakeProton(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"Code":2028,"Error":"Too many attempts"}`)
	})
	u, _ := logUI()
	pr := &rec{selects: []string{ProvProton}, forms: []map[string]string{{"user": "me", "pass": "x"}}}
	err := VPNSignIn(context.Background(), u, pr)
	if err == nil || !strings.Contains(err.Error(), "stopping here") {
		t.Fatalf("err = %v", err)
	}
	if pr.did("confirm:") != 0 {
		t.Errorf("a rate limit must not offer a retry: %v", pr.asked)
	}
}

func TestProton2FACodePrompt(t *testing.T) {
	sandbox(t)
	good := false
	c := fakeProton(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct{ TwoFactorCode string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path == "/auth/2fa" && in.TwoFactorCode == "123456" {
			good = true
			io.WriteString(w, `{"Code":1000,"Scopes":["twofactor"]}`)
			return
		}
		w.WriteHeader(422)
		io.WriteString(w, `{"Code":12087,"Error":"Invalid code"}`)
	})
	c.Session = &proton.Session{Username: "me", UID: "u", AccessToken: "t"}
	u, out := logUI()
	pr := &rec{forms: []map[string]string{{"code": "000000"}, {"code": " 123456 "}}}
	if err := ask2FA(context.Background(), u, pr, c); err != nil {
		t.Fatal(err)
	}
	if !good || pr.did("form:") != 2 || !strings.Contains(out.String(), "Invalid code") {
		t.Errorf("good=%v asked=%v log=%q", good, pr.asked, out.String())
	}
	if f := pr.formSpecs[0].Fields[0]; f.Label != "2FA code" {
		t.Errorf("field = %+v", f)
	}
	// three wrong codes in a row end it
	pr = &rec{forms: []map[string]string{{"code": "1"}, {"code": "2"}, {"code": "3"}}}
	if err := ask2FA(context.Background(), u, pr, c); err == nil || err.Error() != "2FA failed" {
		t.Errorf("err = %v", err)
	}
	// backing out of the prompt is not a failure of the code
	if err := ask2FA(context.Background(), u, &rec{}, c); !errors.Is(err, ErrBack) {
		t.Errorf("err = %v", err)
	}
}

// ---- Tidal -------------------------------------------------------------------------------

type tidalFake struct {
	started atomic.Int32
	polls   atomic.Int32
	approve int32 // the poll that is approved; 0 = never
	reason  string
}

func fakeTidalAPI(t *testing.T, f *tidalFake) *tidal.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		f.started.Add(1)
		io.WriteString(w, `{"deviceCode":"dev","userCode":"AB12CD","verificationUri":"link.tidal.com","verificationUriComplete":"link.tidal.com/AB12CD","expiresIn":300,"interval":1}`)
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		n := f.polls.Add(1)
		switch {
		case f.reason != "":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"`+f.reason+`"}`)
		case f.approve != 0 && n >= f.approve:
			io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":3600,"user":{"userId":42,"countryCode":"DE"}}`)
		default:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"authorization_pending"}`)
		}
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"countryCode":"DE","userId":42}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := tidal.New(filepath.Join(t.TempDir(), "tidal.json"))
	c.AuthURL, c.APIURL, c.HTTP = srv.URL+"/auth", srv.URL+"/api", srv.Client()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	old := tidalClient
	tidalClient = func() *tidal.Client { return c }
	t.Cleanup(func() { tidalClient = old })
	return c
}

func TestTidalLoginShowsTermsThenCodeAndLink(t *testing.T) {
	sandbox(t)
	f := &tidalFake{approve: 3}
	c := fakeTidalAPI(t, f)
	u, out := logUI()
	pr := &rec{confirms: []bool{true}}
	if err := TidalLogin(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if !c.SignedIn() || c.Country() != "DE" || f.polls.Load() != 3 {
		t.Fatalf("signed in %v country %s polls %d", c.SignedIn(), c.Country(), f.polls.Load())
	}
	// the terms come first, in the question itself, and must be accepted
	q := pr.questions[0]
	if !strings.Contains(q.Detail, "Tidal's terms do not allow downloading") || q.Yes != "Yes, I understand" {
		t.Errorf("terms question = %+v", q)
	}
	i := pr.infos[0]
	if i.Code != "AB12CD" || i.Link != "https://link.tidal.com/AB12CD" || i.Waiting == "" {
		t.Errorf("sign-in page = %+v", i)
	}
	if !strings.Contains(out.String(), "Signed in to Tidal (country DE)") {
		t.Errorf("log: %q", out.String())
	}
}

func TestTidalLoginDeclinedTermsTouchesNothing(t *testing.T) {
	sandbox(t)
	f := &tidalFake{approve: 1}
	c := fakeTidalAPI(t, f)
	u, _ := logUI()
	if err := TidalLogin(context.Background(), u, &rec{confirms: []bool{false}}); err != nil {
		t.Fatal(err)
	}
	if f.started.Load() != 0 || c.SignedIn() {
		t.Error("declining the terms must not even ask Tidal for a code")
	}
}

func TestTidalLoginExpiredDeniedAndCancelled(t *testing.T) {
	sandbox(t)
	for reason, want := range map[string]error{"expired_token": tidal.ErrExpired, "access_denied": tidal.ErrDenied} {
		c := fakeTidalAPI(t, &tidalFake{reason: reason})
		u, _ := logUI()
		if err := TidalLogin(context.Background(), u, &rec{confirms: []bool{true}}); !errors.Is(err, want) {
			t.Errorf("%s: err = %v", reason, err)
		}
		if c.SignedIn() {
			t.Errorf("%s: signed in anyway", reason)
		}
	}
	// Cancel in the window ends the wait; the flow sees ErrBack and nothing is saved
	c := fakeTidalAPI(t, &tidalFake{})
	u, _ := logUI()
	pr := &cancelWait{rec: rec{confirms: []bool{true}}}
	if err := TidalLogin(context.Background(), u, pr); !errors.Is(err, ErrBack) || c.SignedIn() {
		t.Errorf("err = %v signedIn = %v", err, c.SignedIn())
	}
}

// cancelWait is a window whose person presses Cancel as soon as the code is shown.
type cancelWait struct{ rec }

func (c *cancelWait) Wait(ctx context.Context, i Info, f func(context.Context) error) error {
	c.rec.infos = append(c.rec.infos, i)
	return ErrBack
}

// ---- Apple Music -------------------------------------------------------------------------

func fakeApple(t *testing.T, browsers map[string]string, storefront string, verifyErr error) {
	t.Helper()
	oldD, oldC, oldV := detectBrowsers, appleBrowserCookies, appleVerify
	t.Cleanup(func() { detectBrowsers, appleBrowserCookies, appleVerify = oldD, oldC, oldV })
	detectBrowsers = func() []browsercookies.Browser {
		var bs []browsercookies.Browser
		for _, id := range []string{"firefox", "chrome"} {
			if _, ok := browsers[id]; ok {
				bs = append(bs, browsercookies.Browser{ID: id, Name: strings.ToUpper(id[:1]) + id[1:]})
			}
		}
		return bs
	}
	appleBrowserCookies = func(b browsercookies.Browser) ([]browsercookies.Cookie, error) {
		return []browsercookies.Cookie{{Name: browsercookies.AppleUserTokenCookie, Value: browsers[b.ID], Domain: ".apple.com"},
			{Name: "itspod", Value: "34", Domain: ".apple.com"}}, nil
	}
	appleVerify = func(_ context.Context, token, _ string) (string, error) {
		if verifyErr != nil {
			return "", verifyErr
		}
		return storefront, nil
	}
}

func TestAppleLoginUsesTheBrowsersSession(t *testing.T) {
	sandbox(t)
	fakeApple(t, map[string]string{"firefox": "tok-ff"}, "gb", nil)
	u, out := logUI()
	pr := &rec{}
	if err := appleLogin(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if config.AppleToken() != "tok-ff" || config.Load().AppleStorefront != "gb" {
		t.Errorf("token %q store %q", config.AppleToken(), config.Load().AppleStorefront)
	}
	if !strings.Contains(config.AppleCookies(), "itspod=34") {
		t.Errorf("the whole cookie jar should be kept: %q", config.AppleCookies())
	}
	if q := pr.questions[0]; !strings.Contains(q.Title, "Firefox") {
		t.Errorf("the question should name the browser: %+v", q)
	}
	if !strings.Contains(out.String(), "Signed in to Apple Music · store GB") {
		t.Errorf("log: %q", out.String())
	}
}

func TestAppleLoginAsksWhichBrowserWhenSeveralAreSignedIn(t *testing.T) {
	sandbox(t)
	fakeApple(t, map[string]string{"firefox": "tok-ff", "chrome": "tok-ch"}, "us", nil)
	u, _ := logUI()
	pr := &rec{selects: []string{"Chrome"}}
	if err := appleLogin(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if config.AppleToken() != "tok-ch" {
		t.Errorf("token = %q", config.AppleToken())
	}
}

func TestAppleLoginPastedTokenIsMaskedAndChecked(t *testing.T) {
	sandbox(t)
	fakeApple(t, nil, "de", nil) // no browser has a session
	u, _ := logUI()
	pr := &rec{forms: []map[string]string{{"token": "  pasted-token "}}}
	if err := appleLogin(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if config.AppleToken() != "pasted-token" || config.Load().AppleStorefront != "de" {
		t.Errorf("token %q", config.AppleToken())
	}
	if f := pr.formSpecs[0].Fields[0]; f.Kind != FieldSecret {
		t.Errorf("the token field must be masked: %+v", f)
	}
	// a token Apple refuses is not saved
	sandbox(t)
	fakeApple(t, nil, "", errors.New("401"))
	pr = &rec{forms: []map[string]string{{"token": "nope"}}}
	if err := appleLogin(context.Background(), u, pr); err == nil || !strings.Contains(err.Error(), "that token doesn't work") || config.AppleToken() != "" {
		t.Errorf("err = %v token = %q", err, config.AppleToken())
	}
	// empty: nothing to do
	if err := appleLogin(context.Background(), u, &rec{forms: []map[string]string{{"token": ""}}}); err == nil || err.Error() != "no login" {
		t.Errorf("err = %v", err)
	}
}

func TestAppleLogoutForgets(t *testing.T) {
	sandbox(t)
	_ = config.SaveAppleToken("t")
	_ = config.SaveAppleCookies("c=1")
	u, out := logUI()
	if err := appleLogout(u); err != nil || config.AppleToken() != "" || config.AppleCookies() != "" {
		t.Fatalf("err %v token %q", err, config.AppleToken())
	}
	if !strings.Contains(out.String(), "Forgot your Apple Music login") {
		t.Errorf("log: %q", out.String())
	}
}

// ---- YouTube Premium ---------------------------------------------------------------------

func TestPremiumSetupNeedsTheWarningAccepted(t *testing.T) {
	sandbox(t)
	u, _ := logUI()
	pr := &rec{confirms: []bool{false}}
	if err := PremiumSetup(context.Background(), u, pr); err != nil || config.Load().Premium {
		t.Fatalf("err %v premium %v", err, config.Load().Premium)
	}
	q := pr.questions[0]
	if !strings.Contains(q.Detail, "EXPERIMENTAL") || q.Yes != "Yes, I understand" {
		t.Errorf("warning question = %+v", q)
	}
	if pr.did("select:") != 0 {
		t.Error("nothing about cookies may be asked before the warning is accepted")
	}
}

func TestPremiumSetupWithACookiesFileAndTurnOff(t *testing.T) {
	sandbox(t)
	oldD := detectBrowsers
	detectBrowsers = func() []browsercookies.Browser { return []browsercookies.Browser{{ID: "firefox", Name: "Firefox"}} }
	t.Cleanup(func() { detectBrowsers = oldD })
	cookies := filepath.Join(t.TempDir(), "cookies.txt")
	_ = os.WriteFile(cookies, []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tabc\n"), 0o600)
	u, _ := logUI()
	pr := &rec{confirms: []bool{true}, selects: []string{"file"}, files: []string{cookies}}
	if err := PremiumSetup(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	if !config.Load().Premium || !fileExists(config.PremiumCookiesPath()) {
		t.Fatal("not turned on")
	}
	if opts := pr.questions[1]; opts.Title == "" || pr.did("select:") != 1 {
		t.Errorf("asked: %v", pr.asked)
	}
	// a file with no YouTube cookies is refused by the question itself
	if err := checkCookies(cookies); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.txt")
	_ = os.WriteFile(other, []byte("nothing\n"), 0o600)
	if pr2 := (&rec{confirms: []bool{true}, selects: []string{"file"}, files: []string{other}}); func() bool {
		sandbox(t)
		return PremiumSetup(context.Background(), u, pr2) != nil && !config.Load().Premium && pr2.validation[0] != nil
	}() == false {
		t.Error("a file without youtube.com cookies must be refused")
	}
	// turning it off asks once and deletes the copy
	sandbox(t)
	cfg := config.Load()
	cfg.Premium = true
	_ = cfg.Save()
	_ = config.WriteSecret(config.PremiumCookiesPath(), []byte("x"))
	if err := PremiumSetup(context.Background(), u, &rec{confirms: []bool{true}}); err != nil {
		t.Fatal(err)
	}
	if config.Load().Premium || fileExists(config.PremiumCookiesPath()) {
		t.Error("not turned off")
	}
}

func TestPremiumSetupFromABrowser(t *testing.T) {
	sandbox(t)
	oldD, oldI := detectBrowsers, importCookies
	detectBrowsers = func() []browsercookies.Browser {
		return []browsercookies.Browser{{ID: "firefox", Name: "Firefox", Note: "close it first"}}
	}
	importCookies = func(b browsercookies.Browser) ([]browsercookies.Cookie, error) {
		return []browsercookies.Cookie{{Domain: ".youtube.com", Path: "/", Name: "SID", Value: "abc"}}, nil
	}
	t.Cleanup(func() { detectBrowsers, importCookies = oldD, oldI })
	u, out := logUI()
	pr := &rec{confirms: []bool{true}, selects: []string{"firefox"}}
	if err := PremiumSetup(context.Background(), u, pr); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(config.PremiumCookiesPath())
	if !config.Load().Premium || !strings.Contains(string(b), "SID") || !strings.Contains(out.String(), "1 YouTube cookies imported") {
		t.Errorf("premium %v cookies %q log %q", config.Load().Premium, b, out.String())
	}
}

// ---- Takeout and the library flows -------------------------------------------------------

func songsCSV(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "music library songs.csv")
	if err := os.WriteFile(p, []byte("Video ID,Song Title,Album Title,Artist Name 1\naaaaaaaaaaa,One,Alb,Artist\nbbbbbbbbbbb,Two,Alb,Artist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChooseExportPickAnotherFileValidatesInTheQuestion(t *testing.T) {
	sandbox(t) // an empty home: nothing is found lying around
	csv := songsCSV(t)
	u, _ := logUI()
	pr := &rec{selects: []string{"pick"}, files: []string{csv}}
	src, err := ChooseExport(context.Background(), u, pr)
	if err != nil {
		t.Fatal(err)
	}
	if src.Tracks != 2 || len(src.Paths) != 1 || src.Paths[0] != csv {
		t.Errorf("source = %+v", src)
	}
	if q := pr.questions[0]; q.Title != "No export found yet" {
		t.Errorf("empty list should say so: %+v", q)
	}
	// a path that isn't there, or has no tracks, is refused by the question
	pr = &rec{selects: []string{"pick"}, files: []string{filepath.Join(t.TempDir(), "nope.zip")}}
	if _, err := ChooseExport(context.Background(), u, pr); err == nil || !strings.Contains(err.Error(), "can't find") {
		t.Errorf("err = %v", err)
	}
	// Back, and backing out of the file question
	if _, err := ChooseExport(context.Background(), u, &rec{selects: []string{"back"}}); !errors.Is(err, ErrBack) {
		t.Errorf("err = %v", err)
	}
	if _, err := ChooseExport(context.Background(), u, &rec{selects: []string{"pick"}, files: []string{""}}); !errors.Is(err, ErrBack) {
		t.Errorf("err = %v", err)
	}
}

func TestChooseExportListsWhatItFindsAndTheGuideIsAnInfoPage(t *testing.T) {
	home := sandbox(t)
	dl := filepath.Join(home, "Downloads")
	_ = os.MkdirAll(dl, 0o755)
	data, _ := os.ReadFile(songsCSV(t))
	_ = os.WriteFile(filepath.Join(dl, "music library songs.csv"), data, 0o600)
	u, _ := logUI()
	var opts []Option
	pr := &selectSpy{rec: rec{selects: []string{"0"}}, got: &opts}
	src, err := ChooseExport(context.Background(), u, pr)
	if err != nil || src.Tracks != 2 {
		t.Fatalf("src %+v err %v", src, err)
	}
	if len(opts) != 4 || !strings.Contains(opts[0].Label, "2 tracks") || !strings.Contains(opts[1].Label, "another file") ||
		!strings.Contains(opts[2].Label, "don't have one") {
		t.Errorf("options = %+v", opts)
	}

	// "I don't have one yet": the steps and a link to Google Takeout, then a wait for the zip
	// (the wait here ends at once, as if Cancel were pressed)
	pr2 := &endWait{rec: rec{selects: []string{"guide"}}}
	if _, err := ChooseExport(context.Background(), u, pr2); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	i := pr2.infos[0]
	if i.Link != "https://takeout.google.com/" || len(i.Steps) != 5 || i.LinkLabel == "" || !strings.Contains(i.Steps[1], "YouTube and YouTube Music") {
		t.Errorf("guide page = %+v", i)
	}
}

type selectSpy struct {
	rec
	got *[]Option
}

func (s *selectSpy) Select(ctx context.Context, title, detail string, opts []Option, def string) (string, error) {
	*s.got = opts
	return s.rec.Select(ctx, title, detail, opts, def)
}

// endWait runs the waiting function with a context that is already over.
type endWait struct{ rec }

func (e *endWait) Wait(ctx context.Context, i Info, f func(context.Context) error) error {
	e.rec.infos = append(e.rec.infos, i)
	c, cancel := context.WithCancel(ctx)
	cancel()
	return f(c)
}

func TestTakeoutFlowPreviewThenDownload(t *testing.T) {
	sandbox(t)
	csv := songsCSV(t)
	var got []string
	old := downloadOnceFn
	downloadOnceFn = func(_ context.Context, _ *ui.UI, _ opts, _ *jobs.Ledger, _ jobs.Sink, inputs []string) error {
		got = inputs
		return nil
	}
	t.Cleanup(func() { downloadOnceFn = old })
	u, _ := logUI()
	o := OptionsFromConfig(config.Load())
	o.Out = t.TempDir()
	pr := &rec{selects: []string{"pick"}, files: []string{csv}}
	if err := TakeoutFlow(context.Background(), u, o, pr); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != csv {
		t.Errorf("downloaded %v", got)
	}
	q := pr.questions[len(pr.questions)-1]
	if q.Title != "Download?" || !strings.Contains(q.Detail, "2 tracks") || !strings.Contains(q.Detail, "Saving to") || q.Yes != "Download" {
		t.Errorf("preview = %+v", q)
	}
	// Back at the preview downloads nothing
	got = nil
	pr = &rec{selects: []string{"pick"}, files: []string{csv}, confirms: []bool{false}}
	if err := TakeoutFlow(context.Background(), u, o, pr); !errors.Is(err, ErrBack) || got != nil {
		t.Errorf("err %v got %v", err, got)
	}
}

func TestBandcampFlowReadsThenAsks(t *testing.T) {
	sandbox(t)
	oldD, oldB := detectBrowsers, bandcampCollection
	detectBrowsers = func() []browsercookies.Browser { return []browsercookies.Browser{{ID: "firefox", Name: "Firefox"}} }
	bandcampCollection = func(context.Context, browsercookies.Browser) (string, []bandcamp.Item, error) {
		return "https://bandcamp.com/me", []bandcamp.Item{{Artist: "A", Title: "One"}, {Artist: "B", Title: "Two"}, {Artist: "C", Title: "Three"}}, nil
	}
	var got []string
	oldF := downloadOnceFn
	downloadOnceFn = func(_ context.Context, _ *ui.UI, _ opts, _ *jobs.Ledger, _ jobs.Sink, inputs []string) error {
		got = inputs
		return nil
	}
	t.Cleanup(func() { detectBrowsers, bandcampCollection, downloadOnceFn = oldD, oldB, oldF })
	u, _ := logUI()
	o := OptionsFromConfig(config.Load())
	o.Out = t.TempDir()

	// no browser chosen yet: it asks which, remembers it, then asks to download
	pr := &rec{selects: []string{"firefox"}}
	if err := BandcampFlow(context.Background(), u, o, pr); err != nil {
		t.Fatal(err)
	}
	if config.Load().BandcampBrowser != "firefox" {
		t.Error("the browser should be remembered")
	}
	if len(got) != 1 || got[0] != "https://bandcamp.com/me" {
		t.Errorf("downloaded %v", got)
	}
	if q := pr.questions[len(pr.questions)-1]; q.Title != "Download 3 purchases?" || !strings.Contains(q.Detail, "original files") {
		t.Errorf("question = %+v", q)
	}
	// the second time it doesn't ask again, and Back downloads nothing
	got = nil
	pr = &rec{confirms: []bool{false}}
	if err := BandcampFlow(context.Background(), u, o, pr); !errors.Is(err, ErrBack) || got != nil || pr.did("select:") != 0 {
		t.Errorf("err %v got %v asked %v", err, got, pr.asked)
	}
	// nothing bought
	bandcampCollection = func(context.Context, browsercookies.Browser) (string, []bandcamp.Item, error) {
		return "https://bandcamp.com/me", nil, nil
	}
	if err := BandcampFlow(context.Background(), u, o, &rec{}); err == nil {
		t.Error("an empty collection should say so")
	}
}

// ---- tag, tidy, sidecars -----------------------------------------------------------------

func TestTagFlowFormAndBackingOut(t *testing.T) {
	sandbox(t)
	u, _ := logUI()
	o := OptionsFromConfig(config.Load())
	o.Out = t.TempDir()
	// the form carries the folder check, the modes (including undo) and the online switch
	pr := &rec{forms: []map[string]string{{"dir": "/definitely/not/here", "mode": "preview", "online": "yes"}}}
	if err := TagFlow(context.Background(), u, o, pr); err == nil || pr.validation[0] == nil || !strings.Contains(pr.validation[0].Error(), "isn't a folder") {
		t.Fatalf("err %v validation %v", err, pr.validation)
	}
	fs := pr.formSpecs[0]
	var modes []string
	for _, f := range fs.Fields {
		if f.Key == "mode" {
			for _, o := range f.Options {
				modes = append(modes, o.Value)
			}
		}
	}
	if strings.Join(modes, ",") != "preview,apply,rename,undo,back" || fs.Fields[2].Kind != FieldYesNo || fs.Fields[2].Yes != "Online" {
		t.Errorf("form = %+v", fs)
	}
	if fs.Fields[0].Default == "" {
		t.Error("the folder should start as the music folder")
	}
	pr = &rec{forms: []map[string]string{{"dir": o.Out, "mode": "back", "online": "no"}}}
	if err := TagFlow(context.Background(), u, o, pr); !errors.Is(err, ErrBack) {
		t.Errorf("err = %v", err)
	}
	if err := TagFlow(context.Background(), u, o, &rec{}); !errors.Is(err, ErrBack) {
		t.Errorf("closing the form: %v", err)
	}
}

// caseInsensitiveDir reports whether dir's file system treats names that differ only by
// capitals as the same (the default on macOS and Windows): then "Daft Punk" and "daft punk"
// can't exist side by side and there is nothing to merge.
func caseInsensitiveDir(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "CaseProbe"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	_ = os.Remove(filepath.Join(dir, "CaseProbe"))
	return err == nil
}

func TestTidyFlowMergesDuplicateFoldersAfterConfirmation(t *testing.T) {
	sandbox(t)
	root := t.TempDir()
	if caseInsensitiveDir(t, root) {
		t.Skip("this file system treats Daft Punk and daft punk as one folder: nothing to merge")
	}
	write := func(rel string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Daft Punk/Discovery/01 - One More Time.opus")
	write("daft punk/Discovery/02 - Aerodynamic.opus")
	u, out := logUI()
	o := OptionsFromConfig(config.Load())
	o.Out = root

	// say no to the merge: nothing moves (the flow then needs ffmpeg for the rename preview,
	// so stop it there by answering "no" and ending with an error we ignore)
	pr := &rec{confirms: []bool{false}}
	_ = mergeFlow(context.Background(), u, o.internal(pr))
	if pr.questions[0].Title != "Merge these folders?" || pr.questions[0].Yes != "Merge" {
		t.Errorf("question = %+v", pr.questions[0])
	}
	if _, err := os.Stat(filepath.Join(root, "daft punk")); err != nil {
		t.Error("nothing should move before the merge is confirmed")
	}
	if !strings.Contains(out.String(), "Daft Punk") {
		t.Errorf("the preview should list the merge: %q", out.String())
	}

	pr = &rec{confirms: []bool{true}}
	if err := mergeFlow(context.Background(), u, o.internal(pr)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("folders after merging: %v", dirs)
	}
	if _, err := os.Stat(filepath.Join(root, dirs[0], "Discovery", "02 - Aerodynamic.opus")); err != nil {
		t.Errorf("the second song should be in the merged folder: %v", err)
	}
}

func TestSidecarLibraryFlowSaysWhenThereIsNothingToDo(t *testing.T) {
	sandbox(t)
	u, out := logUI()
	o := OptionsFromConfig(config.Load())
	o.Out = t.TempDir()
	if err := SidecarLibraryFlow(context.Background(), u, o, &rec{}); err != nil || !strings.Contains(out.String(), "no songs in") {
		t.Errorf("err %v log %q", err, out.String())
	}
	cfg := config.Load()
	cfg.NoFolderJpg, cfg.NoNFO, cfg.CoverPNG = true, true, false
	_ = cfg.Save()
	u2, out2 := logUI()
	if err := SidecarLibraryFlow(context.Background(), u2, o, &rec{}); err != nil || !strings.Contains(out2.String(), "switched off in Settings") {
		t.Errorf("err %v log %q", err, out2.String())
	}
}

// ---- the plan and the terminal prompter --------------------------------------------------

func TestTerminalPrompterFormMasksSecretsAndValidates(t *testing.T) {
	var screen bytes.Buffer
	typed := []string{"sh0rt", "hunter2-hunter2"}
	u := ui.NewTerminal(strings.NewReader("me@proton.me\n"), &screen, func() (string, error) {
		s := typed[0]
		typed = typed[1:]
		return s, nil
	})
	p := TerminalPrompter{U: u}
	got, err := p.Form(context.Background(), FormSpec{Fields: []Field{
		{Key: "user", Label: "Proton email or username"},
		{Key: "pass", Kind: FieldSecret, Label: "Password", Validate: func(s string) error {
			if len(s) < 8 {
				return errors.New("too short")
			}
			return nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got["user"] != "me@proton.me" || got["pass"] != "hunter2-hunter2" {
		t.Fatalf("answers = %v", got)
	}
	// the secret was read through the no-echo reader and never printed; the refusal was
	if strings.Contains(screen.String(), "hunter2") || strings.Contains(screen.String(), "sh0rt") {
		t.Errorf("a secret reached the screen: %q", screen.String())
	}
	if !strings.Contains(screen.String(), "too short") {
		t.Errorf("the validation error should be shown: %q", screen.String())
	}
}

func TestTerminalPrompterFileInfoAndNobodyThere(t *testing.T) {
	var screen bytes.Buffer
	u := ui.NewTerminal(strings.NewReader("nope\n\"/tmp/a b.conf\"\n"), &screen, func() (string, error) { return "", nil })
	p := TerminalPrompter{U: u}
	got, err := p.File(context.Background(), FileRequest{Title: "Where?", Validate: func(s string) error {
		if !strings.HasSuffix(s, ".conf") {
			return errors.New("not a .conf")
		}
		return nil
	}})
	if err != nil || got != "/tmp/a b.conf" || !strings.Contains(screen.String(), "not a .conf") {
		t.Errorf("got %q err %v screen %q", got, err, screen.String())
	}

	screen.Reset()
	if err := p.Wait(context.Background(), Info{Title: "Tidal sign-in", Link: "https://link.tidal.com/AB12CD", Code: "AB12CD",
		Detail: "the link already has it filled in", Waiting: "waiting for you to approve it in the browser"}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Tidal sign-in", "Open  https://link.tidal.com/AB12CD", "Code  AB12CD", "the link already has it filled in", "Ctrl-C to cancel"} {
		if !strings.Contains(ansi.Strip(screen.String()), want) {
			t.Errorf("terminal page lacks %q:\n%s", want, ansi.Strip(screen.String()))
		}
	}

	// no terminal (a pipe, a script): forms answer their defaults, files back out
	q := TerminalPrompter{U: ui.NewWriter(io.Discard)}
	f, _ := q.Form(context.Background(), FormSpec{Fields: []Field{{Key: "k", Default: "d"}}})
	if f["k"] != "d" {
		t.Errorf("form = %v", f)
	}
	if _, err := (AutoPrompter{}).File(context.Background(), FileRequest{}); !errors.Is(err, ErrBack) {
		t.Errorf("auto prompter file: %v", err)
	}
}

func TestVPNTestAndDoctorWriteReadableLines(t *testing.T) {
	sandbox(t)
	u, out := logUI()
	if err := VPNTest(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "VPN: not signed in") || !strings.Contains(s, "music folder") {
		t.Errorf("log: %q", s)
	}
}
