package health

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAllLoaderNamesArePresenceOnlyAndBlockCandidate(t *testing.T) {
	for _, key := range []string{"DYLD_PRINT_ENV", "DYLD_PRINT_TO_FILE", "DYLD_VERSIONED_LIBRARY_PATH", "DYLD_VERSIONED_FRAMEWORK_PATH", "DYLD_FUTURE_CONTROL", "DYLD_"} {
		t.Run(key, func(t *testing.T) {
			d := fixtureIO(t)
			d.Env = environmentSnapshot(func(k string) (string, bool) {
				if strings.HasPrefix(k, "DYLD_") {
					t.Fatal("loader value lookup")
				}
				return "", false
			}, key+"=FAKE_PRIVATE_LOADER_VALUE")
			if d.Env[key] != "present" {
				t.Fatal("presence lost")
			}
			s := Inspect(context.Background(), d)
			if _, why := s.CandidateReference("http://127.0.0.1:4567", testTime); why != "external_loader_policy_unresolved" {
				t.Fatal("loader accepted")
			}
			raw, _ := json.Marshal(s.Report)
			if strings.Contains(string(raw), "FAKE_PRIVATE_LOADER_VALUE") {
				t.Fatal("value exposed")
			}
			b, _ := json.Marshal(map[string]any{"env": map[string]string{key: ""}})
			if settingsMatchesCandidate(b, "http://127.0.0.1:4567") {
				t.Fatal("settings loader accepted")
			}
			if valid, override := settingsShape(b); !valid || !override {
				t.Fatal("settings presence lost")
			}
		})
	}
}
