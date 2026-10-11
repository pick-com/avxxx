package media

import "context"

// Adapter is the boundary for logic extracted from a Kodi source plugin.
// Implementations must only access content they are authorized to access.
type Adapter interface {
	Name() string
	Search(context.Context, string, int) ([]SearchResult, bool, error)
	Detail(context.Context, string) (*Detail, error)
	Resolve(context.Context, string) (*ResolveResult, error)
}

type Registry struct{ adapters map[string]Adapter }

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

func (r *Registry) Register(a Adapter) { r.adapters[a.Name()] = a }

func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		out = append(out, name)
	}
	return out
}
