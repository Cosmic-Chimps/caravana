package nodeagent

import (
	"os"
	"strconv"
	"strings"
)

// vmMaxMapCount reads vm.max_map_count from /proc (feature 060). It returns 0 when the file can't be
// read or doesn't hold a positive number, which the report leaves out: unknown, not zero.
func (a *Agent) vmMaxMapCount() int64 {
	b, err := os.ReadFile(a.path("/proc/sys/vm/max_map_count"))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
