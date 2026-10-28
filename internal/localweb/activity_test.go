package localweb

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cc-anti-ban-skill/internal/health"
)

// A deliberately uncooperative transport returns a successful CONNECT even
// after Close/cancellation. No later phase may treat that response as consent.
type lateConnect struct {
	wrote    chan struct{}
	release  chan struct{}
	once     sync.Once
	writes   atomic.Int32
	response *strings.Reader
}

func (c *lateConnect) Write(p []byte) (int, error) {
	c.writes.Add(1)
	c.once.Do(func() { close(c.wrote) })
	return len(p), nil
}
func (c *lateConnect) Read(p []byte) (int, error)       { <-c.release; return c.response.Read(p) }
func (c *lateConnect) Close() error                     { return nil }
func (c *lateConnect) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *lateConnect) RemoteAddr() net.Addr             { return c.LocalAddr() }
func (c *lateConnect) SetDeadline(time.Time) error      { return nil }
func (c *lateConnect) SetReadDeadline(time.Time) error  { return nil }
func (c *lateConnect) SetWriteDeadline(time.Time) error { return nil }

func TestActivityExpiryCancelsInFlightProbeBeforeNextStage(t *testing.T) {
	for _, cause := range []string{"deadline_context", "expire", "page_close", "request_cancel"} {
		t.Run(cause, func(t *testing.T) {
			now := time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC)
			activity, cancel := context.WithCancel(context.Background())
			defer cancel()
			wire := &lateConnect{wrote: make(chan struct{}), release: make(chan struct{}), response: strings.NewReader("HTTP/1.1 200 Connection Established\r\n\r\n")}
			d := health.LocalIO{OS: "darwin", Home: "/fixture/home", Directory: "/fixture/project", Env: map[string]string{"HTTPS_PROXY": "http://127.0.0.1:4567"}, Lookup: func(string) (string, error) { return "/fixture/bin/claude", nil }, Version: func(context.Context, string) (string, error) { return "2.1.999 (Claude Code)", nil }, Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }, Now: func() time.Time { return now }}
			probeContext := make(chan context.Context, 1)
			var scans atomic.Int32
			a := Actions{ActivityContext: activity, Now: func() time.Time { return now }, Scan: func(ctx context.Context) health.Snapshot { scans.Add(1); return health.Inspect(ctx, d) }, Plan: func(s health.Snapshot, n time.Time) health.Plan { return s.Plan(n) }, Probe: func(ctx context.Context, s health.Snapshot, p health.Plan) health.Report {
				probeContext <- ctx
				return s.Probe(ctx, p, health.NetworkIO{Roots: x509.NewCertPool(), Now: func() time.Time { return now }, Dial: func(context.Context, string, string) (net.Conn, error) { return wire, nil }, Resolve: func(context.Context, string, string) ([]net.IP, error) { t.Error("unexpected DNS"); return nil, nil }})
			}, Close: cancel}
			f := &fixture{bootstrap: Token()}
			f.h = New("127.0.0.1:43210", f.bootstrap, a)
			f.login(t)
			f.request("/api/scan", "{}", nil)
			id := planID(t, f)
			reqCtx, reqCancel := context.WithCancel(context.Background())
			defer reqCancel()
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/probe", strings.NewReader(fmt.Sprintf(`{"plan_id":%q}`, id))).WithContext(reqCtx)
			request.Header.Set("Origin", "http://127.0.0.1:43210")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Session", f.session)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { f.h.ServeHTTP(response, request); close(done) }()
			select {
			case <-wire.wrote:
			case <-time.After(time.Second):
				t.Fatal("CONNECT not started")
			}
			runningContext := <-probeContext
			switch cause {
			case "deadline_context":
				cancel()
			case "expire":
				f.h.Expire()
			case "page_close":
				if f.request("/api/close", "{}", nil).Code != 200 {
					t.Fatal("close")
				}
			case "request_cancel":
				reqCancel()
			}
			select {
			case <-runningContext.Done():
			case <-time.After(time.Second):
				t.Fatal("activity context not cancelled")
			}
			close(wire.release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("probe did not end")
			}
			var report health.Report
			if json.Unmarshal(response.Body.Bytes(), &report) != nil {
				t.Fatal("report")
			}
			if report.Probe == nil || report.Probe.State != "cancelled" || report.Probe.Attempts.Connect != 1 || report.Probe.Attempts.TargetTLS != 0 || report.Probe.Attempts.Head != 0 || wire.writes.Load() != 1 {
				t.Fatalf("cancelled CONNECT advanced: %+v writes=%d", report.Probe, wire.writes.Load())
			}
			if cause != "request_cancel" {
				if f.request("/api/scan", "{}", nil).Code != 410 || scans.Load() != 1 {
					t.Fatal("expired activity accepted")
				}
			}
			if report.ReferenceCompleted() {
				t.Fatal("cancelled probe marked complete")
			}
			// Bytes already sent are still recorded; cancellation does not erase them.
			if report.Probe.StartedAt == nil || report.Probe.Attempts.TCP != 1 {
				t.Fatal("prior request concealed")
			}
		})
	}
}
