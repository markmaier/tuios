package integration

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestJCodeHooksRoundTrip holds the jcode writer to its promise: install adds
// tuios's command to each event in the shape the value already has, and
// uninstall gives back the file byte for byte.
func TestJCodeHooksRoundTrip(t *testing.T) {
	tg := mustTarget(t, JCode)
	cmd := HookCommand("tuios", JCode, tg.Version)
	cases := []struct {
		name string
		have string
		// want is the [hooks] tuios's install must leave, event to value.
		want map[string]any
	}{
		{
			name: "no hooks table",
			have: "[server]\nport = 1\n",
			want: map[string]any{"session_start": cmd, "turn_start": cmd, "turn_end": cmd, "session_end": cmd},
		},
		{
			name: "an empty hooks table between two others",
			have: "[terminal]\n\n[hooks]\n\n[ambient]\nenabled = true\n",
			want: map[string]any{"session_start": cmd, "turn_start": cmd, "turn_end": cmd, "session_end": cmd},
		},
		{
			// The shape herdr-jcode's setup leaves, from a real config.
			name: "one command per event in literal strings",
			have: "[hooks]\npre_tool_timeout_ms = 5000\n" +
				"session_start = '\"/p/herdr-jcode\" report'\n" +
				"turn_start = '\"/p/herdr-jcode\" report'\n" +
				"turn_end = '\"/p/herdr-jcode\" report'\n" +
				"session_end = '\"/p/herdr-jcode\" report'\n\n[ambient]\n",
			want: map[string]any{
				"pre_tool_timeout_ms": int64(5000),
				"session_start":       []any{`"/p/herdr-jcode" report`, cmd},
				"turn_start":          []any{`"/p/herdr-jcode" report`, cmd},
				"turn_end":            []any{`"/p/herdr-jcode" report`, cmd},
				"session_end":         []any{`"/p/herdr-jcode" report`, cmd},
			},
		},
		{
			name: "lists, one of them over several lines with comments",
			have: "[hooks]\nturn_end = [\n  \"~/bin/a\", # first\n  '~/bin/b',\n]\nturn_start = []\npost_tool = \"~/bin/log\"\n",
			want: map[string]any{
				"turn_end":      []any{"~/bin/a", "~/bin/b", cmd},
				"turn_start":    []any{cmd},
				"post_tool":     "~/bin/log",
				"session_start": cmd,
				"session_end":   cmd,
			},
		},
		{
			name: "no file",
			have: "",
			want: map[string]any{"session_start": cmd, "turn_start": cmd, "turn_end": cmd, "session_end": cmd},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var have []byte
			if tc.have != "" {
				have = []byte(tc.have)
			}
			f := tomlHookKeys{table: "hooks"}
			out, changed, _, err := f.apply(tg, have, "tuios", true)
			if err != nil || !changed {
				t.Fatalf("install: changed %v, %v", changed, err)
			}
			var root struct{ Hooks map[string]any }
			if err := toml.Unmarshal(out, &root); err != nil {
				t.Fatalf("install left invalid TOML: %v\n%s", err, out)
			}
			if got, want := toml2str(t, root.Hooks), toml2str(t, tc.want); got != want {
				t.Fatalf("hooks after install:\n%s\nwant\n%s\nfile:\n%s", got, want, out)
			}
			if installed, current, version, err := f.state(tg, out, "tuios"); err != nil || !installed || !current || version != tg.Version {
				t.Fatalf("state after install: %v %v %v %v", installed, current, version, err)
			}
			again, changed, _, err := f.apply(tg, out, "tuios", true)
			if err != nil || changed || string(again) != string(out) {
				t.Fatalf("a second install changed the file: %v\n%s", err, again)
			}
			back, _, remove, err := f.apply(tg, out, "", false)
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			switch {
			case tc.have == "":
				if !remove {
					t.Fatalf("uninstall kept a file install made from nothing:\n%s", back)
				}
			case string(back) != tc.have:
				t.Fatalf("uninstall did not give the file back byte for byte:\n%q\nwant\n%q", back, tc.have)
			}
		})
	}
}

// TestJCodeHooksUpgradeAndStrays replaces an older command in place and takes
// tuios's command off an event this build no longer registers. tuios writes
// one command on an event it adds, so that is what a stray one looks like.
func TestJCodeHooksUpgradeAndStrays(t *testing.T) {
	tg := mustTarget(t, JCode)
	old := HookCommand("tuios", JCode, 0)
	have := "[hooks]\nturn_end = ['~/bin/a', \"" + old + "\"]\nturn_start = \"" + old + "\"\npost_tool = \"" + old + "\"\n"
	f := tomlHookKeys{table: "hooks"}
	if _, current, _, _ := f.state(tg, []byte(have), "tuios"); current {
		t.Fatal("an older install read as current")
	}
	out, _, _, err := f.apply(tg, []byte(have), "tuios", true)
	if err != nil {
		t.Fatal(err)
	}
	cmd := HookCommand("tuios", JCode, tg.Version)
	if strings.Contains(string(out), old) || strings.Contains(string(out), "post_tool") {
		t.Fatalf("the older command or the stray event survived:\n%s", out)
	}
	for _, want := range []string{"turn_end = ['~/bin/a', \"" + cmd + "\"]", "turn_start = \"" + cmd + "\""} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

// TestJCodeHooksRefusals: what tuios cannot edit safely it leaves alone, and
// says why.
func TestJCodeHooksRefusals(t *testing.T) {
	tg := mustTarget(t, JCode)
	f := tomlHookKeys{table: "hooks"}
	for name, have := range map[string]string{
		"not TOML":        "[hooks\nturn_end = 1",
		"dotted keys":     "hooks.turn_end = \"~/bin/a\"\n",
		"an inline table": "hooks = { turn_end = \"~/bin/a\" }\n",
		"a number":        "[hooks]\nturn_end = 5\n",
		"a quoted key":    "[hooks]\n\"turn_end\" = \"~/bin/a\"\n",
	} {
		if _, _, _, err := f.apply(tg, []byte(have), "tuios", true); err == nil {
			t.Errorf("%s: install went ahead", name)
		}
	}
}

func toml2str(t *testing.T, v any) string {
	t.Helper()
	b, err := toml.Marshal(map[string]any{"v": v})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
