package nomadjob

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The dump engines' scripts (features 010 and 046) under a real sh. The tools
// they call are fakes on a PATH that holds only them and the few real
// utilities a script needs, so an image that "has no mysqldump" is a PATH
// without one, and the host's own tools never answer.

// dumpSentinel is in every test password. A fake tool fails when it finds it in
// its arguments, which is where `ps` would show it.
const dumpSentinel = "SENTINEL"

// dumpPassword has a double quote and a backslash, the two characters the
// scripts escape, and the sentinel.
const dumpPassword = `pa"ss\w-` + dumpSentinel

const dumpFakeRclone = `#!/bin/sh
echo "rclone $*" >> "$FAKE_LOG"
cmd=$1
shift
local_path() { echo "$FAKE_BUCKET/${1#dest:}"; }
case "$cmd" in
  copyto)
    case "$1" in
      dest:*) mkdir -p "$(dirname "$2")" && cp "$(local_path "$1")" "$2" ;;
      *) p=$(local_path "$2"); mkdir -p "$(dirname "$p")" && cp "$1" "$p" ;;
    esac ;;
  lsf)
    shift # --files-only
    p=$(local_path "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    for f in "$p"/*; do [ -f "$f" ] && basename "$f"; done
    exit 0 ;;
  deletefile)
    rm -f "$(local_path "$1")" ;;
  *) echo "fake rclone: unknown command $cmd" >&2; exit 2 ;;
esac
`

// dumpFakeStat answers "stat -c %s <file>" (GNU's, which BusyBox has and macOS'
// stat doesn't).
const dumpFakeStat = `#!/bin/sh
wc -c < "$3" | tr -d ' '
`

// dumpFakeSQLClient stands for mariadb and mysql, which one is its name. A login
// that fails answers like the real one. The option file must come first, as the
// real tools need it, and is copied away so the test can read it.
const dumpFakeSQLClient = `#!/bin/sh
name=$(basename "$0")
echo "$name $*" >> "$FAKE_LOG"
case "$1" in
  --defaults-extra-file=*) ;;
  *) echo "$name: --defaults-extra-file must be the first option" >&2; exit 2 ;;
esac
cp "${1#--defaults-extra-file=}" "$FAKE_SEEN/client.cnf"
case "$*" in *` + dumpSentinel + `*) echo "$name: a password is in the arguments" >&2; exit 2 ;; esac
case "$*" in
  *"SHOW DATABASES"*)
    [ -z "$FAKE_LOGIN_FAIL" ] || { echo "ERROR 1045 (28000): Access denied" >&2; exit 1; }
    printf '%s\n' "$FAKE_DATABASES" ;;
  *) cat > "$FAKE_SEEN/restored" ;;
esac
`

// dumpFakeSQLDump stands for mariadb-dump and mysqldump.
const dumpFakeSQLDump = `#!/bin/sh
name=$(basename "$0")
echo "$name $*" >> "$FAKE_LOG"
case "$1" in
  --defaults-extra-file=*) ;;
  *) echo "$name: --defaults-extra-file must be the first option" >&2; exit 2 ;;
esac
case "$*" in *` + dumpSentinel + `*) echo "$name: a password is in the arguments" >&2; exit 2 ;; esac
echo "-- fake dump by $name"
echo "CREATE TABLE t (id int);"
`

// dumpFakeMongo stands for mongodump and mongorestore.
const dumpFakeMongo = `#!/bin/sh
name=$(basename "$0")
echo "$name $*" >> "$FAKE_LOG"
case "$*" in *` + dumpSentinel + `*) echo "$name: a password is in the arguments" >&2; exit 2 ;; esac
for a in "$@"; do
  case "$a" in
    --config=*) cp "${a#--config=}" "$FAKE_SEEN/mongo.yaml" ;;
    --archive=*) archive=${a#--archive=} ;;
  esac
done
case "$name" in
  mongodump) echo "archive bytes" > "$archive" ;;
  mongorestore) cp "$archive" "$FAKE_SEEN/restored" ;;
esac
`

var (
	dumpTools     sync.Once
	dumpToolsPath string
	dumpToolsErr  error
)

// dumpToolsDir writes the fakes once for the whole test run (macOS scans every
// new executable). TestMain removes the folder when the tests are done.
func dumpToolsDir(t *testing.T) string {
	t.Helper()
	dumpTools.Do(func() {
		dumpToolsPath, dumpToolsErr = os.MkdirTemp("", "caravana-dump-tools-")
		if dumpToolsErr != nil {
			return
		}
		for name, text := range map[string]string{
			"rclone": dumpFakeRclone, "stat": dumpFakeStat,
			"mariadb": dumpFakeSQLClient, "mysql": dumpFakeSQLClient,
			"mariadb-dump": dumpFakeSQLDump, "mysqldump": dumpFakeSQLDump,
			"mongodump": dumpFakeMongo, "mongorestore": dumpFakeMongo,
		} {
			if dumpToolsErr = os.WriteFile(filepath.Join(dumpToolsPath, name), []byte(text), 0o755); dumpToolsErr != nil {
				return
			}
		}
	})
	if dumpToolsErr != nil {
		t.Fatalf("fake tools: %v", dumpToolsErr)
	}
	return dumpToolsPath
}

// realTools are the utilities the scripts use, linked into every run's PATH.
var realTools = []string{"sh", "sed", "cat", "cp", "mkdir", "rm", "dirname", "basename", "grep", "sort", "wc", "tr",
	"head", "printenv", "gzip", "gunzip"}

// dumpRun is one run of a script: its own folders and a PATH of the chosen
// fakes.
type dumpRun struct {
	t       *testing.T
	dir     string
	bin     string // the run's PATH
	bucket  string // the fake destination
	seen    string // what the fake tools copied away: client.cnf, mongo.yaml, restored
	secrets string // NOMAD_SECRETS_DIR
	dump    string // where the script's dumpPath is, for the test
	log     string // every fake tool call, one per line
	sh      string
}

// newDumpRun makes a run whose PATH has the real utilities and the named
// fakes. It skips the test when a utility is missing.
func newDumpRun(t *testing.T, fakes ...string) *dumpRun {
	t.Helper()
	r := &dumpRun{t: t, dir: t.TempDir()}
	r.bin = filepath.Join(r.dir, "bin")
	r.bucket = filepath.Join(r.dir, "bucket")
	r.seen = filepath.Join(r.dir, "seen")
	r.secrets = filepath.Join(r.dir, "secrets")
	r.dump = filepath.Join(r.dir, "alloc", "data", "backup.dump")
	r.log = filepath.Join(r.dir, "calls.log")
	for _, d := range []string{r.bin, r.bucket, r.seen, r.secrets, filepath.Dir(r.dump)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range realTools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if tool == "sh" {
			r.sh = path
		}
		if err := os.Symlink(path, filepath.Join(r.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range fakes {
		if err := os.Symlink(filepath.Join(dumpToolsDir(t), f), filepath.Join(r.bin, f)); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// putObject writes a file into the fake destination.
func (r *dumpRun) putObject(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.bucket, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// run runs script under sh as a task would: the dump path is the run's own, and
// env is the task's environment (the job's variable, the template's host and
// port, the env of the task). Nothing of the test process's environment goes in.
func (r *dumpRun) run(script string, env ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	script = strings.ReplaceAll(script, dumpPath, r.dump)
	cmd := exec.Command(r.sh, "-c", script)
	cmd.Dir = r.dir
	cmd.Env = append([]string{"PATH=" + r.bin, "FAKE_LOG=" + r.log, "FAKE_BUCKET=" + r.bucket, "FAKE_SEEN=" + r.seen,
		"NOMAD_SECRETS_DIR=" + r.secrets}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code = exitCode(r.t, cmd.Run())
	return out.String(), errOut.String(), code
}

// calls are the lines of the fake tools' log.
func (r *dumpRun) calls() []string {
	r.t.Helper()
	b, err := os.ReadFile(r.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// called reports whether a fake tool was called with name first.
func (r *dumpRun) called(name string) bool {
	for _, c := range r.calls() {
		if strings.HasPrefix(c, name+" ") {
			return true
		}
	}
	return false
}

// read returns a file of the run, or "" when there is none.
func (r *dumpRun) read(path string) string {
	r.t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// checkSecretFile checks a login file: it holds want and only its owner can
// read it.
func (r *dumpRun) checkSecretFile(path, want string) {
	r.t.Helper()
	if got := r.read(path); got != want {
		r.t.Errorf("%s =\n%q\nwant\n%q", filepath.Base(path), got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		r.t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		r.t.Errorf("%s has mode %v, want 0600", filepath.Base(path), info.Mode().Perm())
	}
}

// loginEnv is the task's environment for a database at 10.99.0.7:3306.
func loginEnv(user string, extra ...string) []string {
	return append([]string{"DB_HOST=10.99.0.7", "DB_PORT=3306", "DB_USER=" + user, "DB_PASSWORD=" + dumpPassword}, extra...)
}

const mysqlOptionFile = "[client]\nhost=\"10.99.0.7\"\nport=\"3306\"\nuser=\"root\"\npassword=\"pa\\\"ss\\\\w-" + dumpSentinel + "\"\n"

func TestMySQLDumpScript(t *testing.T) {
	const databases = "information_schema\napp\nmy shop\nmysql\nperformance_schema\nsys\n"

	for _, tool := range []struct{ dump, client string }{{"mariadb-dump", "mariadb"}, {"mysqldump", "mysql"}} {
		t.Run("dumps with "+tool.dump, func(t *testing.T) {
			r := newDumpRun(t, tool.dump, tool.client)
			stdout, stderr, code := r.run(mysqlDumpScript, loginEnv("root", "FAKE_DATABASES="+databases)...)
			if code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			r.checkSecretFile(filepath.Join(r.secrets, "client.cnf"), mysqlOptionFile)
			calls := r.calls()
			if len(calls) != 2 {
				t.Fatalf("calls = %q, want the database list and the dump", calls)
			}
			opt := "--defaults-extra-file=" + filepath.Join(r.secrets, "client.cnf")
			if want := tool.client + " " + opt + " --batch --skip-column-names -e SHOW DATABASES"; calls[0] != want {
				t.Errorf("list call = %q, want %q", calls[0], want)
			}
			// The server's own databases are left out and a name with a space is one argument.
			want := tool.dump + " " + opt + " --single-transaction --routines --events --triggers --no-tablespaces --databases app my shop"
			if calls[1] != want {
				t.Errorf("dump call = %q, want %q", calls[1], want)
			}
			if got := r.read(r.dump); !strings.Contains(got, "CREATE TABLE t") {
				t.Errorf("the dump file = %q, want the tool's output", got)
			}
			if strings.Contains(stdout+stderr+strings.Join(calls, "\n"), dumpSentinel) {
				t.Error("the password reached an argument or an output")
			}
		})
	}

	t.Run("prefers the MariaDB tools", func(t *testing.T) {
		r := newDumpRun(t, "mariadb-dump", "mariadb", "mysqldump", "mysql")
		if _, stderr, code := r.run(mysqlDumpScript, loginEnv("root", "FAKE_DATABASES=app")...); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if r.called("mysqldump") || r.called("mysql") || !r.called("mariadb-dump") {
			t.Errorf("calls = %q, want only the MariaDB tools", r.calls())
		}
	})

	t.Run("an image with neither tool fails with its sentence", func(t *testing.T) {
		r := newDumpRun(t)
		_, stderr, code := r.run(mysqlDumpScript, loginEnv("root", "FAKE_DATABASES=app")...)
		if code != 1 || stderr != "the image has neither mariadb-dump nor mysqldump\n" {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("a dump tool without its client is as good as none", func(t *testing.T) {
		r := newDumpRun(t, "mysqldump")
		_, stderr, code := r.run(mysqlDumpScript, loginEnv("root")...)
		if code != 1 || stderr != "the image has neither mariadb-dump nor mysqldump\n" {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})

	t.Run("no database to dump is a comment", func(t *testing.T) {
		r := newDumpRun(t, "mariadb-dump", "mariadb")
		_, stderr, code := r.run(mysqlDumpScript, loginEnv("root", "FAKE_DATABASES=information_schema\nmysql\nperformance_schema\nsys\n")...)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if got := r.read(r.dump); got != "-- caravana: no databases to dump\n" {
			t.Errorf("the dump file = %q, want one SQL comment", got)
		}
		if r.called("mariadb-dump") {
			t.Error("the dump tool ran with nothing to dump")
		}
	})

	t.Run("a failed login fails the run and writes no dump", func(t *testing.T) {
		r := newDumpRun(t, "mariadb-dump", "mariadb")
		_, stderr, code := r.run(mysqlDumpScript, loginEnv("root", "FAKE_LOGIN_FAIL=1")...)
		if code == 0 {
			t.Fatal("a failed login didn't fail the run")
		}
		if !strings.Contains(stderr, "Access denied") {
			t.Errorf("stderr = %q, want the client's complaint", stderr)
		}
		if r.called("mariadb-dump") || r.read(r.dump) != "" {
			t.Error("a dump was made after the login failed")
		}
	})

	t.Run("a value with a line break is refused before any file is written", func(t *testing.T) {
		r := newDumpRun(t, "mariadb-dump", "mariadb")
		_, stderr, code := r.run(mysqlDumpScript, "DB_HOST=10.99.0.7", "DB_PORT=3306", "DB_USER=root",
			"DB_PASSWORD=pw\npassword=other")
		if code != 1 || stderr != "a value the backup logs in with has a line break\n" {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(r.secrets, "client.cnf")); err == nil {
			t.Error("an option file was written")
		}
	})
}

func TestMySQLRestoreScript(t *testing.T) {
	t.Run("loads the dump through the client with the option file", func(t *testing.T) {
		r := newDumpRun(t, "mariadb")
		if err := os.WriteFile(r.dump, []byte("CREATE TABLE a (id int);\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := r.run(mysqlRestoreScript, loginEnv("root")...)
		if code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		r.checkSecretFile(filepath.Join(r.secrets, "client.cnf"), mysqlOptionFile)
		if got := r.read(filepath.Join(r.seen, "restored")); got != "CREATE TABLE a (id int);\n" {
			t.Errorf("the client read %q", got)
		}
		want := "mariadb --defaults-extra-file=" + filepath.Join(r.secrets, "client.cnf")
		if calls := r.calls(); len(calls) != 1 || calls[0] != want {
			t.Errorf("calls = %q, want just %q (no password, no database name)", calls, want)
		}
	})
	t.Run("MySQL's client when the image has no mariadb", func(t *testing.T) {
		r := newDumpRun(t, "mysql")
		if err := os.WriteFile(r.dump, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := r.run(mysqlRestoreScript, loginEnv("root")...); code != 0 || !r.called("mysql") {
			t.Errorf("exit %d (%s), calls %q", code, stderr, r.calls())
		}
	})
	t.Run("an image with no client fails with its sentence", func(t *testing.T) {
		r := newDumpRun(t)
		_, stderr, code := r.run(mysqlRestoreScript, loginEnv("root")...)
		if code != 1 || stderr != "the image has neither mariadb nor mysql\n" {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
}

// The restore's first task expands a compressed dump into the file the restore
// task reads.
func TestDownloadGunzipScript(t *testing.T) {
	r := newDumpRun(t, "rclone")
	zipped := newDumpRun(t, "rclone")
	if err := os.WriteFile(zipped.dump, []byte("CREATE TABLE a (id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the object the way the upload task does.
	if _, stderr, code := zipped.run(uploadScript, "NOMAD_JOB_ID=notes_db_backup/periodic-1791", "BACKUP_PATH=b/notes/db",
		"BACKUP_KEEP=3", "BACKUP_EXT=.sql.gz", "BACKUP_GZIP=1"); code != 0 {
		t.Fatalf("upload: exit %d: %s", code, stderr)
	}
	obj, err := os.ReadFile(filepath.Join(zipped.bucket, "b/notes/db/1791.sql.gz"))
	if err != nil {
		t.Fatal(err)
	}
	r.putObject("b/notes/db/1791.sql.gz", string(obj))

	_, stderr, code := r.run(downloadGunzipScript, "NOMAD_META_backup_key=b/notes/db/1791.sql.gz")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := r.read(r.dump); got != "CREATE TABLE a (id int);\n" {
		t.Errorf("the dump file = %q, want the original SQL", got)
	}
}

func TestMongoScripts(t *testing.T) {
	yaml := "password: \"pa\\\"ss\\\\w-" + dumpSentinel + "\"\n"
	login := "--host=10.99.0.7 --port=3306 --username=root --authenticationDatabase=admin"

	t.Run("dump", func(t *testing.T) {
		r := newDumpRun(t, "mongodump")
		stdout, stderr, code := r.run(mongoDumpScript, loginEnv("root")...)
		if code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		r.checkSecretFile(filepath.Join(r.secrets, "mongo.yaml"), yaml)
		want := "mongodump --config=" + filepath.Join(r.secrets, "mongo.yaml") + " " + login + " --archive=" + r.dump + " --gzip"
		if calls := r.calls(); len(calls) != 1 || calls[0] != want {
			t.Errorf("calls = %q, want %q", calls, want)
		}
		if got := r.read(r.dump); got != "archive bytes\n" {
			t.Errorf("the archive = %q", got)
		}
	})
	t.Run("restore", func(t *testing.T) {
		r := newDumpRun(t, "mongorestore")
		if err := os.WriteFile(r.dump, []byte("the archive"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := r.run(mongoRestoreScript, loginEnv("root")...)
		if code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		r.checkSecretFile(filepath.Join(r.secrets, "mongo.yaml"), yaml)
		want := "mongorestore --config=" + filepath.Join(r.secrets, "mongo.yaml") + " " + login + " --archive=" + r.dump +
			" --gzip --drop --nsExclude=admin.* --nsExclude=config.*"
		if calls := r.calls(); len(calls) != 1 || calls[0] != want {
			t.Errorf("calls = %q, want %q", calls, want)
		}
		if got := r.read(filepath.Join(r.seen, "restored")); got != "the archive" {
			t.Errorf("mongorestore read %q", got)
		}
	})
	t.Run("missing tools fail with their sentences", func(t *testing.T) {
		for _, tc := range []struct{ name, script, want string }{
			{"mongodump", mongoDumpScript, "the image has no mongodump\n"},
			{"mongorestore", mongoRestoreScript, "the image has no mongorestore\n"},
		} {
			r := newDumpRun(t)
			_, stderr, code := r.run(tc.script, loginEnv("root")...)
			if code != 1 || stderr != tc.want {
				t.Errorf("%s: exit %d, stderr %q, want %q", tc.name, code, stderr, tc.want)
			}
			if _, err := os.Stat(filepath.Join(r.secrets, "mongo.yaml")); err == nil {
				t.Errorf("%s: a config file was written before the tool was found", tc.name)
			}
		}
	})
	t.Run("a value with a line break is refused", func(t *testing.T) {
		r := newDumpRun(t, "mongodump")
		_, stderr, code := r.run(mongoDumpScript, "DB_HOST=h", "DB_PORT=1", "DB_USER=root\nx", "DB_PASSWORD=p")
		if code != 1 || stderr != "a value the backup logs in with has a line break\n" || r.called("mongodump") {
			t.Errorf("exit %d, stderr %q, calls %q", code, stderr, r.calls())
		}
	})
}
