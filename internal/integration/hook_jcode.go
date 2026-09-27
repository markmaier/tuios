package integration

import "strings"

// The jcode event map. Source: jcode's docs/HOOKS.md and docs/HERDR.md, and
// crates/jcode-base/src/hooks.rs, crates/jcode-terminal-launch/src/lib.rs and
// crates/jcode-app-core/src/agent{.rs,/turn_execution.rs} in
// github.com/1jehuang/jcode at v0.88.0, read for this change. Hooks are keys
// of [hooks] in ~/.jcode/config.toml (or $JCODE_HOME/config.toml), each one
// command or a list of them; the lists need jcode 0.81.5 or newer.
//
// jcode differs from every other harness here in three ways.
//
// Its hooks run in the server, not in the pane. Every jcode client talks to
// one `jcode serve` daemon, started detached by the first client, and the
// hooks are that daemon's children. Their environment is the daemon's, which
// is the environment of whatever pane started it, and their ancestors lead to
// that pane too. So TUIOS_PANE_ID, TUIOS_SESSION and TUIOS_AGENT in a hook's
// environment name the pane that started the server, whichever pane the event
// is for, and resolve-pane would place every hook there. None of them is read.
//
// What does name the pane is the client's own terminal identity, which jcode
// snapshots from each client, forwards to that client's hooks, and repeats
// under a JCODE_CLIENT_ prefix so a hook can tell it from the daemon's
// inherited values. The list of names it forwards is fixed
// (CLIENT_TERMINAL_ENV_VARS). It holds herdr's variables, and tuios gives a
// pane that starts jcode herdr's HERDR_ENV, HERDR_SOCKET_PATH and HERDR_PANE_ID
// (session.Manager.HerdrEnv), so JCODE_CLIENT_HERDR_PANE_ID is the pane's
// window id when JCODE_CLIENT_HERDR_SOCKET_PATH is tuios's own herdr socket.
// A jcode whose list also holds TUIOS_PANE_ID gives JCODE_CLIENT_TUIOS_PANE_ID,
// which is preferred. JCodePane reads both.
//
// Its observer hooks get no stdin. The payload is JCODE_HOOK_PAYLOAD, a JSON
// object that mirrors the JCODE_HOOK_* variables: event, session_id, cwd and
// the event's fields in lower case.
//
// It has no event for a prompt that waits on the person, and needs none: an
// interactive jcode never waits on the person mid-turn. Its destructive-command
// gate answers the model, and its permission requests belong to ambient mode,
// which runs with no client and is reviewed outside the pane.
//
//	session_start   idle, with the session id (source create, attach or
//	                resume; each fires as a session's agent is built, never
//	                mid-turn)
//	turn_start      working
//	turn_end ok     done, with the first line of what the agent said last
//	turn_end error  errored, with the first line of the error
//	session_end     none
//
// An interrupted turn ends with status ok and so reports done. A turn cut short
// by a crash reports nothing; the pane's process detector clears the claim
// when jcode leaves the foreground.
func translateJCode(in Input, p fields) Decision {
	event := eventName(in, p)
	r := Report{SessionID: p.str("session_id")}
	switch event {
	case "session_start":
		r.State = "idle"
	case "turn_start":
		r.State = "working"
	case "turn_end":
		switch p.str("status") {
		case "ok":
			r.State = "done"
			text := activityText(p.str("last_assistant_text"))
			if text != "" {
				r.Message = text
			}
			r.Activity = &Activity{Event: ActivityTurnEnd, Text: text, Model: Clip(p.str("model"))}
		case "error":
			r.State = "errored"
			r.Message = "the turn ended on an error"
			if e := Clip(firstLine(p.str("error"))); e != "" {
				r.Message = e
			}
		default:
			return skip(JCode, event, "turn_end status "+p.str("status")+" is not a state change")
		}
	case "session_end":
		r.State = "none"
	case "":
		return skip(JCode, event, "the payload names no event")
	default:
		return skip(JCode, event, "event not mapped")
	}
	return send(JCode, event, r)
}

// jcodePayloadEnv is where jcode puts a hook's payload.
const jcodePayloadEnv = "JCODE_HOOK_PAYLOAD"

// agentHint is the TUIOS_AGENT that speaks for the pane a hook reports for.
// A jcode hook's own TUIOS_AGENT is the jcode server's, inherited from the
// pane that started it, so only the value jcode forwarded from the client
// counts.
func agentHint(id string, in Input) string {
	if id == JCode {
		return in.env("JCODE_CLIENT_" + AgentHintEnv)
	}
	return in.env(AgentHintEnv)
}

// JCodePane is the pane a jcode hook reports for, from the terminal identity
// jcode forwarded from the client (see translateJCode): TUIOS_PANE_ID and
// TUIOS_SESSION when jcode forwards them, else HERDR_PANE_ID when HERDR_ENV
// is set and HERDR_SOCKET_PATH is herdrSocket, the herdr socket of the tuios
// daemon the hook reports to. session is empty when only the window is known;
// a window id names one window across every session. by says which route
// matched, and window is empty when neither did.
func JCodePane(getenv func(string) string, herdrSocket string) (session, window, by string) {
	client := func(name string) string { return strings.TrimSpace(getenv("JCODE_CLIENT_" + name)) }
	if id := client("TUIOS_PANE_ID"); id != "" {
		return client("TUIOS_SESSION"), id, "jcode-client"
	}
	if herdrSocket == "" || client("HERDR_ENV") != "1" || client("HERDR_SOCKET_PATH") != herdrSocket {
		return "", "", ""
	}
	if id := client("HERDR_PANE_ID"); id != "" {
		return "", id, "jcode-herdr"
	}
	return "", "", ""
}
