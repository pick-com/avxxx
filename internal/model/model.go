package model

import "time"

type Item struct {
	ID        int64     `json:"id"`
	SourceID  int64     `json:"source_id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Source struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}
type Job struct {
	ID        int64     `json:"id"`
	SourceID  int64     `json:"source_id"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
}
