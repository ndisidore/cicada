package progress

import "strings"

// CmdInfo holds the original user command and the full internal command
// for a BuildKit vertex. Populated at build time, carried through the
// pipeline for display layers to show clean user-facing output while
// preserving the full command for debug logging.
type CmdInfo struct {
	UserCmd string // original user command as written in the pipeline
	FullCmd string // full shell command including preamble and retry env prefix
}

// ExitInfo extracts the exit info suffix from a BuildKit process error string.
// Returns the portion after "did not complete successfully: " (e.g. "exit code: 1").
// Returns the full string unchanged if the marker is not found.
func ExitInfo(errStr string) string {
	const marker = "did not complete successfully: "
	if idx := strings.LastIndex(errStr, marker); idx >= 0 {
		return errStr[idx+len(marker):]
	}
	return errStr
}
