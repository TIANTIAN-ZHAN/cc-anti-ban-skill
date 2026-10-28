package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"cc-anti-ban-skill/internal/health"
)

func readyPlan(t *testing.T) (*fixture, string) {
	t.Helper()
	f := setup(t)
	f.login(t)
	if f.request("/api/scan", "{}", nil).Code != 200 {
		t.Fatal("scan failed")
	}
	return f, planID(t, f)
}

func assertCancellation(t *testing.T, w *httptest.ResponseRecorder, status int, state string, cancelled bool) {
	t.Helper()
	var result struct {
		State     string `json:"state"`
		Cancelled bool   `json:"cancelled"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != status || result.State != state || result.Cancelled != cancelled {
		t.Fatalf("wrong cancellation result: code=%d state=%s cancelled=%v", w.Code, result.State, result.Cancelled)
	}
}

func TestCancelRevokesPlanAndReplay(t *testing.T) {
	f, id := readyPlan(t)
	body := fmt.Sprintf(`{"plan_id":%q}`, id)
	if f.request("/api/cancel", `{}`, nil).Code != 400 {
		t.Fatal("missing plan accepted")
	}
	assertCancellation(t, f.request("/api/cancel", `{"plan_id":"wrong"}`, nil), 409, "not_current", false)
	for i := 0; i < 2; i++ {
		assertCancellation(t, f.request("/api/cancel", body, nil), 200, "cancelled", true)
		if f.request("/api/probe", body, nil).Code != 409 || f.probes.Load() != 0 {
			t.Fatal("cancelled plan reached executor")
		}
	}
	newID := planID(t, f)
	if newID == id {
		t.Fatal("plan ID reused")
	}
	assertCancellation(t, f.request("/api/cancel", body, nil), 409, "not_current", false)
	if f.request("/api/probe", body, nil).Code != 409 {
		t.Fatal("old plan revived after replan")
	}
	if f.request("/api/probe", fmt.Sprintf(`{"plan_id":%q}`, newID), nil).Code != 200 || f.probes.Load() != 1 {
		t.Fatal("fresh plan unusable")
	}
	assertCancellation(t, f.request("/api/cancel", fmt.Sprintf(`{"plan_id":%q}`, newID), nil), 409, "completed", false)
}

func TestCancelExpiredAndInvalidatedPlans(t *testing.T) {
	f, id := readyPlan(t)
	body := fmt.Sprintf(`{"plan_id":%q}`, id)
	f.now = f.now.Add(time.Minute) // Exact expiry is already expired.
	for i := 0; i < 2; i++ {
		assertCancellation(t, f.request("/api/cancel", body, nil), 200, "expired", false)
	}
	if f.request("/api/probe", body, nil).Code != 409 || f.probes.Load() != 0 {
		t.Fatal("expired plan executed")
	}
	_ = f.request("/api/scan", `{}`, nil)
	assertCancellation(t, f.request("/api/cancel", body, nil), 409, "not_current", false)
}

func TestCancelCannotClaimRunningOperationRevoked(t *testing.T) {
	f, id := readyPlan(t)
	body := fmt.Sprintf(`{"plan_id":%q}`, id)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan int, 1)
	f.h.actions.Probe = func(_ context.Context, s health.Snapshot, _ health.Plan) health.Report {
		f.probes.Add(1)
		close(started)
		<-release
		return s.Report
	}
	go func() { done <- f.request("/api/probe", body, nil).Code }()
	<-started
	assertCancellation(t, f.request("/api/cancel", body, nil), 409, "running", false)
	if f.request("/api/probe", body, nil).Code != 409 || f.request("/api/plan", `{}`, nil).Code != 409 {
		t.Error("busy operation reused")
	}
	close(release)
	if <-done != 200 || f.probes.Load() != 1 {
		t.Fatal("unexpected execution count")
	}
	assertCancellation(t, f.request("/api/cancel", body, nil), 409, "completed", false)
}

func TestCancelProbeAtomicRace(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		f, id := readyPlan(t)
		body := fmt.Sprintf(`{"plan_id":%q}`, id)
		gate := make(chan struct{})
		cancelled, probed := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
		go func() { <-gate; cancelled <- f.request("/api/cancel", body, nil) }()
		go func() { <-gate; probed <- f.request("/api/probe", body, nil) }()
		close(gate)
		cw, pw := <-cancelled, <-probed
		if cw.Code == 200 {
			assertCancellation(t, cw, 200, "cancelled", true)
			if pw.Code != 409 || f.probes.Load() != 0 {
				t.Fatal("cancellation acknowledged after executor started")
			}
		} else {
			var result struct {
				State     string `json:"state"`
				Cancelled bool   `json:"cancelled"`
			}
			if json.Unmarshal(cw.Body.Bytes(), &result) != nil || cw.Code != 409 || result.Cancelled || (result.State != "running" && result.State != "completed") || pw.Code != 200 || f.probes.Load() != 1 {
				t.Fatal("execution winner reported revoked or replayed")
			}
		}
		if f.request("/api/probe", body, nil).Code != 409 {
			t.Fatal("consumed race plan replayed")
		}
	}
}

func TestPlanStateQueryNeverExecutesAndRecoversLostCancelReply(t *testing.T) {
	f, id := readyPlan(t)
	body := fmt.Sprintf(`{"plan_id":%q}`, id)
	assertCancellation(t, f.request("/api/plan-status", body, nil), 200, "ready", false)
	if f.probes.Load() != 0 {
		t.Fatal("state query executed probe")
	}
	_ = f.request("/api/cancel", body, nil) // Simulate the caller losing this response.
	assertCancellation(t, f.request("/api/plan-status", body, nil), 200, "cancelled", true)
	assertCancellation(t, f.request("/api/cancel", body, nil), 200, "cancelled", true)
	if f.request("/api/probe", body, nil).Code != 409 || f.probes.Load() != 0 {
		t.Fatal("query revived cancelled authorization")
	}
}
