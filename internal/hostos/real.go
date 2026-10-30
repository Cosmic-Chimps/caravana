package hostos

import (
	"io/fs"
	"os"
)

// This is the one file of the package that touches the real file system. It
// only ever hands out a read-only view: an fs.FS has no way to write.

// Root is the host's own file system: /var/log/apt/history.log is
// "var/log/apt/history.log" in it.
func Root() fs.FS { return os.DirFS("/") }

// Dir is the file system under dir, for development and tests that read a
// folder of fixtures in place of the host's files. An empty dir is the host's
// own (Root).
func Dir(dir string) fs.FS {
	if dir == "" {
		return Root()
	}
	return os.DirFS(dir)
}
