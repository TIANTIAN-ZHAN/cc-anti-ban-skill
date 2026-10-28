//go:build darwin

package localweb

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"cc-anti-ban-skill/internal/execution"
	"cc-anti-ban-skill/internal/health"
	"cc-anti-ban-skill/internal/runner"
)

func TestMain(m *testing.M) {
	if os.Getenv("CC_WEB_FAKE_CLI") == "1" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			if os.Getenv(key) != os.Getenv("EXPECTED_PROXY") {
				os.Exit(90)
			}
		}
		if os.Getenv("NO_PROXY") != "" || os.Getenv("ALL_PROXY") != "" || os.Getenv("FAKE_INHERITED_SECRET") != "fictional-only" {
			os.Exit(91)
		}
		for _, entry := range os.Environ() {
			if strings.HasPrefix(entry, "DYLD_") {
				os.Exit(92)
			}
		}
		fmt.Println("WEB_FAKE_READY")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Println("WEB_FAKE_ECHO:" + strings.TrimSpace(line))
		os.Exit(7)
	}
	os.Exit(m.Run())
}

type launchReceipt struct {
	Started         int32            `json:"started"`
	Connects        int32            `json:"connects"`
	Heads           int32            `json:"heads"`
	Status          execution.Status `json:"status"`
	ReplayRejected  bool             `json:"replay_rejected"`
	ParentUnchanged bool             `json:"parent_unchanged"`
	LostReplies     int32            `json:"lost_replies,omitempty"`
}

func proxyTLSFixture(t *testing.T) (*httptest.Server, *x509.CertPool, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "isolated proxy fixture"}, DNSNames: []string{health.TargetHost}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	priv, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: priv}))
	if e != nil {
		t.Fatal(e)
	}
	parsed, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	connects, heads := &atomic.Int32{}, &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != health.TargetHost+":443" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("unapproved proxy request")
			w.WriteHeader(400)
			return
		}
		connects.Add(1)
		c, b, e := w.(http.Hijacker).Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(4 * time.Second))
		_, _ = b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = b.Flush()
		secure := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if e = secure.Handshake(); e != nil {
			return
		}
		req, e := http.ReadRequest(bufio.NewReader(secure))
		if e != nil {
			return
		}
		if req.Method != "HEAD" || req.Host != health.TargetHost || req.URL.Path != "/" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Error("wrong fixed reference request")
			return
		}
		heads.Add(1)
		_, _ = io.WriteString(secure, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}))
	t.Cleanup(server.Close)
	return server, roots, connects, heads
}

func TestRepairPTYChild(t *testing.T) {
	if os.Getenv("CC_WEB_PTY_CHILD") != "1" {
		t.Skip("isolated PTY child only")
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, "project")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	proxy, roots, connects, heads := proxyTLSFixture(t)
	_, portText, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	now := time.Now().UTC()
	d := health.LocalIO{OS: "darwin", Home: home, Directory: dir, Env: map[string]string{}, Lookup: func(string) (string, error) { return executable, nil }, Version: func(context.Context, string) (string, error) { return "2.1.999 (Claude Code)", nil }, Stat: func(p string) (fs.FileInfo, error) {
		if strings.HasPrefix(p, home+"/") {
			return os.Lstat(p)
		}
		return nil, fs.ErrNotExist
	}, Now: func() time.Time { return now }}
	if mode := os.Getenv("PTY_SCENARIO"); mode == "comparable" || mode == "ui" || mode == "privacy-ui" {
		d.Env["HTTPS_PROXY"] = proxy.URL
	}
	if mode := os.Getenv("PTY_SCENARIO"); mode == "ui" || mode == "privacy-ui" {
		// Exercise consent/report/export with fake sensitive content, never SystemIO.
		config := filepath.Join(home, ".claude", "settings.json")
		if e := os.MkdirAll(filepath.Dir(config), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(config, []byte(`{"apiKey":"FAKE_UI_SETTINGS_KEY_6","env":{"UNRELATED_TOKEN":"FAKE_UI_ENV_SECRET_6"}}`), 0600); e != nil {
			t.Fatal(e)
		}
		d.ReadSettings = func() ([]byte, error) { return os.ReadFile(config) }
	}
	network := func() health.NetworkIO {
		return health.NetworkIO{Roots: roots, TrustID: "isolated-fixture-ca-v1", Now: func() time.Time { return now }, Resolve: func(context.Context, string, string) ([]net.IP, error) {
			t.Error("literal proxy resolved")
			return nil, nil
		}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != proxy.Listener.Addr().String() {
				t.Error("nonfixture dial")
				return nil, fmt.Errorf("denied")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}}
	}
	observe := func(ctx context.Context) health.Snapshot { return health.Inspect(ctx, d) }
	terminal := runner.New(os.Stdin, os.Stdout, os.Stderr)
	var starts atomic.Int32
	var lastProcess execution.Process
	before := os.Getenv("HTTPS_PROXY")
	manager := execution.New(context.Background(), execution.Dependencies{Recheck: func(ctx context.Context, previous health.Snapshot) health.Snapshot {
		return health.Reinspect(ctx, d, previous)
	}, Available: terminal.Available, Start: func(prepared *runner.Prepared, env []string) (execution.Process, error) {
		starts.Add(1)
		p, e := terminal.Start(prepared, env)
		lastProcess = p
		return p, e
	}, Environment: func() []string {
		return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "CC_WEB_FAKE_CLI=1", "EXPECTED_PROXY=" + proxy.URL, "FAKE_INHERITED_SECRET=fictional-only", "HTTPS_PROXY=http://old.invalid", "NO_PROXY=*", "DYLD_PRINT_ENV=1", "DYLD_PRINT_TO_FILE=" + filepath.Join(home, "forbidden-dyld-output"), "DYLD_FUTURE_CONTROL=not-a-value-to-inspect"}
	}, Network: network, Now: func() time.Time { return now }})
	t.Cleanup(func() {
		manager.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if !manager.Wait(ctx) {
			t.Error("owned root not drained")
		}
	})
	bootstrap := Token()
	closedUI := make(chan struct{})
	var closeOnce sync.Once
	h := New("pending", bootstrap, Actions{Scan: observe, Plan: func(s health.Snapshot, n time.Time) health.Plan { return s.Plan(n) }, Probe: func(ctx context.Context, s health.Snapshot, p health.Plan) health.Report {
		return s.Probe(ctx, p, network())
	}, Close: func() { closeOnce.Do(func() { close(closedUI) }) }, Now: func() time.Time { return now }, Execution: manager})
	server := httptest.NewUnstartedServer(h)
	h.host = server.Listener.Addr().String()
	h.origin = "http://" + h.host
	var loss *cancelReplyLoss
	if os.Getenv("CC_UI_CANCEL_LOSS") == "1" {
		loss = &cancelReplyLoss{next: h, seen: map[string]bool{}}
		server.Config.Handler = loss
	}
	server.Start()
	defer server.Close()
	if uiPath := os.Getenv("CC_HEALTH_PHASE2_UI_FIXTURE"); uiPath != "" {
		if tmp := os.Getenv("TMPDIR"); tmp == "" || !strings.HasPrefix(filepath.Clean(uiPath), filepath.Clean(tmp)+string(os.PathSeparator)) {
			t.Fatal("UI path outside fixture TMPDIR")
		}
		if e := os.WriteFile(uiPath, []byte(fmt.Sprintf("%s/#%s\n%d\n", server.URL, bootstrap, port)), 0600); e != nil {
			t.Fatal(e)
		}
		defer os.Remove(uiPath)
		select {
		case <-closedUI:
		case <-time.After(3 * time.Minute):
		}
		manager.Close()
		wait, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if !manager.Wait(wait) {
			t.Fatal("UI root not drained")
		}
		status := execution.Status{State: "fixture_finished", Finished: true}
		if lastProcess != nil {
			v := lastProcess.View()
			status.Process = &v
		}
		file := os.NewFile(3, "fixture-result")
		if _, e := os.Stat(filepath.Join(home, "forbidden-dyld-output")); !os.IsNotExist(e) {
			t.Fatal("dyld wrote inherited environment before fake main")
		}
		receipt := launchReceipt{Started: starts.Load(), Connects: connects.Load(), Heads: heads.Load(), Status: status, ParentUnchanged: os.Getenv("HTTPS_PROXY") == before}
		if loss != nil {
			receipt.LostReplies = loss.drops.Load()
		}
		_ = json.NewEncoder(file).Encode(receipt)
		_ = file.Close()
		return
	}
	session := ""
	request := func(path, body string) (int, []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+path, strings.NewReader(body))
		r.Header.Set("Origin", server.URL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Session", session)
		if path == "/api/session" {
			r.Header.Set("X-Bootstrap", bootstrap)
		}
		response, e := server.Client().Do(r)
		if e != nil {
			t.Error("fixture HTTP request failed")
			return 0, nil
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data
	}
	code, data := request("/api/session", "{}")
	var login map[string]string
	_ = json.Unmarshal(data, &login)
	session = login["session"]
	if code != 200 || session == "" {
		t.Fatal("login")
	}
	if code, _ = request("/api/scan", "{}"); code != 200 {
		t.Fatal("scan")
	}
	baselineRequests := int32(0)
	if os.Getenv("PTY_SCENARIO") == "comparable" {
		c, data := request("/api/plan", "{}")
		var baseline health.Plan
		_ = json.Unmarshal(data, &baseline)
		if c != 200 || !baseline.Allowed {
			t.Fatal("baseline plan")
		}
		if c, _ = request("/api/probe", fmt.Sprintf(`{"plan_id":%q}`, baseline.ID)); c != 200 {
			t.Fatal("baseline probe")
		}
		baselineRequests = 1
	}
	prepare := func() string {
		code, data := request("/api/repair-plan", fmt.Sprintf(`{"loopback":"127.0.0.1","port":%d}`, port))
		var p execution.Plan
		_ = json.Unmarshal(data, &p)
		if code != 200 || !p.Allowed {
			t.Fatalf("plan unavailable: %s", p.Reason)
		}
		return p.ID
	}
	id := prepare()
	body := fmt.Sprintf(`{"plan_id":%q}`, id)
	if starts.Load() != 0 || connects.Load() != baselineRequests {
		t.Fatal("preview executed")
	}
	if code, _ = request("/api/repair-cancel", body); code != 200 {
		t.Fatal("cancel")
	}
	if code, _ = request("/api/repair-start", body); code != 409 || starts.Load() != 0 {
		t.Fatal("cancel replay")
	}
	id = prepare()
	body = fmt.Sprintf(`{"plan_id":%q}`, id)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c, _ := request("/api/repair-start", body); codes <- c }()
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for c := range codes {
		if c == 200 {
			accepted++
		} else if c != 409 {
			t.Fatal("bad start response")
		}
	}
	if accepted != 1 {
		t.Fatal("concurrent consume")
	}
	if c, _ := request("/api/repair-stop", `{"plan_id":"foreign"}`); c != 409 {
		t.Fatal("foreign process stop")
	}
	until := time.Now().Add(12 * time.Second)
	var status execution.Status
	closed := false
	for time.Now().Before(until) {
		status, e = manager.Status(id)
		if e == nil && status.Process != nil && os.Getenv("PTY_SCENARIO") == "service-close" && !closed {
			if c, _ := request("/api/close", "{}"); c != 200 {
				t.Fatal("close")
			}
			closed = true
		}
		if e == nil && status.Finished {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !status.Finished || status.Process == nil || !status.Process.EnvironmentDelivered || !status.Process.TerminalRestored || starts.Load() != 1 {
		t.Fatalf("missing launch lifecycle proof: %+v", status)
	}
	replayed := false
	if !closed {
		c, _ := request("/api/repair-start", body)
		replayed = c == 409
		if status.Reference == nil || status.Reference.Score.Value == nil || *status.Reference.Score.Value != 100 || heads.Load() != baselineRequests+1 || connects.Load() != baselineRequests+1 || status.Process.ExitCode == nil || *status.Process.ExitCode != 7 {
			t.Fatalf("reference or natural exit: %+v", status)
		}
		if status.Comparable != (baselineRequests == 1) {
			t.Fatal("completed matching profile comparison failed")
		}
	} else {
		replayed = true
		if status.Process.State != "stopped" {
			t.Fatal("service close did not stop owned root")
		}
	}
	if _, e := os.Stat(filepath.Join(home, "forbidden-dyld-output")); !os.IsNotExist(e) {
		t.Fatal("dyld wrote inherited environment before fake main")
	}
	receipt := launchReceipt{Started: starts.Load(), Connects: connects.Load(), Heads: heads.Load(), Status: status, ReplayRejected: replayed, ParentUnchanged: os.Getenv("HTTPS_PROXY") == before}
	file := os.NewFile(3, "fixture-result")
	if e := json.NewEncoder(file).Encode(receipt); e != nil {
		t.Fatal(e)
	}
	_ = file.Close()
}

func ptyIOCTL(fd, request uintptr, value unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if e != 0 {
		return e
	}
	return nil
}
func TestRepairHTTPPTYEndToEnd(t *testing.T) {
	for _, mode := range []string{"natural", "service-close", "comparable"} {
		t.Run(mode, func(t *testing.T) {
			runHTTPPTY(t, mode, "")
		})
	}
}

func TestPhase2UIFixture(t *testing.T) {
	path := os.Getenv("CC_HEALTH_PHASE2_UI_FIXTURE")
	if path == "" {
		t.Skip("manual isolated phase two UI")
	}
	runHTTPPTY(t, "ui", path)
}

func TestPrivacyConsentUIFixture(t *testing.T) {
	path := os.Getenv("CC_HEALTH_PRIVACY_UI_FIXTURE")
	if path == "" {
		t.Skip("manual isolated consent/report/export UI")
	}
	runHTTPPTY(t, "privacy-ui", path)
}

func runHTTPPTY(t *testing.T, mode, uiPath string) {
	master, e := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer master.Close()
	if e = ptyIOCTL(master.Fd(), syscall.TIOCPTYGRANT, nil); e != nil {
		t.Fatal(e)
	}
	if e = ptyIOCTL(master.Fd(), syscall.TIOCPTYUNLK, nil); e != nil {
		t.Fatal(e)
	}
	var name [128]byte
	if e = ptyIOCTL(master.Fd(), syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); e != nil {
		t.Fatal(e)
	}
	slave, e := os.OpenFile(strings.TrimRight(string(name[:]), "\x00"), os.O_RDWR|syscall.O_NOCTTY, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer slave.Close()
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	defer write.Close()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	home := t.TempDir()
	cmd := exec.Command(binary, "-test.run=^TestRepairPTYChild$", "-test.timeout=200s")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + os.Getenv("TMPDIR"), "CC_WEB_PTY_CHILD=1", "PTY_SCENARIO=" + mode, "HTTPS_PROXY=http://fixture-parent.invalid"}
	if uiPath != "" {
		cmd.Env = append(cmd.Env, "CC_HEALTH_PHASE2_UI_FIXTURE="+uiPath, "CC_UI_CANCEL_LOSS="+os.Getenv("CC_UI_CANCEL_LOSS"))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	_ = write.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		select {
		case <-done:
		default:
			_ = master.Close()
			_ = slave.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fixture did not exit after its owned PTY closed")
			}
		}
	}()
	echoed := make(chan bool, 1)
	go func() {
		r := bufio.NewReader(master)
		for {
			line, e := r.ReadString('\n')
			if strings.Contains(line, "WEB_FAKE_READY") && (mode == "natural" || mode == "ui" || mode == "comparable") {
				_, _ = master.Write([]byte("approved fixture\n"))
			}
			if strings.Contains(line, "WEB_FAKE_ECHO:approved fixture") {
				select {
				case echoed <- true:
				default:
				}
			}
			if e != nil {
				return
			}
		}
	}()
	got := make(chan launchReceipt, 1)
	go func() { var result launchReceipt; _ = json.NewDecoder(read).Decode(&result); got <- result }()
	var result launchReceipt
	timeout := 18 * time.Second
	if mode == "ui" || mode == "privacy-ui" {
		timeout = 190 * time.Second
	}
	select {
	case result = <-got:
	case <-time.After(timeout):
		t.Fatal("HTTP PTY lifecycle timeout")
	}
	if mode == "ui" && os.Getenv("CC_UI_CANCEL_LOSS") == "1" && result.LostReplies != 2 {
		t.Fatal("both lost cancellation replies were not exercised")
	}
	if mode == "privacy-ui" {
		if result.Started != 0 || result.Connects != 0 || result.Heads != 0 || !result.ParentUnchanged || result.Status.Process != nil || !result.Status.Finished {
			t.Fatal("privacy-only UI caused launch or reference traffic")
		}
	} else if result.Started != 1 || !result.ParentUnchanged || (!result.ReplayRejected && mode != "ui") || result.Status.Process == nil || !result.Status.Process.TerminalRestored {
		t.Fatalf("bad receipt: %+v", result)
	}
	if mode == "natural" || mode == "comparable" {
		select {
		case <-echoed:
		case <-time.After(time.Second):
			t.Fatal("interactive output missing")
		}
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("fixture supervisor failed")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("fixture supervisor exit timeout")
	}
	done <- nil
	if mode == "privacy-ui" {
		b, _ := json.Marshal(result)
		if e := os.WriteFile(uiPath+".result.json", b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

// Test-only transport failure: the cancellation reaches the server, but its
// first reply is lost. Query/close must remain usable without re-authorizing.
type cancelReplyLoss struct {
	next  http.Handler
	mu    sync.Mutex
	seen  map[string]bool
	drops atomic.Int32
}

func (h *cancelReplyLoss) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	drop := (r.URL.Path == "/api/cancel" || r.URL.Path == "/api/repair-cancel") && !h.seen[r.URL.Path]
	if drop {
		h.seen[r.URL.Path] = true
	}
	h.mu.Unlock()
	if drop {
		recorder := httptest.NewRecorder()
		h.next.ServeHTTP(recorder, r)
		h.drops.Add(1)
		// Once response headers have arrived, a browser cannot transparently
		// retry an early connection reset and hide the missing acknowledgement.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		if c, _, e := w.(http.Hijacker).Hijack(); e == nil {
			c.Close()
		}
		return
	}
	h.next.ServeHTTP(w, r)
}
