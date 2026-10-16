package api

// Patterns returns every registered route pattern ("GET /api/releases/{slug}"),
// in registration order. The acceptance suite (internal/acceptance) walks the
// public GET routes with it, so a new route is covered without editing the suite.
func Patterns() []string {
	rts := (&Server{}).routes()
	out := make([]string, 0, len(rts))
	for _, rt := range rts {
		out = append(out, rt.pattern)
	}
	return out
}
