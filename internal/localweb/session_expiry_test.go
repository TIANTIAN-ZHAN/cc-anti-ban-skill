package localweb

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestUnconsumedBootstrapCannotCreateSessionAfterExpiry(t *testing.T) {
	for _, trigger := range []string{"explicit_expire", "activity_deadline"} {
		t.Run(trigger, func(t *testing.T) {
			f := setup(t)
			if trigger == "activity_deadline" {
				// Expired parent simulates the authority clock even before Expire's worker runs.
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				actions := f.h.actions
				actions.ActivityContext = ctx
				f.h = New(f.h.host, f.bootstrap, actions)
			} else {
				f.now = f.now.Add(16 * time.Minute)
				f.h.Expire()
			}
			if w := f.request("/api/session", "{}", nil); w.Code != 410 {
				t.Fatalf("expired bootstrap accepted: %d", w.Code)
			}
			f.h.mu.Lock()
			bootstrap, session := f.h.bootstrap, f.h.session
			f.h.mu.Unlock()
			if bootstrap != "" || session != "" {
				t.Fatal("expired bootstrap/session retained")
			}
			if f.scans.Load() != 0 || f.probes.Load() != 0 {
				t.Fatal("expiry performed activity")
			}
		})
	}
}

func TestExpiryKeepsExistingSessionAndRejectsNewBootstrap(t *testing.T) {
	f := setup(t)
	f.login(t)
	session := f.session
	f.now = f.now.Add(16 * time.Minute)
	f.h.Expire()
	if w := f.request("/api/session", "{}", nil); w.Code != 410 {
		t.Fatal("new session after expiry")
	}
	// No execution exists in this fixture. 409 proves the original session passed
	// auth and reached only the cleanup/status handler, rather than being revoked.
	if w := f.request("/api/repair-status", `{"plan_id":"original-operation"}`, nil); w.Code != 409 {
		t.Fatal("existing session revoked", w.Code)
	}
	f.h.mu.Lock()
	same := f.h.session == session
	f.h.mu.Unlock()
	if !same {
		t.Fatal("session replaced")
	}
	if f.request("/api/scan", "{}", nil).Code != 410 {
		t.Fatal("normal action survives expiry")
	}
}

func TestSessionAndExpiryHaveOneAtomicOrder(t *testing.T) {
	for i := 0; i < 32; i++ {
		f := setup(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var code int
		var issued string
		go func() {
			defer wg.Done()
			<-start
			w := f.request("/api/session", "{}", nil)
			code = w.Code
			if code == 200 {
				var result map[string]string
				json.Unmarshal(w.Body.Bytes(), &result)
				issued = result["session"]
			}
		}()
		go func() { defer wg.Done(); <-start; f.h.Expire() }()
		close(start)
		wg.Wait()
		f.h.mu.Lock()
		stored, bootstrap, expired := f.h.session, f.h.bootstrap, f.h.cleanupOnly
		f.h.mu.Unlock()
		if !expired || bootstrap != "" {
			t.Fatal("expiry did not revoke bootstrap")
		}
		switch code {
		case 200:
			if issued == "" || stored != issued {
				t.Fatal("pre-expiry session lost")
			}
		case 410:
			if stored != "" {
				t.Fatal("post-expiry session created")
			}
		default:
			t.Fatal("unexpected race result", code)
		}
		if f.request("/api/session", "{}", nil).Code != 410 {
			t.Fatal("old bootstrap reused after race")
		}
	}
}
