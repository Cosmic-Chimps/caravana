package nomadjob

import "github.com/cosmic-chimps/caravana/internal/app"

// The dump engines (features 010 and 046). Each is a recipe: the commands of
// its dump and restore tasks, its object's extension and the names of the host
// and port variables its template renders. BackupJob and RestoreJob build the
// same groups from any recipe.
//
// The scripts use $VAR and $(...) only, never ${...}: Nomad interpolates
// ${...} in task config before the shell sees it. They don't use pipefail,
// which not every image's sh has, so a step that must fail the run is a command
// of its own.

// dumpRecipe is what differs between the dump engines.
type dumpRecipe struct {
	ext        string // the object's extension, with its dot
	gzip       bool   // the upload task compresses the dump and the restore's download expands it
	host, port string // the variables the database template renders
	dump       map[string]any
	restore    map[string]any
}

// gzipFlag is the upload task's BACKUP_GZIP.
func (r dumpRecipe) gzipFlag() string {
	if r.gzip {
		return "1"
	}
	return "0"
}

// dumpConfig is the dump task's docker config for image.
func (r dumpRecipe) dumpConfig(image string) map[string]any { return withImage(r.dump, image) }

// restoreConfig is the restore task's docker config for image.
func (r dumpRecipe) restoreConfig(image string) map[string]any { return withImage(r.restore, image) }

// downloadConfig is the restore's first task: rclone fetches the object into
// dumpPath. An engine whose dump the upload task compresses expands it on the
// way. The plain download has Nomad's interpolation on purpose: it puts the
// job's meta, the object, into the rclone arguments; the script reads the same
// meta from the task's environment.
func (r dumpRecipe) downloadConfig(rcloneImage string) map[string]any {
	if r.gzip {
		return map[string]any{"image": rcloneImage, "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{downloadGunzipScript}}
	}
	return map[string]any{"image": rcloneImage, "args": []string{"copyto", "dest:${NOMAD_META_backup_key}", dumpPath}}
}

// withImage is a copy of config with the task's image added.
func withImage(config map[string]any, image string) map[string]any {
	out := map[string]any{"image": image}
	for k, v := range config {
		out[k] = v
	}
	return out
}

// shell is the docker config part that runs script with the image's own sh.
func shell(script string) map[string]any {
	return map[string]any{"entrypoint": []string{"/bin/sh", "-c"}, "args": []string{script}}
}

// recipeFor is the recipe of engine. An engine that isn't a dump engine can't
// reach a backup job (app.DumpEngine decides that), so anything else is the
// first engine's, Postgres.
func recipeFor(engine string) dumpRecipe {
	switch engine {
	case app.BackupMySQL:
		return dumpRecipe{ext: ".sql.gz", gzip: true, host: "DB_HOST", port: "DB_PORT",
			dump: shell(mysqlDumpScript), restore: shell(mysqlRestoreScript)}
	case app.BackupMongoDB:
		return dumpRecipe{ext: ".archive.gz", host: "DB_HOST", port: "DB_PORT",
			dump: shell(mongoDumpScript), restore: shell(mongoRestoreScript)}
	}
	return dumpRecipe{ext: ".dump", host: "PGHOST", port: "PGPORT",
		dump: map[string]any{"command": "pg_dump", "args": []string{"-Fc", "-f", dumpPath}},
		// One transaction: a restore that fails partway (a scheduled one
		// included, feature 036) leaves the database as it was, not half
		// cleaned.
		restore: shell(`pg_restore --clean --if-exists --no-owner --single-transaction -d "$PGDATABASE" ` + dumpPath)}
}

// downloadGunzipScript is the restore's download task for a compressed dump:
// the object lands in a file and is expanded to dumpPath, the file the restore
// task reads. $NOMAD_META_backup_key is the object, from the job's meta.
const downloadGunzipScript = `set -eu
rclone copyto "dest:$NOMAD_META_backup_key" ` + dumpPath + `.gz
gunzip -c ` + dumpPath + `.gz > ` + dumpPath + `
`

// clientConfigPreamble starts the scripts that log in with a file instead of
// arguments (feature 046, D5): a password in argv shows in every process
// listing of the host, a file in the task's own secrets directory doesn't.
// umask 077 makes the file readable by the task's user only, and a value with
// a line break is refused because it could add a line to the file. esc escapes
// what a double-quoted value of both file formats (a MySQL option file and
// the Mongo tools' YAML config) treats as special: backslash and double quote.
const clientConfigPreamble = `set -eu
umask 077
nl='
'
for v in "$DB_HOST" "$DB_PORT" "$DB_USER" "$DB_PASSWORD"; do
  case "$v" in
    *"$nl"*) echo "a value the backup logs in with has a line break" >&2; exit 1 ;;
  esac
done
esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
`

// mysqlClientFile writes the [client] option file the MySQL and MariaDB
// tools read with --defaults-extra-file (which must be their first option).
// MYSQL_PWD would do for the password, but MySQL 8 deprecates it. The values
// go through printf, not echo: dash's echo (the sh of Debian and Ubuntu images)
// reads \\ as an escape and would undo esc.
const mysqlClientFile = `cnf="$NOMAD_SECRETS_DIR/client.cnf"
{
  echo '[client]'
  printf 'host="%s"\n' "$(esc "$DB_HOST")"
  printf 'port="%s"\n' "$(esc "$DB_PORT")"
  printf 'user="%s"\n' "$(esc "$DB_USER")"
  printf 'password="%s"\n' "$(esc "$DB_PASSWORD")"
} > "$cnf"
`

// mysqlDumpScript dumps every database the login sees but the server's own
// (information_schema, performance_schema, mysql and sys) into dumpPath as plain
// SQL, which the upload task compresses. The image's own client does it, so the
// script picks the MariaDB tools when the image has them (a MariaDB image's
// mysqldump is only a link on older versions and absent on newer ones) and
// MySQL's otherwise; an image with neither fails with that sentence.
//
// The list of databases is read in a command substitution of its own: a failed
// login fails the run there, which a pipe into a filter would hide. The names
// become the script's arguments (set --), so one with a space survives. With
// --databases the dump creates and selects each database, so a restore needs no
// name. --single-transaction gives a consistent dump of InnoDB tables without
// locking, and --no-tablespaces keeps the dump from needing the PROCESS privilege.
const mysqlDumpScript = clientConfigPreamble + `if command -v mariadb-dump >/dev/null 2>&1 && command -v mariadb >/dev/null 2>&1; then
  dump=mariadb-dump; client=mariadb
elif command -v mysqldump >/dev/null 2>&1 && command -v mysql >/dev/null 2>&1; then
  dump=mysqldump; client=mysql
else
  echo "the image has neither mariadb-dump nor mysqldump" >&2
  exit 1
fi
` + mysqlClientFile + `dbs=$("$client" --defaults-extra-file="$cnf" --batch --skip-column-names -e 'SHOW DATABASES')
set --
while IFS= read -r d; do
  case "$d" in
    ''|information_schema|performance_schema|mysql|sys) ;;
    *) set -- "$@" "$d" ;;
  esac
done <<DATABASES
$dbs
DATABASES
if [ "$#" -eq 0 ]; then
  echo '-- caravana: no databases to dump' > ` + dumpPath + `
else
  "$dump" --defaults-extra-file="$cnf" --single-transaction --routines --events --triggers --no-tablespaces --databases "$@" > ` + dumpPath + `
fi
`

// mysqlRestoreScript loads the dump through the client. Each table in it is
// dropped and made again (the dump tools' default), and tables made since stay.
// MySQL commits on DDL, so unlike Postgres's it is not one transaction.
const mysqlRestoreScript = clientConfigPreamble + `if command -v mariadb >/dev/null 2>&1; then
  client=mariadb
elif command -v mysql >/dev/null 2>&1; then
  client=mysql
else
  echo "the image has neither mariadb nor mysql" >&2
  exit 1
fi
` + mysqlClientFile + `"$client" --defaults-extra-file="$cnf" < ` + dumpPath + `
`

// mongoClientFile writes the config file the database tools read with
// --config: only the password, the one value that mustn't show in argv. (The
// config option is in the tools from 100.3, which the mongo 4.4 images and
// every later one have.)
const mongoClientFile = `cfg="$NOMAD_SECRETS_DIR/mongo.yaml"
printf 'password: "%s"\n' "$(esc "$DB_PASSWORD")" > "$cfg"
`

// mongoLogin is the arguments both tools log in with. The root user of the
// official image lives in the admin database.
const mongoLogin = `--config="$cfg" --host="$DB_HOST" --port="$DB_PORT" --username="$DB_USER" --authenticationDatabase=admin`

// mongoDumpScript dumps every database into one compressed archive: mongodump
// compresses itself, so the upload task leaves the file as it is.
const mongoDumpScript = clientConfigPreamble + `command -v mongodump >/dev/null 2>&1 || { echo "the image has no mongodump" >&2; exit 1; }
` + mongoClientFile + `mongodump ` + mongoLogin + ` --archive=` + dumpPath + ` --gzip
`

// mongoRestoreScript replaces every collection in the archive (--drop) and
// leaves users and the server's own state alone (admin and config are not
// restored, so a restore can't lock the service's own login out).
const mongoRestoreScript = clientConfigPreamble + `command -v mongorestore >/dev/null 2>&1 || { echo "the image has no mongorestore" >&2; exit 1; }
` + mongoClientFile + `mongorestore ` + mongoLogin + ` --archive=` + dumpPath + ` --gzip --drop --nsExclude='admin.*' --nsExclude='config.*'
`
