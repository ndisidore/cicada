package progress

import (
	"strings"
	"time"
)

// IsTimeoutExitCode checks if a BuildKit vertex error indicates a step timeout.
// cfgTimeout must be the timeout parsed from the vertex annotation; when zero
// the function returns false because without a configured timeout the exit
// code is not attributable to the timeout command.
//
// Checked exit codes:
//   - 124: GNU coreutils timeout convention
//   - 137: SIGKILL (128+9); BusyBox timeout with -s KILL exits with the
//     child's signal status. The cfgTimeout guard disambiguates from OOM kills.
func IsTimeoutExitCode(vertexError string, cfgTimeout time.Duration) bool {
	if cfgTimeout == 0 {
		return false
	}
	s := strings.TrimSpace(vertexError)
	return strings.HasSuffix(s, "exit code: 124") ||
		strings.HasSuffix(s, "exit code: 137")
}
