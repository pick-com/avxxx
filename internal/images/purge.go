package images

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/objstore"
)

// Asset is one media_assets row as the purge job and admin routes see it.
type Asset struct {
	ID               int64      `json:"id"`
	EntityType       string     `json:"entity_type"`
	EntityID         int64      `json:"entity_id"`
	AssetType        string     `json:"asset_type"`
	Variant          string     `json:"variant"`
	DerivedFrom      *int64     `json:"derived_from,omitempty"`
	ObjectKey        string     `json:"object_key"`
	MIMEType         string     `json:"mime_type"`
	Width            *int       `json:"width,omitempty"`
	Height           *int       `json:"height,omitempty"`
	ByteSize         int64      `json:"byte_size"`
	SourceID         *int64     `json:"source_id,omitempty"`
	SourceURL        string     `json:"source_url,omitempty"`
	SHA256           string     `json:"content_sha256"`
	SuppressedAt     *time.Time `json:"suppressed_at,omitempty"`
	SuppressedReason string     `json:"suppressed_reason,omitempty"`
	RetainedUntil    *time.Time `json:"retained_until,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// PurgeStore is everything the media_purge job needs from the database.
type PurgeStore interface {
	// MarkRetention sets retained_until on rows that must go (suppressed
	// rows and their variants: suppressed_at+after; rows of entities that
	// are not public: now+after) and clears it on rows whose entity is
	// public again and that are not suppressed.
	MarkRetention(ctx context.Context, now time.Time, after time.Duration) (marked, cleared int64, err error)
	// ExpiredAssets lists up to limit rows with retained_until <= now,
	// derived variants before originals.
	ExpiredAssets(ctx context.Context, now time.Time, limit int) ([]Asset, error)
	// DeleteAssets deletes rows by id (their objects are already gone).
	DeleteAssets(ctx context.Context, ids []int64) error
	// KnownObjectKeys returns which of keys a media_assets row owns.
	KnownObjectKeys(ctx context.Context, keys []string) (map[string]bool, error)
	StorageState(ctx context.Context, name string) (string, error)
	SetStorageState(ctx context.Context, name, value string) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// PurgeConfig bounds one run.
type PurgeConfig struct {
	After          time.Duration // retention after suppression / takedown (MEDIA_PURGE_AFTER, default 24h)
	OrphanGrace    time.Duration // minimum object age before an unowned object is deleted
	MaxDeletes     int           // rows per run
	OrphanScanMax  int           // bucket objects listed per run
	OrphanPageSize int
}

// DefaultPurgeConfig is the documented policy.
func DefaultPurgeConfig() PurgeConfig {
	return PurgeConfig{After: 24 * time.Hour, OrphanGrace: 24 * time.Hour, MaxDeletes: 2000, OrphanScanMax: 5000, OrphanPageSize: 1000}
}

// PurgeConfigFromEnv reads MEDIA_PURGE_AFTER (a duration in (0, 24h]: the
// policy's upper bound) and keeps the defaults otherwise.
func PurgeConfigFromEnv(getenv func(string) string) (PurgeConfig, []string) {
	c := DefaultPurgeConfig()
	var warn []string
	if v := strings.TrimSpace(getenv("MEDIA_PURGE_AFTER")); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 || d > 24*time.Hour {
			warn = append(warn, fmt.Sprintf("MEDIA_PURGE_AFTER %q must be a duration within 24h; using %s", v, c.After))
		} else {
			c.After = d
		}
	}
	return c, warn
}

// orphanCursor is the media_storage_state row holding the bucket scan position.
const orphanCursor = "orphan_scan_after"

// PurgeRunner executes the media_purge job.
type PurgeRunner struct {
	Store   PurgeStore
	Objects objstore.Storage
	Config  PurgeConfig
	JobID   int64
	Now     func() time.Time
}

// PurgeReport summarizes one run.
type PurgeReport struct {
	Marked         int64  `json:"marked"`
	Cleared        int64  `json:"cleared"`
	DeletedRows    int    `json:"deleted_rows"`
	DeletedObjects int    `json:"deleted_objects"`
	DeleteErrors   int    `json:"delete_errors"`
	Scanned        int    `json:"orphans_scanned"`
	Orphans        int    `json:"orphans_deleted"`
	ScanComplete   bool   `json:"orphan_scan_complete"`
	Stopped        string `json:"stopped,omitempty"`
}

func (r *PurgeRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run marks, deletes expired assets (object first, then row) and deletes
// orphaned objects older than the grace period.
func (r *PurgeRunner) Run(ctx context.Context) (rep PurgeReport, err error) {
	start := r.now()
	defer func() { r.record(ctx, start, rep, err) }()
	cfg := r.Config
	def := DefaultPurgeConfig()
	if cfg.After <= 0 {
		cfg.After = def.After
	}
	if cfg.OrphanGrace <= 0 {
		cfg.OrphanGrace = def.OrphanGrace
	}
	if cfg.MaxDeletes <= 0 {
		cfg.MaxDeletes = def.MaxDeletes
	}
	if cfg.OrphanScanMax <= 0 {
		cfg.OrphanScanMax = def.OrphanScanMax
	}
	if cfg.OrphanPageSize <= 0 {
		cfg.OrphanPageSize = def.OrphanPageSize
	}
	if rep.Marked, rep.Cleared, err = r.Store.MarkRetention(ctx, start, cfg.After); err != nil {
		return rep, fmt.Errorf("mark retention: %w", err)
	}
	if r.Objects == nil {
		// Without storage nothing can be deleted safely: rows stay hidden
		// (public reads filter them) until storage is configured.
		rep.Stopped = "storage_not_configured"
		return rep, nil
	}
	expired, err := r.Store.ExpiredAssets(ctx, r.now(), cfg.MaxDeletes)
	if err != nil {
		return rep, fmt.Errorf("expired assets: %w", err)
	}
	var ids []int64
	for _, a := range expired {
		if err := r.Objects.Delete(ctx, a.ObjectKey); err != nil {
			rep.DeleteErrors++
			slog.Warn("media_purge: delete object", "key", a.ObjectKey, "err", err)
			continue
		}
		rep.DeletedObjects++
		ids = append(ids, a.ID)
	}
	if len(ids) > 0 {
		if err := r.Store.DeleteAssets(ctx, ids); err != nil {
			return rep, fmt.Errorf("delete rows: %w", err)
		}
		rep.DeletedRows = len(ids)
	}
	return rep, r.orphans(ctx, cfg, &rep)
}

func (r *PurgeRunner) orphans(ctx context.Context, cfg PurgeConfig, rep *PurgeReport) error {
	after, err := r.Store.StorageState(ctx, orphanCursor)
	if err != nil {
		return fmt.Errorf("orphan cursor: %w", err)
	}
	cutoff := r.now().Add(-cfg.OrphanGrace)
	for rep.Scanned < cfg.OrphanScanMax {
		objs, more, err := r.Objects.List(ctx, "", after, min(cfg.OrphanPageSize, cfg.OrphanScanMax-rep.Scanned))
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		rep.Scanned += len(objs)
		if len(objs) > 0 {
			keys := make([]string, len(objs))
			for i, o := range objs {
				keys[i] = o.Key
			}
			known, err := r.Store.KnownObjectKeys(ctx, keys)
			if err != nil {
				return fmt.Errorf("known keys: %w", err)
			}
			for _, o := range objs {
				if known[o.Key] || o.LastModified.After(cutoff) || !managedKey(o.Key) {
					continue
				}
				if err := r.Objects.Delete(ctx, o.Key); err != nil {
					rep.DeleteErrors++
					continue
				}
				rep.Orphans++
			}
			after = objs[len(objs)-1].Key
		}
		if !more {
			after, rep.ScanComplete = "", true
			break
		}
	}
	return r.Store.SetStorageState(ctx, orphanCursor, after)
}

// managedKey reports whether key follows the image key scheme, so the
// orphan sweep never touches objects something else put in the bucket.
func managedKey(key string) bool {
	parts := strings.Split(key, "/")
	return len(parts) == 4 && (parts[0] == EntityRelease || parts[0] == EntityPerformer)
}

func (r *PurgeRunner) record(ctx context.Context, start time.Time, rep PurgeReport, cause error) {
	if r.Store == nil {
		return
	}
	detail := map[string]any{"marked": rep.Marked, "cleared": rep.Cleared, "deleted_rows": rep.DeletedRows, "deleted_objects": rep.DeletedObjects,
		"delete_errors": rep.DeleteErrors, "orphans_scanned": rep.Scanned, "orphans_deleted": rep.Orphans}
	if rep.Stopped != "" {
		detail["stopped"] = rep.Stopped
	}
	run := audit.Run{Job: JobKindPurge, JobID: r.JobID, StartedAt: start, DurationMS: r.now().Sub(start).Milliseconds(),
		Checked: rep.DeletedRows + rep.Scanned, Fixed: rep.DeletedRows + rep.Orphans, Issues: rep.DeleteErrors, Result: audit.ResultOK, Detail: detail}
	switch {
	case cause != nil:
		run.Result = audit.ResultFailed
		detail["error"] = cause.Error()
	case rep.DeleteErrors > 0:
		run.Result = audit.ResultIssues
	}
	if err := r.Store.RecordRun(context.WithoutCancel(ctx), run); err != nil {
		slog.Warn("media_purge: record run", "err", err)
	}
}
