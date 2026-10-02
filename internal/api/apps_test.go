package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// waitUntil polls cond for up to 5 seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// US5: an app reads back exactly as it was sent.
func TestGetApp(t *testing.T) {
	e := newTestEnv(t, "")
	deployOK(t, e, "whoami", whoamiBody)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")

	var got struct {
		App       map[string]any `json:"app"`
		CreatedAt time.Time      `json:"createdAt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(whoamiBody), &sent); err != nil {
		t.Fatal(err)
	}
	sent["id"] = "whoami" // taken from the path
	if !reflect.DeepEqual(got.App, sent) {
		t.Errorf("app = %v\nwant %v (durations as the same strings)", got.App, sent)
	}
	if got.CreatedAt.IsZero() {
		t.Error("createdAt missing")
	}

	// The app can be sent back unchanged as a deploy body.
	back, _ := json.Marshal(got.App)
	deployOK(t, e, "whoami", string(back))

	if status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/ghost", "", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: %d %s, want 404", status, raw)
	}
}
