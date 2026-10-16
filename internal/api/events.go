package api

import (
	"avxxx/internal/events"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	maxEventBatch     = 50        // events per POST /api/events
	maxEventBodyBytes = 256 << 10 // 256 KB
)

// eventSink is the subset of *store.Store used by event ingestion.
type eventSink interface {
	// InsertEvents stores events not seen before (by event_id) and returns
	// how many were newly inserted.
	InsertEvents(ctx context.Context, evs []events.Event) (int, error)
	QuarantineEvents(ctx context.Context, rs []events.Rejected) error
}

func (srv *Server) eventRoutes() []route {
	var sink eventSink
	if srv.store != nil {
		sink = srv.store
	}
	return []route{
		{"POST /api/events", srv.ingestEvents(sink, events.Default, time.Now)},
	}
}

type ingestResult struct {
	Accepted    int `json:"accepted"`
	Duplicates  int `json:"duplicates"`
	Quarantined int `json:"quarantined"`
}

// ingestEvents handles POST /api/events (PRD 14.5): a JSON array of up to 50
// events. Valid events are stored (deduped by event_id), invalid ones are
// quarantined with a reason, and the reply is 202 with the three counts. A
// valid bearer token attaches user_id; a missing or bad token never rejects
// the batch. Without analytics consent (or with Sec-GPC: 1) user_id and
// device_id are stripped before storing.
func (srv *Server) ingestEvents(sink eventSink, reg events.Registry, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sink == nil {
			reply(w, nil, &httpError{status: http.StatusServiceUnavailable, msg: "event store unavailable"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reply(w, nil, payloadTooLarge("body exceeds 256 KB", err))
				return
			}
			reply(w, nil, badRequest("could not read body", err))
			return
		}
		var raws []json.RawMessage
		if err := json.Unmarshal(body, &raws); err != nil {
			reply(w, nil, badRequest("body must be a JSON array of events", err))
			return
		}
		if len(raws) == 0 {
			reply(w, nil, badRequest("empty event batch", nil))
			return
		}
		if len(raws) > maxEventBatch {
			reply(w, nil, payloadTooLarge("too many events (max 50 per batch)", nil))
			return
		}

		batch := reg.ProcessBatch(raws, now(), srv.optionalUserID(r), r.Header.Get("Sec-GPC") == "1")

		inserted, err := sink.InsertEvents(r.Context(), batch.Valid)
		if err != nil {
			reply(w, nil, err) // 500: the client keeps the batch and retries; dedupe makes that safe
			return
		}
		if err := sink.QuarantineEvents(r.Context(), batch.Rejected); err != nil {
			// Not worth failing the batch (and re-sending valid events) over.
			log.Printf("events: quarantine %d events: %v", len(batch.Rejected), err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(ingestResult{
			Accepted:    inserted,
			Duplicates:  len(batch.Valid) - inserted + batch.InBatchDups,
			Quarantined: len(batch.Rejected),
		})
	}
}

// optionalUserID returns the user id for a valid bearer token, or nil for
// anonymous requests and invalid tokens. Events are accepted either way.
func (srv *Server) optionalUserID(r *http.Request) *int64 {
	if srv.accounts == nil || bearerToken(r) == "" {
		return nil
	}
	id, err := srv.userID(r)
	if err != nil {
		if !errors.Is(err, errUnauthorized) {
			log.Printf("events: resolve user: %v", err)
		}
		return nil
	}
	return &id
}

// payloadTooLarge returns a 413 error with a client-visible message.
func payloadTooLarge(msg string, cause error) error {
	return &httpError{status: http.StatusRequestEntityTooLarge, msg: msg, err: cause}
}
