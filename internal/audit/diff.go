package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Op says what happened to a path.
type Op string

// The ops of a change. The console words an added secret as "set".
const (
	OpAdded   Op = "added"
	OpRemoved Op = "removed"
	OpChanged Op = "changed"
)

// Change is one field-level difference. From and To are the old and new values as text, cut at
// MaxValueBytes; a string that is empty reads `""`, so an absent From or To always means "not
// shown". A change of secret material (see Diff) has neither.
type Change struct {
	Path string `json:"path"`
	Op   Op     `json:"op"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

const (
	// MaxValueBytes is the longest a shown value is, ellipsis included.
	MaxValueBytes = 200
	// MaxChanges is how many changes Diff returns; the rest are counted in one last change.
	MaxChanges = 100
	// maxKeyBytes cuts the key of a list item in a path.
	maxKeyBytes = 100
)

// secretWords are the key endings whose values never show (plan D6): a leaf whose key, in any
// case, is or ends with one of them is told as set, changed or removed and nothing else. It covers
// the {name, value} of an env variable and of a secret.
var secretWords = []string{
	"password", "token", "secret", "secretaccesskey", "privatekey", "apikey", "signingsecret", "hash", "value",
}

// Redacted reports whether a value under key must never show.
func Redacted(key string) bool {
	k := strings.ToLower(key)
	for _, w := range secretWords {
		if strings.HasSuffix(k, w) {
			return true
		}
	}
	return false
}

// listKeys are the fields that name an item of a list, in the order they are tried.
var listKeys = []string{"name", "host", "id", "label", "path"}

// Diff compares two values by their JSON and returns what changed, in a stable order: an object's
// fields by name, a list's items by the order of before and then the new ones of after. A list of
// objects is compared by key, the first of name, host, id, label and path that every item has
// (and has once); otherwise by index. Paths read services[web].domains[new.example.com].path.
//
// An object or a list that appears or disappears whole is one change with a short summary
// ({…} or […]), not every leaf; so is any value under a Redacted key, which shows no value at
// all. Empty objects and lists count as absent. At most MaxChanges are returned; a last change
// counts the rest.
func Diff(before, after any) []Change {
	bv, err := normalize(before)
	if err != nil {
		return []Change{{Path: "(unreadable)", Op: OpChanged}}
	}
	av, err := normalize(after)
	if err != nil {
		return []Change{{Path: "(unreadable)", Op: OpChanged}}
	}
	// The root is always compared field by field, so a creation lists its fields and not one {…}.
	d := &differ{}
	bObj, bIsObj := bv.(map[string]any)
	aObj, aIsObj := av.(map[string]any)
	switch {
	case bIsObj && aIsObj:
		d.walkObject("", bObj, aObj)
	case bIsObj && !present(av):
		d.walkObject("", bObj, map[string]any{})
	case aIsObj && !present(bv):
		d.walkObject("", map[string]any{}, aObj)
	default:
		d.walk("", "", bv, av)
	}
	if d.more > 0 {
		d.out = append(d.out, Change{Path: "…", Op: OpChanged, To: fmt.Sprintf("%d more changes", d.more)})
	}
	return d.out
}

// normalize turns v into the generic JSON shapes (map, slice, string, json.Number, bool, nil).
func normalize(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

type differ struct {
	out  []Change
	more int
}

func (d *differ) add(c Change) {
	if len(d.out) >= MaxChanges {
		d.more++
		return
	}
	d.out = append(d.out, c)
}

// present reports whether v is something: not null and not an empty object or list.
func present(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

func (d *differ) walk(path, key string, a, b any) {
	ap, bp := present(a), present(b)
	if !ap && !bp {
		return
	}
	if key != "" && Redacted(key) {
		switch {
		case !ap:
			d.add(Change{Path: path, Op: OpAdded})
		case !bp:
			d.add(Change{Path: path, Op: OpRemoved})
		case !reflect.DeepEqual(a, b):
			d.add(Change{Path: path, Op: OpChanged})
		}
		return
	}
	switch {
	case !ap:
		d.add(Change{Path: path, Op: OpAdded, To: render(b)})
		return
	case !bp:
		d.add(Change{Path: path, Op: OpRemoved, From: render(a)})
		return
	}
	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			d.walkObject(path, av, bv)
			return
		}
	case []any:
		if bv, ok := b.([]any); ok {
			d.walkList(path, av, bv)
			return
		}
	default:
		if !isContainer(b) {
			if !reflect.DeepEqual(a, b) {
				d.add(Change{Path: path, Op: OpChanged, From: render(a), To: render(b)})
			}
			return
		}
	}
	// Different kinds: a scalar became an object, say.
	d.add(Change{Path: path, Op: OpChanged, From: render(a), To: render(b)})
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

func (d *differ) walkObject(path string, a, b map[string]any) {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		d.walk(joinField(path, k), k, a[k], b[k])
	}
}

func (d *differ) walkList(path string, a, b []any) {
	if field := listKey(a, b); field != "" {
		keyOf := func(v any) string { return cutBytes(v.(map[string]any)[field].(string), maxKeyBytes) }
		index := func(l []any) map[string]any {
			m := make(map[string]any, len(l))
			for _, v := range l {
				m[keyOf(v)] = v
			}
			return m
		}
		ma, mb := index(a), index(b)
		var order []string
		for _, v := range a {
			order = append(order, keyOf(v))
		}
		for _, v := range b {
			if k := keyOf(v); ma[k] == nil {
				order = append(order, k)
			}
		}
		for _, k := range order {
			d.walk(path+"["+k+"]", "", ma[k], mb[k])
		}
		return
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		var ea, eb any
		if i < len(a) {
			ea = a[i]
		}
		if i < len(b) {
			eb = b[i]
		}
		d.walk(fmt.Sprintf("%s[%d]", path, i), "", ea, eb)
	}
}

// listKey is the field that names the items of both lists, or "" when there is none: every item
// is an object with that field as a non-empty string, once in its list.
func listKey(lists ...[]any) string {
candidate:
	for _, field := range listKeys {
		for _, l := range lists {
			seen := map[string]bool{}
			for _, v := range l {
				m, ok := v.(map[string]any)
				if !ok {
					return ""
				}
				s, ok := m[field].(string)
				if !ok || s == "" {
					continue candidate
				}
				k := cutBytes(s, maxKeyBytes)
				if seen[k] {
					continue candidate
				}
				seen[k] = true
			}
		}
		return field
	}
	return ""
}

func joinField(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// render is a value as text: a scalar as it is (an empty string as ""), an object as {…}, a list
// as […]. Text is cut at MaxValueBytes.
func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		if x == "" {
			return `""`
		}
		return cutBytes(x, MaxValueBytes)
	case json.Number:
		return cutBytes(x.String(), MaxValueBytes)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case map[string]any:
		return "{…}"
	case []any:
		return "[…]"
	}
	return "?"
}

// cutBytes shortens s to at most n bytes, on a character boundary, ending in an ellipsis when it
// cut anything.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
