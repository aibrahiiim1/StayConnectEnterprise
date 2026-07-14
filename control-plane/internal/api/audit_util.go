package api

// pickNonEmpty returns a map of only the non-empty/non-nil entries. Used by
// audit payloads so PATCH rows record exactly which fields were touched.
func pickNonEmpty(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x != "" {
				out[k] = x
			}
		case nil:
			// skip
		default:
			out[k] = x
		}
	}
	return out
}
