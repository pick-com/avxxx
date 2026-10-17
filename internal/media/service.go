package media

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

var ErrAdapterNotFound = errors.New("source adapter not found")

// MaxSearchQueryBytes bounds a provider search query: q is caller-supplied
// and becomes part of the search cache key and the provider request.
const MaxSearchQueryBytes = 512

// ErrQueryTooLong is a search query over MaxSearchQueryBytes (400).
var ErrQueryTooLong = fmt.Errorf("query is longer than %d bytes", MaxSearchQueryBytes)

type Service struct {
	registry *Registry
	timeout  time.Duration
}

func NewService(registry *Registry) *Service {
	return &Service{registry: registry, timeout: 8 * time.Second}
}

func (s *Service) adapter(name string) (Adapter, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: all", ErrAdapterNotFound)
	}
	a, ok := s.registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, name)
	}
	return a, nil
}

func (s *Service) Search(ctx context.Context, source, q string, page int) ([]SearchResult, bool, error) {
	if strings.TrimSpace(q) == "" {
		return nil, false, errors.New("query is required")
	}
	if len(q) > MaxSearchQueryBytes {
		return nil, false, ErrQueryTooLong
	}
	if page < 1 {
		return nil, false, errors.New("page must be positive")
	}
	a, err := s.adapter(source)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	items, more, err := a.Search(ctx, strings.TrimSpace(q), page)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	out := items[:0]
	for _, item := range items {
		if item.ID != "" && !seen[item.ID] {
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, more, nil
}

func (s *Service) Detail(ctx context.Context, source, id string) (*Detail, error) {
	a, err := s.adapter(source)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return a.Detail(ctx, id)
}

func (s *Service) Resolve(ctx context.Context, source, id string) (*ResolveResult, error) {
	a, err := s.adapter(source)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := a.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, stream := range result.Streams {
		if !validHTTPURL(stream.URL) {
			return nil, errors.New("resolver returned an invalid stream URL")
		}
	}
	// Signed URLs that carry their expiry cap how long the result is cached.
	now := time.Now()
	fillStreamExpiry(result.Streams, now)
	fillStreamExpiry(result.Subtitles, now)
	return result, nil
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}
