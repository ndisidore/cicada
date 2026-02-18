package runner

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/moby/buildkit/client"
)

const _redactMask = "***"

// redactStatus replaces secret values in SolveStatus vertex logs and errors
// with "***". Values are replaced longest-first so that a shorter secret
// that is a prefix of a longer one does not cause partial redaction.
func redactStatus(status *client.SolveStatus, secrets map[string]string) {
	if len(secrets) == 0 || status == nil {
		return
	}
	sorted := sortedSecretValues(secrets)
	for _, log := range status.Logs {
		log.Data = redactBytes(log.Data, sorted)
	}
	for _, vtx := range status.Vertexes {
		if vtx.Error != "" {
			vtx.Error = redactString(vtx.Error, sorted)
		}
	}
	for _, w := range status.Warnings {
		w.Short = redactBytes(w.Short, sorted)
		for i := range w.Detail {
			w.Detail[i] = redactBytes(w.Detail[i], sorted)
		}
	}
}

// sortedSecretValues returns non-empty secret values sorted by descending
// length so that longer values are replaced before shorter prefixes.
func sortedSecretValues(secrets map[string]string) []string {
	vals := make([]string, 0, len(secrets))
	for _, v := range secrets {
		if v != "" {
			vals = append(vals, v)
		}
	}
	slices.SortFunc(vals, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
	return vals
}

func redactBytes(data []byte, secrets []string) []byte {
	for _, v := range secrets {
		data = bytes.ReplaceAll(data, []byte(v), []byte(_redactMask))
	}
	return data
}

func redactString(s string, secrets []string) string {
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, _redactMask)
	}
	return s
}
