package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func serverWithToolBudget(t *testing.T, n int) Server {
	t.Helper()
	var s Server
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"name":"rea","transport":"http","url":"http://127.0.0.1:1/mcp","agents":[],"toolTimeout":%d}`, n)), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServerToolBudgetRoundTripAndHub(t *testing.T) {
	sandbox(t)
	s := serverWithToolBudget(t, 450)
	if _, err := SaveServer("", s); err != nil {
		t.Fatal(err)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s = *l.server("rea")
	if err := s.check(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"toolTimeout":450`) {
		t.Fatalf("configured call budget was discarded: %s", b)
	}
	// Decode the gateway-facing representation to assert propagation without
	// referring to a newly added field on the old code's type.
	b, err = json.Marshal(hubServerOf(&s))
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(b, &card); err != nil {
		t.Fatal(err)
	}
	if got := card["ToolTimeout"]; got != float64(450*time.Second) {
		t.Fatalf("hub received budget %v, want 450s", got)
	}
}

func TestServerToolBudgetPreservesHostWaitOverrides(t *testing.T) {
	s := serverWithToolBudget(t, 450)
	s.Name = HubEntry
	for _, tc := range []struct {
		format mcpFormat
		key    string
		value  int
	}{
		{fmtCodex, "tool_timeout_sec", 500},
		{fmtDsh, "toolCallTimeoutMs", 500000},
		{fmtHermes, "timeout", 500},
	} {
		f := &mcpFile{Format: tc.format}
		b, err := json.Marshal(f.merged(&s, map[string]any{tc.key: tc.value}))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got[tc.key] != float64(tc.value) {
			t.Errorf("%s override discarded by sync: %s", tc.key, b)
		}
	}
}

func TestServerToolBudgetBoundsAndLegacyDefault(t *testing.T) {
	for _, n := range []int{-1, 86401} {
		s := serverWithToolBudget(t, n)
		if err := s.check(); err == nil {
			t.Errorf("unbounded/invalid timeout %d was accepted", n)
		}
	}
	for _, n := range []int{0, 1, 450, 86400} {
		s := serverWithToolBudget(t, n)
		if err := s.check(); err != nil {
			t.Errorf("valid timeout %d rejected: %v", n, err)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 && strings.Contains(string(b), "toolTimeout") {
			t.Errorf("legacy no-budget config acquired a field: %s", b)
		}
	}
}
