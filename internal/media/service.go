package media

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrAdapterNotFound = errors.New("source adapter not found")

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
	return result, nil
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

type ResultCache struct {
	mu     sync.Mutex
	values map[string]cacheValue
}
type cacheValue struct {
	value   any
	expires time.Time
}

func NewResultCache() *ResultCache { return &ResultCache{values: map[string]cacheValue{}} }
func (c *ResultCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.values[key]
	if !ok || time.Now().After(v.expires) {
		delete(c.values, key)
		return nil, false
	}
	return v.value, true
}
func (c *ResultCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = cacheValue{value: value, expires: time.Now().Add(ttl)}
}
