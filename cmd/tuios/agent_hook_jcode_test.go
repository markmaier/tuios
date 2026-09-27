package main

import (
	"strings"
	"testing"
)

// jcodeServerEnv is what a jcode hook inherits from jcode's shared server: the
// environment of the pane that started the server, which is not the pane the
// event is for.
func jcodeServerEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"TUIOS_PANE_ID":      "w-server",
		"TUIOS_SESSION":      "first",
		"JCODE_HOOK_EVENT":   "turn_start",
		"JCODE_HOOK_PAYLOAD": `{"event":"turn_start","session_id":"session_fox_1","model":"m"}`,
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// TestAgentHookJCodeReportsForTheClientsPane: the pane comes from the herdr
// variables jcode forwarded from the client, the payload from the
// environment, and nothing the server's own environment or ancestors say is
// used.
func TestAgentHookJCodeReportsForTheClientsPane(t *testing.T) {
	h := &hookRun{
		herdrSock: "/run/tuios.sock.herdr",
		env: jcodeServerEnv(map[string]string{
			"JCODE_CLIENT_HERDR_ENV":         "1",
			"JCODE_CLIENT_HERDR_SOCKET_PATH": "/run/tuios.sock.herdr",
			"JCODE_CLIENT_HERDR_PANE_ID":     "w-client",
		}),
	}
	h.run(t, agentHookOptions{}, "", "jcode")

	for _, c := range h.daemon.calls {
		if c.verb == "resolve-pane" {
			t.Fatalf("a jcode hook asked resolve-pane, which places it in the server's pane: %v", c.params)
		}
	}
	r := h.daemon.reports()
	if len(r) != 1 {
		t.Fatalf("reports = %v\n%s", r, h.stderr.String())
	}
	if r[0]["window"] != "w-client" || r[0]["session"] != "" || r[0]["state"] != "working" {
		t.Fatalf("report went to %v", r[0])
	}
	if _, ok := r[0]["harness_pid"]; ok {
		t.Fatalf("a jcode report named the server as its harness process: %v", r[0])
	}
	if !strings.Contains(h.stderr.String(), `"pane_by":"jcode-herdr"`) {
		t.Fatalf("explain = %s", h.stderr.String())
	}
}

// TestAgentHookJCodePrefersTheClientsTuiosPane: a jcode that forwards
// TUIOS_PANE_ID and TUIOS_SESSION names the pane outright.
func TestAgentHookJCodePrefersTheClientsTuiosPane(t *testing.T) {
	h := &hookRun{
		herdrSock: "/run/tuios.sock.herdr",
		env: jcodeServerEnv(map[string]string{
			"JCODE_CLIENT_TUIOS_PANE_ID":     "w-tuios",
			"JCODE_CLIENT_TUIOS_SESSION":     "second",
			"JCODE_CLIENT_HERDR_ENV":         "1",
			"JCODE_CLIENT_HERDR_SOCKET_PATH": "/run/tuios.sock.herdr",
			"JCODE_CLIENT_HERDR_PANE_ID":     "w-herdr",
		}),
	}
	h.run(t, agentHookOptions{}, "", "jcode")
	r := h.daemon.reports()
	if len(r) != 1 || r[0]["window"] != "w-tuios" || r[0]["session"] != "second" {
		t.Fatalf("reports = %v", r)
	}
}

// TestAgentHookJCodeWithNoClientPaneReportsNothing: without the client's
// identity there is no pane, and the server's TUIOS_PANE_ID is never taken
// for one. A herdr pane id that belongs to a real herdr is not tuios's.
func TestAgentHookJCodeWithNoClientPaneReportsNothing(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"nothing forwarded": {},
		"a real herdr's pane": {
			"JCODE_CLIENT_HERDR_ENV":         "1",
			"JCODE_CLIENT_HERDR_SOCKET_PATH": "/run/user/1000/herdr.sock",
			"JCODE_CLIENT_HERDR_PANE_ID":     "herdr-pane-3",
		},
		"herdr's pane id without HERDR_ENV": {
			"JCODE_CLIENT_HERDR_SOCKET_PATH": "/run/tuios.sock.herdr",
			"JCODE_CLIENT_HERDR_PANE_ID":     "w-client",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := &hookRun{herdrSock: "/run/tuios.sock.herdr", env: jcodeServerEnv(extra)}
			h.run(t, agentHookOptions{}, "", "jcode")
			if r := h.daemon.reports(); len(r) != 0 {
				t.Fatalf("reported %v", r)
			}
			if !strings.Contains(h.stderr.String(), "no pane") {
				t.Fatalf("explain = %s", h.stderr.String())
			}
		})
	}
}
