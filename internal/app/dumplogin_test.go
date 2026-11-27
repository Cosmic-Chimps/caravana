package app

import "testing"

func TestDumpEngine(t *testing.T) {
	for engine, want := range map[string]bool{
		BackupPostgres: true, BackupMySQL: true, BackupMongoDB: true,
		BackupVolume: false, BackupSQLite: false, BackupOpenSearch: false, "": false, "redis": false,
	} {
		if got := DumpEngine(engine); got != want {
			t.Errorf("DumpEngine(%q) = %v, want %v", engine, got, want)
		}
		if got := (Backup{Engine: engine}).Dumps(); got != want {
			t.Errorf("Backup{%q}.Dumps() = %v, want %v", engine, got, want)
		}
	}
}

// lookupOf answers from a map: the names a service has and their values.
func lookupOf(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := vars[name]; return v, ok }
}

func TestFindDumpLogin(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		vars   map[string]string
		want   DumpLogin
		wantOK bool
	}{
		{"postgres defaults", BackupPostgres, map[string]string{"POSTGRES_PASSWORD": "pw"}, DumpLogin{"postgres", "pw", "postgres"}, true},
		{"postgres user and db", BackupPostgres, map[string]string{"POSTGRES_PASSWORD": "pw", "POSTGRES_USER": "app", "POSTGRES_DB": "shop"}, DumpLogin{"app", "pw", "shop"}, true},
		{"postgres db follows the user", BackupPostgres, map[string]string{"POSTGRES_PASSWORD": "pw", "POSTGRES_USER": "app"}, DumpLogin{"app", "pw", "app"}, true},
		{"postgres without a password", BackupPostgres, map[string]string{"POSTGRES_USER": "app"}, DumpLogin{User: "app", Database: "app"}, false},
		{"mariadb root", BackupMySQL, map[string]string{"MARIADB_ROOT_PASSWORD": "a"}, DumpLogin{User: "root", Password: "a"}, true},
		{"mysql root", BackupMySQL, map[string]string{"MYSQL_ROOT_PASSWORD": "b"}, DumpLogin{User: "root", Password: "b"}, true},
		{"mariadb root wins over mysql root", BackupMySQL, map[string]string{"MARIADB_ROOT_PASSWORD": "a", "MYSQL_ROOT_PASSWORD": "b"}, DumpLogin{User: "root", Password: "a"}, true},
		{"root wins over a user", BackupMySQL, map[string]string{"MYSQL_ROOT_PASSWORD": "b", "MYSQL_USER": "u", "MYSQL_PASSWORD": "p"}, DumpLogin{User: "root", Password: "b"}, true},
		{"mariadb user", BackupMySQL, map[string]string{"MARIADB_USER": "u", "MARIADB_PASSWORD": "p"}, DumpLogin{User: "u", Password: "p"}, true},
		{"mysql user", BackupMySQL, map[string]string{"MYSQL_USER": "u", "MYSQL_PASSWORD": "p"}, DumpLogin{User: "u", Password: "p"}, true},
		{"pairs aren't mixed", BackupMySQL, map[string]string{"MARIADB_USER": "u", "MYSQL_PASSWORD": "p"}, DumpLogin{}, false},
		{"mysql with only a user", BackupMySQL, map[string]string{"MYSQL_USER": "u"}, DumpLogin{}, false},
		{"mongodb", BackupMongoDB, map[string]string{"MONGO_INITDB_ROOT_USERNAME": "root", "MONGO_INITDB_ROOT_PASSWORD": "pw"}, DumpLogin{User: "root", Password: "pw"}, true},
		{"mongodb without the user", BackupMongoDB, map[string]string{"MONGO_INITDB_ROOT_PASSWORD": "pw"}, DumpLogin{}, false},
		{"opensearch initial admin", BackupOpenSearch, map[string]string{"OPENSEARCH_INITIAL_ADMIN_PASSWORD": "pw"}, DumpLogin{User: "admin", Password: "pw"}, true},
		{"opensearch initial admin wins", BackupOpenSearch, map[string]string{"OPENSEARCH_INITIAL_ADMIN_PASSWORD": "a", "INDEXER_PASSWORD": "b", "ELASTIC_PASSWORD": "c"}, DumpLogin{User: "admin", Password: "a"}, true},
		{"wazuh indexer default user", BackupOpenSearch, map[string]string{"INDEXER_PASSWORD": "pw"}, DumpLogin{User: "admin", Password: "pw"}, true},
		{"wazuh indexer user", BackupOpenSearch, map[string]string{"INDEXER_PASSWORD": "pw", "INDEXER_USERNAME": "ops"}, DumpLogin{User: "ops", Password: "pw"}, true},
		{"elastic", BackupOpenSearch, map[string]string{"ELASTIC_PASSWORD": "pw"}, DumpLogin{User: "elastic", Password: "pw"}, true},
		{"opensearch security off", BackupOpenSearch, map[string]string{"discovery.type": "single-node"}, DumpLogin{}, false},
		{"volume has no login", BackupVolume, map[string]string{"POSTGRES_PASSWORD": "pw"}, DumpLogin{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FindDumpLogin(tc.engine, lookupOf(tc.vars))
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("FindDumpLogin = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// BackupPasswordNames lists every name FindDumpLogin reads as a password, and
// no other.
func TestBackupPasswordNames(t *testing.T) {
	for _, engine := range []string{BackupPostgres, BackupMySQL, BackupMongoDB, BackupOpenSearch} {
		names := BackupPasswordNames(engine)
		if len(names) == 0 {
			t.Errorf("%s has no password names", engine)
		}
		for _, name := range names {
			read := false
			FindDumpLogin(engine, func(n string) (string, bool) {
				read = read || n == name
				return "", false
			})
			if !read {
				t.Errorf("%s: %s is listed but never read as a login", engine, name)
			}
		}
	}
	if got := BackupPasswordNames(BackupVolume); got != nil {
		t.Errorf("volume password names = %v, want none", got)
	}
}
