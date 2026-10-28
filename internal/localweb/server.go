// Package localweb exposes fixed consent-bound actions to one loopback session.
package localweb

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"cc-anti-ban-skill/internal/execution"
	"cc-anti-ban-skill/internal/health"
)

//go:embed web/*
var assets embed.FS

type Actions struct {
	Scan            func(context.Context) health.Snapshot
	Plan            func(health.Snapshot, time.Time) health.Plan
	Probe           func(context.Context, health.Snapshot, health.Plan) health.Report
	Close           func()
	Now             func() time.Time
	Execution       *execution.Manager
	ActivityContext context.Context
}

type Handler struct {
	host, origin, bootstrap, session string
	mu                               sync.Mutex
	actions                          Actions
	snapshot                         *health.Snapshot
	plan                             *health.Plan
	planState                        string
	busy                             bool
	closed                           bool
	cleanupOnly                      bool
	activity                         context.Context
	cancelActivity                   context.CancelFunc
	repair                           *execution.Plan
	repairState                      string
}

func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}

func New(host, bootstrap string, a Actions) *Handler {
	parent := a.ActivityContext
	if parent == nil {
		parent = context.Background()
	}
	activity, cancel := context.WithCancel(parent)
	return &Handler{host: host, origin: "http://" + host, bootstrap: bootstrap, actions: a, activity: activity, cancelActivity: cancel}
}
func equal(a, b string) bool {
	return a != "" && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.Host != h.host || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.origin {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method rejected", http.StatusMethodNotAllowed)
			return
		}
		name, kind := "", ""
		switch r.URL.Path {
		case "/":
			name = "index.html"
			kind = "text/html; charset=utf-8"
		case "/app.js":
			name = "app.js"
			kind = "text/javascript; charset=utf-8"
		case "/style.css":
			name = "style.css"
			kind = "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		data, _ := assets.ReadFile("web/" + name)
		w.Header().Set("Content-Type", kind)
		_, _ = w.Write(data)
		return
	}
	// Origin is mandatory even for reads and session establishment. There is no
	// cookie fallback, query token, CORS, form endpoint, or missing-Origin exception.
	if r.Method != http.MethodPost {
		http.Error(w, "method rejected", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != h.origin {
		http.Error(w, "origin rejected", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		http.Error(w, "site rejected", http.StatusForbidden)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "content type rejected", http.StatusUnsupportedMediaType)
		return
	}
	var input struct {
		PlanID   string `json:"plan_id,omitempty"`
		Loopback string `json:"loopback,omitempty"`
		Port     int    `json:"port,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "body rejected", http.StatusBadRequest)
		return
	}
	acceptsID := r.URL.Path == "/api/probe" || r.URL.Path == "/api/cancel" || r.URL.Path == "/api/plan-status" || r.URL.Path == "/api/repair-plan-status" || r.URL.Path == "/api/repair-start" || r.URL.Path == "/api/repair-cancel" || r.URL.Path == "/api/repair-status" || r.URL.Path == "/api/repair-stop" || r.URL.Path == "/api/repair-cleanup"
	if (!acceptsID && input.PlanID != "") || (r.URL.Path != "/api/repair-plan" && (input.Loopback != "" || input.Port != 0)) {
		http.Error(w, "body rejected", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.activity.Err() != nil {
		h.expireLocked()
	}
	if h.closed {
		http.Error(w, "session closed", http.StatusGone)
		return
	}
	if r.URL.Path == "/api/session" {
		if h.cleanupOnly {
			http.Error(w, "session expired", http.StatusGone)
			return
		}
		if !equal(h.bootstrap, r.Header.Get("X-Bootstrap")) {
			http.Error(w, "session rejected", http.StatusForbidden)
			return
		}
		candidate := Token()
		// The activity deadline may have elapsed while entropy was being acquired.
		if h.activity.Err() != nil {
			h.expireLocked()
			http.Error(w, "session expired", http.StatusGone)
			return
		}
		h.bootstrap = ""
		h.session = candidate
		writeJSON(w, map[string]string{"session": h.session})
		return
	}
	if !equal(h.session, r.Header.Get("X-Session")) {
		http.Error(w, "session rejected", http.StatusForbidden)
		return
	}
	if h.cleanupOnly && r.URL.Path != "/api/repair-status" && r.URL.Path != "/api/repair-cleanup" && r.URL.Path != "/api/close" {
		http.Error(w, "session expired; cleanup only", http.StatusGone)
		return
	}
	switch r.URL.Path {
	case "/api/scan":
		if h.busy || h.executionActive() {
			http.Error(w, "operation in progress", http.StatusConflict)
			return
		}
		h.plan = nil
		h.planState = "invalidated"
		h.discardRepair()
		h.busy = true
		activity, finishActivity := h.operationContext(r.Context())
		s := func() health.Snapshot {
			h.mu.Unlock()
			defer h.mu.Lock()
			defer finishActivity()
			return h.actions.Scan(activity)
		}()
		h.busy = false
		if h.closed {
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		h.snapshot = &s
		s.Report.RepairAvailable = h.actions.Execution != nil
		writeJSON(w, s.Report)
	case "/api/plan":
		if h.busy || h.executionActive() {
			http.Error(w, "operation in progress", http.StatusConflict)
			return
		}
		if h.snapshot == nil {
			http.Error(w, "scan required", http.StatusConflict)
			return
		}
		p := h.actions.Plan(*h.snapshot, h.actions.Now())
		h.discardRepair()
		h.plan = &p
		h.planState = "ready"
		writeJSON(w, p)
	case "/api/cancel":
		if input.PlanID == "" {
			http.Error(w, "plan id required", http.StatusBadRequest)
			return
		}
		if h.plan == nil || !equal(h.plan.ID, input.PlanID) {
			writePlanState(w, http.StatusConflict, "not_current", false)
			return
		}
		if h.planState == "ready" && !h.actions.Now().Before(h.plan.ExpiresAt) {
			h.planState = "expired"
		}
		switch h.planState {
		case "ready":
			h.planState = "cancelled"
			writePlanState(w, http.StatusOK, "cancelled", true)
		case "cancelled", "expired":
			writePlanState(w, http.StatusOK, h.planState, h.planState == "cancelled")
		default:
			writePlanState(w, http.StatusConflict, h.planState, false)
		}
	case "/api/plan-status":
		if h.plan == nil || !equal(h.plan.ID, input.PlanID) {
			writePlanState(w, 409, "not_current", false)
			return
		}
		if h.planState == "ready" && !h.actions.Now().Before(h.plan.ExpiresAt) {
			h.planState = "expired"
		}
		writePlanState(w, 200, h.planState, h.planState == "cancelled")
	case "/api/probe":
		if h.plan != nil && h.planState == "ready" && !h.actions.Now().Before(h.plan.ExpiresAt) {
			h.planState = "expired"
		}
		if h.busy || h.executionActive() || h.plan == nil || h.snapshot == nil || !equal(h.plan.ID, input.PlanID) || !h.plan.Allowed || h.planState != "ready" {
			http.Error(w, "fresh consent plan required", http.StatusConflict)
			return
		}
		p := *h.plan
		s := *h.snapshot
		h.planState = "running"
		h.busy = true // atomically consume before any network activity
		activity, finishActivity := h.operationContext(r.Context())
		result := func() health.Report {
			h.mu.Unlock()
			defer h.mu.Lock()
			defer finishActivity()
			return h.actions.Probe(activity, s, p)
		}()
		h.busy = false
		s.Report = result
		if !h.closed && !h.cleanupOnly {
			h.planState = "completed"
			if result.Probe != nil {
				h.planState = result.Probe.State
			}
			s.Report = result
			s.Report.RepairAvailable = h.actions.Execution != nil
			h.snapshot = &s
		}
		writeJSON(w, s.Report)
	case "/api/repair-plan":
		if h.actions.Execution == nil || h.snapshot == nil || h.busy || h.executionActive() {
			http.Error(w, "repair unavailable", http.StatusConflict)
			return
		}
		h.discardRepair()
		p := h.actions.Execution.Prepare(*h.snapshot, input.Loopback, input.Port)
		h.plan = nil
		h.planState = "invalidated"
		h.repair = &p
		h.repairState = "ready"
		writeJSON(w, p)
	case "/api/repair-cancel":
		if h.repair == nil || !equal(h.repair.ID, input.PlanID) {
			writePlanState(w, 409, "not_current", false)
			return
		}
		if h.repairState == "ready" && !h.actions.Now().Before(h.repair.ExpiresAt) {
			h.repairState = "expired"
			h.repair.Close()
		}
		if h.repairState == "ready" {
			h.repairState = "cancelled"
			h.repair.Close()
		}
		if h.repairState == "cancelled" || h.repairState == "expired" {
			writePlanState(w, 200, h.repairState, h.repairState == "cancelled")
		} else {
			writePlanState(w, 409, "execution_started", false)
		}
	case "/api/repair-plan-status":
		if h.repair == nil || !equal(h.repair.ID, input.PlanID) {
			writePlanState(w, 409, "not_current", false)
			return
		}
		if h.repairState == "ready" && !h.actions.Now().Before(h.repair.ExpiresAt) {
			h.repairState = "expired"
			h.repair.Close()
		}
		writePlanState(w, 200, h.repairState, h.repairState == "cancelled")
	case "/api/repair-start":
		if h.repair != nil && equal(h.repair.ID, input.PlanID) && h.repairState == "ready" && !h.actions.Now().Before(h.repair.ExpiresAt) {
			h.repairState = "expired"
			h.repair.Close()
		}
		if h.actions.Execution == nil || h.repair == nil || h.busy || !equal(h.repair.ID, input.PlanID) || h.repairState != "ready" || !h.repair.Allowed || !h.actions.Now().Before(h.repair.ExpiresAt) {
			http.Error(w, "fresh repair plan required", 409)
			return
		}
		h.repairState = "consumed" // Consume atomically before any OS launch or probe.
		status, err := h.actions.Execution.Accept(*h.repair)
		if err != nil {
			http.Error(w, "repair unavailable", 409)
			return
		}
		writeJSON(w, status)
	case "/api/repair-status", "/api/repair-stop", "/api/repair-cleanup":
		if h.actions.Execution == nil {
			http.Error(w, "repair unavailable", 409)
			return
		}
		var status execution.Status
		var err error
		if r.URL.Path == "/api/repair-stop" {
			status, err = h.actions.Execution.Stop(input.PlanID)
		} else if r.URL.Path == "/api/repair-cleanup" {
			status, err = h.actions.Execution.RetryCleanup(input.PlanID)
		} else {
			status, err = h.actions.Execution.Status(input.PlanID)
		}
		if err != nil {
			http.Error(w, "operation unavailable", 409)
			return
		}
		writeJSON(w, status)
	case "/api/close":
		h.cancelActivity()
		if h.actions.Execution != nil && h.actions.Execution.ResourcesPending() {
			h.cleanupOnly = true
			h.discardRepair()
			h.actions.Execution.Close()
			writeJSON(w, map[string]any{"closed": false, "cleanup_only": true, "operation_id": h.actions.Execution.CurrentID()})
			h.actions.Close()
			return
		}
		h.closed = true
		h.session = ""
		h.snapshot = nil
		h.plan = nil
		h.planState = "invalidated"
		h.discardRepair()
		if h.actions.Execution != nil {
			h.actions.Execution.Close()
		}
		writeJSON(w, map[string]bool{"closed": true})
		h.actions.Close()
	default:
		http.NotFound(w, r)
	}
}

// Expiry revokes launch/network authorization but preserves the existing
// authenticated loopback channel while owned resource cleanup remains unresolved.
func (h *Handler) Expire() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked()
}

func (h *Handler) expireLocked() {
	h.cancelActivity()
	h.cleanupOnly = true
	h.bootstrap = ""
	h.plan = nil
	h.planState = "expired"
	h.discardRepair()
	if h.actions.Execution != nil {
		h.actions.Execution.Close()
	}
}

// Activity cancellation is independent of the authenticated cleanup connection.
func (h *Handler) operationContext(request context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(h.activity)
	stop := context.AfterFunc(request, cancel)
	if request.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (h *Handler) executionActive() bool {
	return h.actions.Execution != nil && h.actions.Execution.Active()
}

func (h *Handler) discardRepair() {
	if h.repair != nil && h.repairState != "consumed" {
		h.repair.Close()
	}
	h.repair = nil
	h.repairState = "invalidated"
}

func writePlanState(w http.ResponseWriter, status int, state string, cancelled bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "cancelled": cancelled})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
