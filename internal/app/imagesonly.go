package app

import (
	"encoding/json"
	"fmt"
	"slices"
)

// ImagesOnlyChange says whether next is stored with nothing changed but the images of its
// services: the change a deploy-level person may make (feature 053). The same services in the same
// order, and every other field equal. When it isn't, field names a thing that differs (the first, alphabetically), in
// the API's own words ("services[0].env", "services", "name"), so the refusal can say what needs
// more than deploy.
//
// The comparison is on the JSON the API and the store use, so a field left out and one sent empty
// are the same, as they are everywhere else. An image that is the same is "no change" too: a
// deploy of the stored app as it is passes.
func ImagesOnlyChange(stored, next App) (field string, ok bool) {
	a, err1 := fields(stored)
	b, err2 := fields(next)
	if err1 != nil || err2 != nil {
		return "", false // an app that can't be encoded is not stored with one change: refuse
	}
	for _, key := range unionKeys(a, b) {
		if key == "services" {
			continue
		}
		if string(a[key]) != string(b[key]) {
			return key, false
		}
	}
	if len(stored.Services) != len(next.Services) {
		return "services", false
	}
	for i := range stored.Services {
		sa, err1 := fields(stored.Services[i])
		sb, err2 := fields(next.Services[i])
		if err1 != nil || err2 != nil {
			return fmt.Sprintf("services[%d]", i), false
		}
		for _, key := range unionKeys(sa, sb) {
			if key == "image" {
				continue
			}
			if string(sa[key]) != string(sb[key]) {
				return fmt.Sprintf("services[%d].%s", i, key), false
			}
		}
	}
	return "", true
}

// fields is v's JSON object as raw values by key.
func fields(v any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// unionKeys is the keys of both maps, sorted, so the first difference is the same every time.
func unionKeys(a, b map[string]json.RawMessage) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range []map[string]json.RawMessage{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	slices.Sort(keys)
	return keys
}
