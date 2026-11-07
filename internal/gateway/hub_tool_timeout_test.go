package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Decode as a caller of HubServer would, so these regressions also compile
// against the old implementation (where the unknown budget is ignored).
func budgetServer(t *testing.T, name string, budget time.Duration) HubServer {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"Name": name, "ToolTimeout": int64(budget)})
	var s HubServer
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func toolBudgetHub(t *testing.T, budget time.Duration) (*fakeHub, http.Handler) {
	t.Helper()
	fresh(t)
	old := hubCallWait
	hubCallWait = 25 * time.Millisecond
	t.Cleanup(func() { hubCallWait = old })
	f := &fakeHub{on: true,
		servers: map[string][]HubServer{"dsh": {budgetServer(t, "rea", budget), {Name: "ordinary"}}},
		tools: map[string][]HubTool{"dsh": {
			{Server: "rea", Tool: "work", Spec: hubSpec("work")},
			{Server: "ordinary", Tool: "work", Spec: hubSpec("work")},
		}},
	}
	withHub(t, f)
	return f, lanGuard(New().Handler())
}

func TestHubMCPConfiguredToolBudgetOutlivesDefault(t *testing.T) {
	f, h := toolBudgetHub(t, 2*time.Second)
	f.call = func(ctx context.Context, _, _ string) (json.RawMessage, error) {
		// 100ms is beyond the simulated old 25ms ceiling, with room before 2s.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return json.RawMessage(`{"content":[{"type":"text","text":"completed"}]}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m := readMCP(t, hubPost(h, "dsh", here, toolsCall("rea__work", `{}`)))
	if m.Result.IsError || len(m.Result.Content) == 0 || m.Result.Content[0].Text != "completed" {
		t.Fatalf("configured server was cut off at the legacy deadline: %+v", m)
	}
}

func TestHubMCPToolBudgetIsolationAndDefault(t *testing.T) {
	f, h := toolBudgetHub(t, 2*time.Second)
	f.call = func(ctx context.Context, server, _ string) (json.RawMessage, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("tool call has no finite deadline")
		}
		remaining := time.Until(deadline)
		if server == "rea" && (remaining < time.Second || remaining > 2*time.Second) {
			t.Errorf("REA budget = %v, want about 2s", remaining)
		}
		if server == "ordinary" && remaining > 25*time.Millisecond {
			t.Errorf("ordinary server inherited REA budget: %v", remaining)
		}
		return json.RawMessage(`{"content":[]}`), nil
	}
	for _, server := range []string{"rea", "ordinary"} {
		m := readMCP(t, hubPost(h, "dsh", here, toolsCall(server+"__work", `{}`)))
		if m.Result.IsError {
			t.Fatalf("%s: %+v", server, m)
		}
	}
}

func TestHubMCPToolBudgetExpiresAndNamesSelectedBudget(t *testing.T) {
	f, h := toolBudgetHub(t, 100*time.Millisecond)
	f.call = func(ctx context.Context, _, _ string) (json.RawMessage, error) { <-ctx.Done(); return nil, ctx.Err() }
	m := readMCP(t, hubPost(h, "dsh", here, toolsCall("rea__work", `{}`)))
	if !m.Result.IsError || m.Result.Content[0].Text != "The MCP server rea couldn't run work: it didn't answer in 100ms" {
		t.Fatalf("configured deadline was not enforced/named: %+v", m)
	}
}

func TestHubMCPToolBudgetKeepsCallerCancellation(t *testing.T) {
	f, h := toolBudgetHub(t, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var backendErr error
	f.call = func(call context.Context, _, _ string) (json.RawMessage, error) {
		cancel()
		<-call.Done()
		backendErr = call.Err()
		return nil, backendErr
	}
	r := httptest.NewRequest(http.MethodPost, HubMCPPath+"dsh", strings.NewReader(toolsCall("rea__work", `{}`))).WithContext(ctx)
	r.RemoteAddr, r.Host = here, "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if backendErr != context.Canceled {
		t.Fatalf("caller cancellation did not reach backend: %v", backendErr)
	}
	if strings.Contains(w.Body.String(), "didn't answer in") {
		t.Fatalf("caller cancellation was mislabeled as budget exhaustion: %s", w.Body.String())
	}
}
