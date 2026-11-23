package app

import (
	"encoding/json"
	"testing"
)

// A policy written before feature 052 serializes exactly as it did, so no
// stored app's revision moves.
func TestBackupJSONWithoutPlacesIsUnchanged(t *testing.T) {
	b := Backup{Engine: BackupPostgres, Schedule: "0 3 * * *", Keep: 7}
	doc, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(doc), `{"engine":"postgres","schedule":"0 3 * * *","keep":7}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}

	with, err := json.Marshal(Backup{Engine: BackupPostgres, Destination: "main", CopyTo: []string{"offsite"}, CopyKeep: 14})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(with), `{"engine":"postgres","destination":"main","copyTo":["offsite"],"copyKeep":14}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}

func TestCopyKeepOrDefault(t *testing.T) {
	tests := []struct {
		name string
		b    Backup
		want int
	}{
		{"nothing set follows the default keep", Backup{}, DefaultBackupKeep},
		{"follows the policy's keep", Backup{Keep: 3}, 3},
		{"its own number wins", Backup{Keep: 3, CopyKeep: 20}, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.b.CopyKeepOrDefault(); got != tt.want {
				t.Errorf("CopyKeepOrDefault() = %d, want %d", got, tt.want)
			}
		})
	}
}
