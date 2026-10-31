package hostos

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseSecurityStatus(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	t.Run("a kernel among the packages", func(t *testing.T) {
		got, err := ParseSecurityStatus(read("pro-security-status.json"))
		if err != nil {
			t.Fatal(err)
		}
		want := SecurityStatus{
			// Sorted and once each; the ESM package is not one this host can update,
			// and an architecture suffix is cut.
			Packages: []string{"libssl3t64", "linux-image-6.8.0-149-generic", "openssl"},
			Kernel:   true,
			Count:    5,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})
	t.Run("nothing waiting", func(t *testing.T) {
		got, err := ParseSecurityStatus(read("pro-security-status-none.json"))
		if err != nil || len(got.Packages) != 0 || got.Kernel || got.Count != 0 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("a kernel metapackage is not a kernel image", func(t *testing.T) {
		got, err := ParseSecurityStatus([]byte(`{"packages":[{"package":"linux-virtual","service_name":"standard-security","status":"upgrade_available"}]}`))
		if err != nil || got.Kernel || len(got.Packages) != 1 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("names that aren't package names are dropped", func(t *testing.T) {
		got, err := ParseSecurityStatus([]byte(`{"packages":[
			{"package":"curl","service_name":"standard-security","status":"upgrade_available"},
			{"package":"Bad Name; ls","service_name":"standard-security","status":"upgrade_available"},
			{"package":"","service_name":"standard-security","status":"upgrade_available"},
			{"package":"-x","service_name":"standard-security","status":"upgrade_available"}]}`))
		if err != nil || !reflect.DeepEqual(got.Packages, []string{"curl"}) {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	for name, data := range map[string]string{
		"not json":     "pro: command not found",
		"empty":        "",
		"wrong shape":  `{"packages":"many"}`,
		"a list":       `[]`,
		"too many":     `{"packages":[` + strings.Repeat(`{"package":"a"},`, maxSecurityPackages) + `{"package":"a"}]}`,
		"truncated":    `{"packages":[{"package":"cu`,
		"error object": `{"error":"no"}x`,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			got, err := ParseSecurityStatus([]byte(data))
			if !errors.Is(err, ErrBadSecurityStatus) {
				t.Fatalf("err = %v, got %+v", err, got)
			}
			// pro's output is data from the host: it never goes into an error.
			if data != "" && strings.Contains(err.Error(), data[:min(len(data), 20)]) {
				t.Errorf("the error holds pro's output: %v", err)
			}
		})
	}
}
