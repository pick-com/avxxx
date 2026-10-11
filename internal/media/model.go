package media

import "time"

type SearchResult struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Cover  string `json:"cover,omitempty"`
	Year   int    `json:"year,omitempty"`
	Type   string `json:"type,omitempty"`
}

type Episode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Detail struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Cover       string    `json:"cover,omitempty"`
	Description string    `json:"description,omitempty"`
	Episodes    []Episode `json:"episodes,omitempty"`
}

type Stream struct {
	URL       string            `json:"url"`
	Protocol  string            `json:"protocol"`
	Quality   string            `json:"quality,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}

type ResolveResult struct {
	Streams   []Stream `json:"streams"`
	Subtitles []Stream `json:"subtitles,omitempty"`
}
