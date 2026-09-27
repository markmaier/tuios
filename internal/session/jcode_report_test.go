package session

import (
	"fmt"
	"slices"
	"testing"
)

// TestAgentReportFindsTheWindowsSession: a report that names a window by its
// id and no session reaches that window's session, not the most recently
// active one. A jcode hook reports from jcode's shared server knowing only the
// window id (internal/integration/hook_jcode.go).
func TestAgentReportFindsTheWindowsSession(t *testing.T) {
	d, sp := startTestDaemon(t)
	target := makeSessionWithWindow(t, d, "target")
	other := makeSessionWithWindow(t, d, "other")
	other.TouchActive()
	id := target.GetState().Windows[0].ID
	c := dialVerb(t, sp)

	result(t, c.call(t, fmt.Sprintf(`{"verb":"set-agent-state","params":{"window":%q,"state":"working","harness":"jcode","agent_session_id":"session_fox_1"}}`, id)))
	if got := target.GetState().Windows[0].AgentState; got != AgentStateWorking {
		t.Fatalf("target window state = %v, want working", got)
	}
	if got := other.GetState().Windows[0].AgentState; got != AgentStateNone {
		t.Fatalf("the other session's window took the report: %v", got)
	}
	result(t, c.call(t, fmt.Sprintf(`{"verb":"set-agent-session","params":{"window":%q,"harness":"jcode","agent_session_id":"session_fox_1"}}`, id)))
}

// TestHerdrEnvForJCode: a pane that starts jcode is told the herdr
// variables jcode forwards to its hooks, and a shell pane is not.
func TestHerdrEnvForJCode(t *testing.T) {
	m := NewManager()
	m.SetHerdrSocket("/run/tuios.sock.herdr")
	want := []string{"HERDR_ENV=1", "HERDR_SOCKET_PATH=/run/tuios.sock.herdr", "HERDR_PANE_ID=w1"}
	for _, argv := range [][]string{{"jcode"}, {"/home/u/.local/bin/jcode", "--resume", "session_fox_1"}} {
		if got := m.HerdrEnv("w1", argv); !slices.Equal(got, want) {
			t.Errorf("HerdrEnv(%q) = %q, want %q", argv, got, want)
		}
	}
	for _, argv := range [][]string{nil, {"bash"}, {"jcode-notes"}} {
		if got := m.HerdrEnv("w1", argv); got != nil {
			t.Errorf("HerdrEnv(%q) = %q, want none", argv, got)
		}
	}
}
