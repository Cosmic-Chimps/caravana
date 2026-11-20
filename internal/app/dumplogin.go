package app

import "slices"

// What a dump logs in with (features 010 and 046). The credentials are never
// a field of the model: they are the variables the database's own image reads,
// found by name in the service's environment or secrets, so one set of values
// starts the database and backs it up.

// DumpLogin is the account a dump or restore logs in with. Database is only
// the Postgres engine's: MySQL's dump takes every database the login sees and
// MongoDB's takes all of them.
type DumpLogin struct {
	User, Password, Database string
}

// FindDumpLogin finds engine's account by looking names up with lookup, which
// answers a variable's value and whether the service has it at all (a secret's
// value isn't known to the model, so validation only asks if it is there; the
// service asks with the values in hand). ok is false when the service doesn't
// have what the engine needs, and DumpLoginHint says what that is (the login
// is then empty, but for Postgres' user and database, which have defaults).
//
// Postgres: POSTGRES_PASSWORD, with POSTGRES_USER (postgres) and POSTGRES_DB
// (the user). MySQL: the root password the image sets (MARIADB_ROOT_PASSWORD,
// then MYSQL_ROOT_PASSWORD) and user root, else a MARIADB_USER or MYSQL_USER
// with its password. MongoDB: MONGO_INITDB_ROOT_USERNAME and
// MONGO_INITDB_ROOT_PASSWORD.
func FindDumpLogin(engine string, lookup func(name string) (string, bool)) (DumpLogin, bool) {
	switch engine {
	case BackupPostgres:
		// The user and database have defaults, so they are found even without a
		// password; ok says whether the password is there.
		password, ok := lookup("POSTGRES_PASSWORD")
		user := "postgres"
		if v, found := lookup("POSTGRES_USER"); found {
			user = v
		}
		db := user
		if v, found := lookup("POSTGRES_DB"); found {
			db = v
		}
		return DumpLogin{User: user, Password: password, Database: db}, ok
	case BackupMySQL:
		for _, name := range []string{"MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD"} {
			if password, ok := lookup(name); ok {
				return DumpLogin{User: "root", Password: password}, true
			}
		}
		for _, p := range [][2]string{{"MARIADB_USER", "MARIADB_PASSWORD"}, {"MYSQL_USER", "MYSQL_PASSWORD"}} {
			user, userOK := lookup(p[0])
			password, passwordOK := lookup(p[1])
			if userOK && passwordOK {
				return DumpLogin{User: user, Password: password}, true
			}
		}
	case BackupMongoDB:
		user, userOK := lookup("MONGO_INITDB_ROOT_USERNAME")
		password, passwordOK := lookup("MONGO_INITDB_ROOT_PASSWORD")
		if userOK && passwordOK {
			return DumpLogin{User: user, Password: password}, true
		}
	}
	return DumpLogin{}, false
}

// DumpLoginHint is the half sentence a refusal gives when FindDumpLogin finds
// nothing: which variables the engine's backup logs in with.
func DumpLoginHint(engine string) string {
	switch engine {
	case BackupMySQL:
		return "logs in as root with MARIADB_ROOT_PASSWORD or MYSQL_ROOT_PASSWORD, or with MARIADB_USER and MARIADB_PASSWORD (or MYSQL_USER and MYSQL_PASSWORD)"
	case BackupMongoDB:
		return "logs in with MONGO_INITDB_ROOT_USERNAME and MONGO_INITDB_ROOT_PASSWORD"
	default:
		return "logs in with POSTGRES_PASSWORD"
	}
}

// BackupPasswordNames are the variables of engine's backup that hold a password,
// in the order FindDumpLogin tries them. A plain environment variable of one of
// these names, on a service backed up with the engine, shows the password in the
// app's definition, so the deploy warns (QA #36) and a template is refused.
func BackupPasswordNames(engine string) []string {
	switch engine {
	case BackupPostgres:
		return []string{"POSTGRES_PASSWORD"}
	case BackupMySQL:
		return []string{"MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_PASSWORD", "MYSQL_PASSWORD"}
	case BackupMongoDB:
		return []string{"MONGO_INITDB_ROOT_PASSWORD"}
	}
	return nil
}

// hasDumpLogin reports whether svc names what engine's dump logs in with, in
// its environment or secrets (validation: it can't know a secret's value).
func hasDumpLogin(engine string, svc Service) bool {
	_, ok := FindDumpLogin(engine, func(name string) (string, bool) {
		if slices.ContainsFunc(svc.Env, func(e EnvVar) bool { return e.Name == name }) ||
			slices.ContainsFunc(svc.Secrets, func(s Secret) bool { return s.Name == name }) {
			return "", true
		}
		return "", false
	})
	return ok
}
