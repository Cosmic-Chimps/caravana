package app

import "testing"

func TestOpenSearchRepository(t *testing.T) {
	svc := Service{
		Volumes: []Volume{{Name: "data", Path: "/data"}, {Name: "snaps", Path: "/data/snaps"}},
	}
	for _, tc := range []struct {
		name, repo, volume, inVolume string
		ok                           bool
	}{
		{"the mount itself", "/data/snaps", "snaps", "", true},
		{"a folder in a volume", "/data/other/x", "data", "other/x", true},
		{"the longest volume wins", "/data/snaps/x", "snaps", "x", true},
		{"outside", "/elsewhere", "", "", false},
		{"beside a volume's name", "/database", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc.Backup = &Backup{Engine: BackupOpenSearch, Repository: tc.repo}
			v, in, ok := svc.OpenSearchRepository()
			if ok != tc.ok || v.Name != tc.volume || in != tc.inVolume {
				t.Errorf("OpenSearchRepository = %q, %q, %v; want %q, %q, %v", v.Name, in, ok, tc.volume, tc.inVolume, tc.ok)
			}
		})
	}
	svc.Backup = &Backup{Engine: BackupSQLite, Repository: "/data/snaps"}
	if _, _, ok := svc.OpenSearchRepository(); ok {
		t.Error("a sqlite backup has no opensearch repository")
	}
}
