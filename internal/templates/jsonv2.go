package templates

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A template's JSON (the manifest, the bundle and the API's answer) has two
// features the plain struct tags can't say: an item of a service's lists may
// carry "when" (format 2), which lives in Template.ItemWhens and not in the
// compose.Overlay types, and a template is decoded strictly, so a field this
// Caravana doesn't know is an error the catalog parser can tell apart.

// whenLists are the overlay's lists whose items may have a "when".
var whenLists = []string{"domains", "secrets", "identities", "publish"}

// UnmarshalJSON reads a manifest or a bundle's template, refusing unknown fields.
func (t *Template) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var whens map[string]map[string][]Condition
	if sv, ok := raw["services"]; ok {
		var services map[string]json.RawMessage
		if err := json.Unmarshal(sv, &services); err != nil {
			return fmt.Errorf("services: %w", err)
		}
		for name, ov := range services {
			cleaned, w, err := splitWhens(ov)
			if err != nil {
				return fmt.Errorf("services.%s.%w", name, err)
			}
			services[name] = cleaned
			if w != nil {
				if whens == nil {
					whens = map[string]map[string][]Condition{}
				}
				whens[name] = w
			}
		}
		re, err := json.Marshal(services)
		if err != nil {
			return err
		}
		raw["services"] = re
	}
	cleaned, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	type plain Template // no methods: no recursion
	var p plain
	if err := decodeStrict(cleaned, &p); err != nil {
		return err
	}
	*t = Template(p)
	t.ItemWhens = whens
	return nil
}

// splitWhens takes the "when" out of each item of an overlay's lists. The
// returned conditions are per list, one per item (nil for none); nil when no
// item has one.
func splitWhens(overlay json.RawMessage) (json.RawMessage, map[string][]Condition, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(overlay, &m); err != nil {
		return overlay, nil, nil // the strict decode reports it
	}
	var out map[string][]Condition
	for _, field := range whenLists {
		arr, ok := m[field]
		if !ok {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(arr, &items); err != nil {
			continue
		}
		conds := make([]Condition, len(items))
		found := false
		for i, it := range items {
			var im map[string]json.RawMessage
			if err := json.Unmarshal(it, &im); err != nil {
				continue
			}
			w, ok := im["when"]
			if !ok {
				continue
			}
			var c Condition
			if err := json.Unmarshal(w, &c); err != nil {
				return nil, nil, fmt.Errorf("%s[%d].when: %w", field, i, err)
			}
			if c == nil {
				c = Condition{}
			}
			delete(im, "when")
			b, err := json.Marshal(im)
			if err != nil {
				return nil, nil, err
			}
			items[i], conds[i], found = b, c, true
		}
		if !found {
			continue
		}
		b, err := json.Marshal(items)
		if err != nil {
			return nil, nil, err
		}
		m[field] = b
		if out == nil {
			out = map[string][]Condition{}
		}
		out[field] = conds
	}
	if out == nil {
		return overlay, nil, nil
	}
	b, err := json.Marshal(m)
	return b, out, err
}

// MarshalJSON writes the template with each item's "when" back in its list.
func (t Template) MarshalJSON() ([]byte, error) {
	type plain Template
	if len(t.ItemWhens) == 0 {
		return json.Marshal(plain(t))
	}
	services, err := t.ServicesJSON()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		plain
		Services map[string]json.RawMessage `json:"services,omitempty"`
	}{plain(t), services})
}

// ServicesJSON is the template's services as JSON, with the items' "when"
// where they were written. The API answers with it.
func (t Template) ServicesJSON() (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(t.Services))
	for name, ov := range t.Services {
		b, err := json.Marshal(ov)
		if err != nil {
			return nil, err
		}
		if whens := t.ItemWhens[name]; len(whens) > 0 {
			b, err = putWhens(b, whens)
			if err != nil {
				return nil, err
			}
		}
		out[name] = b
	}
	return out, nil
}

// putWhens writes each condition into its item of the overlay's JSON.
func putWhens(overlay json.RawMessage, whens map[string][]Condition) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(overlay, &m); err != nil {
		return nil, err
	}
	for field, conds := range whens {
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(m[field], &items); err != nil {
			return nil, err
		}
		for i, c := range conds {
			if c == nil || i >= len(items) {
				continue
			}
			b, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			items[i]["when"] = b
		}
		b, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		m[field] = b
	}
	return json.Marshal(m)
}

// isUnknownField says a decode error is a field the struct doesn't have.
func isUnknownField(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown field")
}
