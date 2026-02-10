package pipeline

// mapSlice applies fn to each element of s, returning a new slice.
// Returns nil when s is nil.
func mapSlice[T, R any](s []T, fn func(T) R) []R {
	if s == nil {
		return nil
	}
	out := make([]R, len(s))
	for i, v := range s {
		out[i] = fn(v)
	}
	return out
}

// flatMap applies fn to each element of s, concatenating the resulting slices.
// Returns nil when s is nil.
func flatMap[T, R any](s []T, fn func(T) []R) []R {
	if s == nil {
		return nil
	}
	out := make([]R, 0)
	for _, v := range s {
		out = append(out, fn(v)...)
	}
	return out
}

// collectUnique extracts values via fn, deduplicates them, and skips zero values.
// Returns nil when s is nil.
func collectUnique[T any, R comparable](s []T, fn func(T) R) []R {
	if s == nil {
		return nil
	}
	seen := make(map[R]struct{}, len(s))
	var zero R
	var out []R
	for _, v := range s {
		r := fn(v)
		if r == zero {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
