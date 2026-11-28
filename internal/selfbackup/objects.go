package selfbackup

import (
	"strconv"
	"strings"
)

// Where backups live in the bucket (plan D2): <prefix>/_caravana/<ts>/caravana-<ts>.cvbackup, where <ts>
// is unix seconds. "_caravana" can't be an app ID, so the folder is never inside an app's path.
const (
	// Folder is the folder of the owner's prefix that holds Caravana's own backups.
	Folder = "_caravana"
	// Extension ends every backup's name.
	Extension = ".cvbackup"
)

// Base is the folder, with its trailing slash, that holds every backup under the owner's prefix ("" for
// none): what a listing asks for.
func Base(prefix string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return Folder + "/"
	}
	return p + "/" + Folder + "/"
}

// ObjectKey is the key of the backup made at unix second ts.
func ObjectKey(prefix string, ts int64) string {
	n := strconv.FormatInt(ts, 10)
	return Base(prefix) + n + "/caravana-" + n + Extension
}

// ParseObjectKey reads the time out of a key under Base(prefix), and false for anything that isn't a
// backup's key: a retention pass must delete only what it can name.
func ParseObjectKey(prefix, key string) (ts int64, ok bool) {
	rest, ok := strings.CutPrefix(key, Base(prefix))
	if !ok {
		return 0, false
	}
	dir, file, ok := strings.Cut(rest, "/")
	if !ok || dir == "" || dir[0] == '0' || strings.Contains(dir, "+") || strings.Contains(dir, "-") {
		return 0, false
	}
	ts, err := strconv.ParseInt(dir, 10, 64)
	if err != nil || ts <= 0 || file != "caravana-"+dir+Extension {
		return 0, false
	}
	return ts, true
}
