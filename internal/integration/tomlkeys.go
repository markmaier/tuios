package integration

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// tomlHookKeys is a TOML table whose keys are hook events, each holding one
// command or a list of them: jcode's [hooks] in config.toml. tuios adds its
// command to the value of every event it registers and takes it out again,
// and everything else in the file, the rest of each value included, is kept
// byte for byte.
//
// A key that does not exist is written as one command, which every jcode
// release reads. A key the user set to one command becomes a list of theirs
// and tuios's, which needs jcode 0.81.5 or newer, and goes back to the one
// command on uninstall. A list keeps its layout: tuios's command goes after
// its last element and comes out with the separator in front of it.
//
// The file is parsed before it is edited and again after, so a file that is
// not TOML is refused and an edit that would break it is never written.
// A table set another way than a [table] header (dotted keys, an inline
// table) is refused, since there is no line of the user's to add to.
type tomlHookKeys struct {
	table string
}

// tomlSpan is a byte range [start, end) of a TOML document.
type tomlSpan struct{ start, end int }

// tomlKeyValue is one key = value line, as the scanner found it.
type tomlKeyValue struct {
	table string
	key   string
	// line is the whole statement, from the start of its line through its
	// newline. value is the value alone, and elems its elements when it is
	// an array.
	line  tomlSpan
	value tomlSpan
	elems []tomlSpan
	array bool
}

// tomlDoc is what the scanner found in a document.
type tomlDoc struct {
	text   string
	kvs    []tomlKeyValue
	header map[string]tomlSpan // table name -> its [header] line
	// lastInTable is, per table, the end of its last key = value line, or of
	// its header line when it has none.
	lastInTable map[string]int
}

func (f tomlHookKeys) apply(t *Target, have []byte, tuios string, install bool) ([]byte, bool, bool, error) {
	if !install && have == nil {
		return nil, false, false, nil
	}
	doc, hooks, err := f.read(have)
	if err != nil {
		return nil, false, false, err
	}
	cmd := ""
	if install {
		cmd = HookCommand(tuios, t.ID, t.Version)
	}
	type edit struct {
		span tomlSpan
		text string
	}
	var edits []edit
	wanted := map[string]bool{}
	for _, ev := range t.Events {
		wanted[ev.Name] = true
	}
	var missing []string
	for _, key := range f.keysOf(doc, hooks, t.Events) {
		kv, ok := doc.find(f.table, key)
		if !ok {
			if install && wanted[key] {
				missing = append(missing, key)
			}
			continue
		}
		add := install && wanted[key]
		switch v := hooks[key].(type) {
		case string:
			switch {
			case isManagedCommand(v) && add:
				if v != cmd {
					edits = append(edits, edit{kv.value, tomlString(cmd)})
				}
			case isManagedCommand(v):
				edits = append(edits, edit{kv.line, ""})
			case add:
				edits = append(edits, edit{kv.value, "[" + doc.slice(kv.value) + ", " + tomlString(cmd) + "]"})
			}
		case []any:
			if len(v) != len(kv.elems) {
				return nil, false, false, fmt.Errorf("its [%s] %s could not be read element by element, so it is left alone", f.table, key)
			}
			user, ours := splitCommands(v)
			if add && len(ours) == 1 && v[len(v)-1] == cmd {
				continue
			}
			if len(ours) == 0 && !add {
				continue
			}
			val, err := withoutManaged(doc.slice(kv.value))
			if err != nil {
				return nil, false, false, fmt.Errorf("its [%s] %s could not be read element by element (%v), so it is left alone", f.table, key, err)
			}
			switch {
			case add:
				val = withAppended(val, tomlString(cmd))
			case len(user) == 0:
				// tuios never makes a list from nothing, so the user's list
				// was empty before and is again.
			case len(user) == 1:
				// The list tuios made of the user's one command goes back
				// to that command.
				_, elems, _, _ := scanValue(val, 0)
				val = val[elems[0].start:elems[0].end]
			}
			edits = append(edits, edit{kv.value, val})
		default:
			if isManagedAny(v) || add {
				return nil, false, false, fmt.Errorf("its [%s] %s is neither a command nor a list of commands, so it is left alone", f.table, key)
			}
		}
	}
	text := doc.text
	if len(missing) > 0 {
		var b strings.Builder
		for _, key := range missing {
			fmt.Fprintf(&b, "%s = %s\n", key, tomlString(cmd))
		}
		if at, ok := doc.lastInTable[f.table]; ok {
			edits = append(edits, edit{tomlSpan{at, at}, b.String()})
		} else {
			prefix := text
			if prefix != "" {
				if !strings.HasSuffix(prefix, "\n") {
					prefix += "\n"
				}
				prefix += "\n"
			}
			edits = append(edits, edit{tomlSpan{0, len(text)}, prefix + f.header() + "\n" + b.String()})
		}
	}
	slices.SortStableFunc(edits, func(a, b edit) int { return b.span.start - a.span.start })
	for _, e := range edits {
		text = text[:e.span.start] + e.text + text[e.span.end:]
	}
	out := []byte(text)
	// The edit is checked, not trusted: what comes back must still be TOML
	// and must hold exactly what was meant.
	if _, got, err := f.read(out); err != nil {
		return nil, false, false, fmt.Errorf("the edit would not leave valid TOML (%v), so nothing was written", err)
	} else if err := f.verify(t, hooks, got, cmd, install); err != nil {
		return nil, false, false, err
	}
	if !install {
		text = f.withoutOwnTable(text)
		out = []byte(text)
	}
	// A file left with nothing in it held only what install wrote.
	remove := !install && have != nil && strings.TrimSpace(text) == ""
	return out, text != string(have), remove, nil
}

// tableMarker ends the header of a table tuios added, so uninstall knows the
// table is its own to take out once it is empty.
const tableMarker = " # added by tuios integration install"

func (f tomlHookKeys) header() string { return "[" + f.table + "]" + tableMarker }

// withoutOwnTable takes out the table tuios added, with the blank line in
// front of it, when nothing is left in it.
func (f tomlHookKeys) withoutOwnTable(text string) string {
	doc, err := scanTOML(text)
	if err != nil {
		return text
	}
	hdr, ok := doc.header[f.table]
	if !ok || strings.TrimSpace(doc.slice(hdr)) != f.header() {
		return text
	}
	for _, kv := range doc.kvs {
		if kv.table == f.table {
			return text
		}
	}
	start := hdr.start
	if start >= 2 && text[start-1] == '\n' && text[start-2] == '\n' {
		start--
	}
	return text[:start] + text[hdr.end:]
}

// verify checks an edited [hooks] against the one it was made from: every
// user command is still there in its order, and tuios's command is on exactly
// the events it registers, once, when installing, and nowhere otherwise.
func (f tomlHookKeys) verify(t *Target, before, after map[string]any, cmd string, install bool) error {
	wanted := map[string]bool{}
	for _, ev := range t.Events {
		wanted[ev.Name] = true
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	for key := range keys {
		userBefore, _ := splitCommands(before[key])
		userAfter, ours := splitCommands(after[key])
		if !slices.Equal(userBefore, userAfter) {
			return fmt.Errorf("the edit would have changed your own [%s] %s, so nothing was written", f.table, key)
		}
		want := 0
		if install && wanted[key] {
			want = 1
		}
		if len(ours) != want || (want == 1 && ours[0] != cmd) {
			return fmt.Errorf("the edit would not leave tuios's command on [%s] %s as intended, so nothing was written", f.table, key)
		}
	}
	return nil
}

// splitCommands parts a hook value into the user's commands and tuios's.
func splitCommands(v any) (user, ours []string) {
	add := func(s string) {
		if isManagedCommand(s) {
			ours = append(ours, s)
		} else {
			user = append(user, s)
		}
	}
	switch v := v.(type) {
	case string:
		add(v)
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				add(s)
			} else {
				user = append(user, fmt.Sprint(e))
			}
		}
	case nil:
	default:
		user = append(user, fmt.Sprint(v))
	}
	return user, ours
}

// isManagedAny reports whether a hook value of an unexpected shape holds a
// command of tuios's anywhere in it.
func isManagedAny(v any) bool {
	return strings.Contains(fmt.Sprint(v), " agent-hook ") && strings.Contains(fmt.Sprint(v), managedMarker)
}

// keysOf lists the keys to look at: every event tuios registers, and every
// key of the table that holds one of tuios's commands, so a command left on
// an event this build no longer registers is taken out too.
func (f tomlHookKeys) keysOf(doc *tomlDoc, hooks map[string]any, events []HookEvent) []string {
	var keys []string
	seen := map[string]bool{}
	for _, ev := range events {
		keys = append(keys, ev.Name)
		seen[ev.Name] = true
	}
	for _, kv := range doc.kvs {
		if kv.table != f.table || seen[kv.key] {
			continue
		}
		if _, ours := splitCommands(hooks[kv.key]); len(ours) > 0 {
			keys = append(keys, kv.key)
			seen[kv.key] = true
		}
	}
	return keys
}

// read parses data and scans it, returning the hooks table as a map (empty
// when there is none). It refuses what it cannot edit.
func (f tomlHookKeys) read(data []byte) (*tomlDoc, map[string]any, error) {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("it is not valid TOML: %w", err)
	}
	doc, err := scanTOML(string(data))
	if err != nil {
		return nil, nil, err
	}
	raw, ok := root[f.table]
	if !ok {
		return doc, map[string]any{}, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("its %s is not a table, so tuios cannot add to it", f.table)
	}
	if _, ok := doc.header[f.table]; !ok {
		return nil, nil, fmt.Errorf("it sets %s with dotted keys or an inline table, which tuios does not edit. Move them under a [%s] header and install again", f.table, f.table)
	}
	for key := range hooks {
		if _, ok := doc.find(f.table, key); !ok {
			return nil, nil, fmt.Errorf("it sets [%s] %s in a way tuios does not edit (a dotted or quoted key). Write it as a plain key under [%s] and install again", f.table, key, f.table)
		}
	}
	return doc, hooks, nil
}

func (f tomlHookKeys) state(t *Target, have []byte, tuios string) (bool, bool, int, error) {
	if have == nil {
		return false, false, 0, nil
	}
	_, hooks, err := f.read(have)
	if err != nil {
		return false, false, 0, err
	}
	cmd := HookCommand(tuios, t.ID, t.Version)
	var entries []managedEntry
	version := 0
	for key, v := range hooks {
		_, ours := splitCommands(v)
		for _, c := range ours {
			entries = append(entries, managedEntry{Event: key, Command: c})
			if v := parseVersion(c); v > version {
				version = v
			}
		}
	}
	return len(entries) > 0, managedCurrent(entries, t.Events, cmd), version, nil
}

func (tomlHookKeys) owned() bool { return false }

func (d *tomlDoc) slice(s tomlSpan) string { return d.text[s.start:s.end] }

func (d *tomlDoc) find(table, key string) (tomlKeyValue, bool) {
	for _, kv := range d.kvs {
		if kv.table == table && kv.key == key {
			return kv, true
		}
	}
	return tomlKeyValue{}, false
}

// withoutManaged is the array val with every element that is one of tuios's
// commands taken out, each with the separator in front of it (or, for the
// first element, the one after it). The rest of the text is kept.
func withoutManaged(val string) (string, error) {
	for {
		_, elems, array, err := scanValue(val, 0)
		if err != nil {
			return "", err
		}
		if !array {
			return "", errors.New("it is not an array")
		}
		k := -1
		for i, e := range elems {
			var one struct{ V any }
			if toml.Unmarshal([]byte("V = "+val[e.start:e.end]), &one) != nil {
				return "", errors.New("an element does not parse")
			}
			if s, ok := one.V.(string); ok && isManagedCommand(s) {
				k = i
				break
			}
		}
		switch {
		case k < 0:
			return val, nil
		case len(elems) == 1:
			val = "[]"
		case k == 0:
			val = val[:elems[0].start] + val[elems[1].start:]
		default:
			val = val[:elems[k-1].end] + val[elems[k].end:]
		}
	}
}

// withAppended is the array val with elem after its last element.
func withAppended(val, elem string) string {
	_, elems, _, _ := scanValue(val, 0)
	if len(elems) == 0 {
		return "[" + elem + "]"
	}
	end := elems[len(elems)-1].end
	return val[:end] + ", " + elem + val[end:]
}

// scanTOML finds every key = value statement of a valid TOML document with
// its table, and every table header. It reads only what the edits need: a
// quoted key is kept with its quotes, so it matches no plain name, and an
// [[array of tables]] header names no table a key is looked up in.
func scanTOML(text string) (*tomlDoc, error) {
	d := &tomlDoc{text: text, header: map[string]tomlSpan{}, lastInTable: map[string]int{}}
	table := ""
	i := 0
	for i < len(text) {
		lineStart := i
		i = skipBlank(text, i)
		if i >= len(text) {
			break
		}
		switch text[i] {
		case '\n':
			i++
			continue
		case '\r':
			i++
			continue
		case '#':
			i = lineEnd(text, i)
			continue
		case '[':
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			line := text[i : i+end]
			if strings.HasPrefix(line, "[[") {
				table = "\x00array"
			} else {
				c := strings.IndexByte(line, ']')
				if c < 0 {
					return nil, errors.New("a table header has no closing bracket")
				}
				table = strings.TrimSpace(line[1:c])
				d.header[table] = tomlSpan{lineStart, lineEnd(text, i)}
				d.lastInTable[table] = lineEnd(text, i)
			}
			i = lineEnd(text, i)
			continue
		}
		eq := findEquals(text, i)
		if eq < 0 {
			return nil, fmt.Errorf("a line has no = at byte %d", i)
		}
		key := strings.TrimSpace(text[i:eq])
		v := skipBlank(text, eq+1)
		end, elems, array, err := scanValue(text, v)
		if err != nil {
			return nil, err
		}
		stmtEnd := lineEnd(text, end)
		d.kvs = append(d.kvs, tomlKeyValue{
			table: table, key: key,
			line:  tomlSpan{lineStart, stmtEnd},
			value: tomlSpan{v, end}, elems: elems, array: array,
		})
		d.lastInTable[table] = stmtEnd
		i = stmtEnd
	}
	return d, nil
}

// skipBlank skips spaces and tabs.
func skipBlank(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}

// lineEnd is the index just past the newline that ends the line holding i,
// or the end of the text.
func lineEnd(text string, i int) int {
	if n := strings.IndexByte(text[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(text)
}

// findEquals is the = that ends a key starting at i, outside quotes.
func findEquals(text string, i int) int {
	for i < len(text) && text[i] != '\n' {
		switch text[i] {
		case '=':
			return i
		case '"', '\'':
			end, _, _, err := scanValue(text, i)
			if err != nil {
				return -1
			}
			i = end
			continue
		}
		i++
	}
	return -1
}

// scanValue is the end of the value that starts at i, and its elements when
// it is an array.
func scanValue(text string, i int) (end int, elems []tomlSpan, array bool, err error) {
	if i >= len(text) {
		return 0, nil, false, errors.New("a key has no value")
	}
	switch {
	case strings.HasPrefix(text[i:], `"""`), strings.HasPrefix(text[i:], `'''`):
		q := text[i : i+3]
		j := i + 3
		for {
			n := strings.Index(text[j:], q)
			if n < 0 {
				return 0, nil, false, errors.New("a multi-line string is not closed")
			}
			j += n
			if q == `"""` && backslashes(text, j)%2 == 1 {
				j++
				continue
			}
			j += 3
			// Up to two more quotes belong to the string.
			for k := 0; k < 2 && j < len(text) && text[j] == q[0]; k++ {
				j++
			}
			return j, nil, false, nil
		}
	case text[i] == '"':
		for j := i + 1; j < len(text); j++ {
			switch text[j] {
			case '\\':
				j++
			case '"':
				return j + 1, nil, false, nil
			case '\n':
				return 0, nil, false, errors.New("a string is not closed")
			}
		}
		return 0, nil, false, errors.New("a string is not closed")
	case text[i] == '\'':
		n := strings.IndexAny(text[i+1:], "'\n")
		if n < 0 || text[i+1+n] != '\'' {
			return 0, nil, false, errors.New("a string is not closed")
		}
		return i + n + 2, nil, false, nil
	case text[i] == '[' || text[i] == '{':
		closer := byte(']')
		if text[i] == '{' {
			closer = '}'
		}
		j := i + 1
		for {
			j = skipSpaceAndComments(text, j)
			if j >= len(text) {
				return 0, nil, false, errors.New("an array or inline table is not closed")
			}
			if text[j] == closer {
				return j + 1, elems, closer == ']', nil
			}
			if text[j] == ',' {
				j++
				continue
			}
			if closer == '}' {
				// An inline table's key = value: step over the key.
				eq := findEquals(text, j)
				if eq < 0 {
					return 0, nil, false, errors.New("an inline table has a key with no value")
				}
				j = skipBlank(text, eq+1)
			}
			e, _, _, err := scanValue(text, j)
			if err != nil {
				return 0, nil, false, err
			}
			elems = append(elems, tomlSpan{j, e})
			j = e
		}
	default:
		j := i
		for j < len(text) && !strings.ContainsRune(" \t\r\n,]}#", rune(text[j])) {
			j++
		}
		return j, nil, false, nil
	}
}

// skipSpaceAndComments skips whitespace, line breaks and comments.
func skipSpaceAndComments(text string, i int) int {
	for i < len(text) {
		switch text[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '#':
			i = lineEnd(text, i)
		default:
			return i
		}
	}
	return i
}

// backslashes counts the backslashes right before i.
func backslashes(text string, i int) int {
	n := 0
	for i-1-n >= 0 && text[i-1-n] == '\\' {
		n++
	}
	return n
}
