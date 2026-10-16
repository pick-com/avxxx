package api

// 观看统计 (WatchStats, PRD 20): GET /api/me/watch-stats?range=7d|30d|365d,
// owner-only (the bearer token's user; there is no way to ask for another
// user's statistics). The body is built from user_watch_daily (migration
// 043, rebuilt hourly by cmd/audit) and kept WatchStatsTTL per user and
// range; clearing the watch history drops the cached copies.

import (
	"avxxx/internal/images"
	"avxxx/internal/store"
	"avxxx/internal/watchstats"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// WatchStatsTTL is how long a summary is served from memory. The table
// changes at most hourly, so a short TTL only absorbs page reloads.
const WatchStatsTTL = 60 * time.Second

type watchStatsStore interface {
	WatchStatsFor(ctx context.Context, userID int64, rangeName string, now time.Time) (store.WatchStats, error)
}

func watchStatsKey(uid int64, rangeName string) string {
	return "ws:" + strconv.FormatInt(uid, 10) + ":" + rangeName
}

func (srv *Server) getWatchStats(w http.ResponseWriter, r *http.Request, uid int64) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "7d"
	}
	if _, ok := watchstats.Ranges[rangeName]; !ok {
		reply(w, nil, badRequest("range must be 7d, 30d or 365d", nil))
		return
	}
	if srv.watchStats == nil {
		reply(w, nil, errNotFound)
		return
	}
	key := watchStatsKey(uid, rangeName)
	if body, ok := srv.watchStatsCache.get(key, WatchStatsTTL); ok {
		writeCachedJSON(w, body, "private, max-age=60")
		return
	}
	v, err := srv.watchStats.WatchStatsFor(r.Context(), uid, rangeName, time.Now())
	if err != nil {
		reply(w, nil, err)
		return
	}
	if len(v.Performers) > 0 {
		ids := make([]int64, len(v.Performers))
		for i, p := range v.Performers {
			ids[i] = p.ID
		}
		imgs := srv.publicImages(r.Context(), images.EntityPerformer, ids)
		for i := range v.Performers {
			v.Performers[i].Portrait = imgs[v.Performers[i].ID]
		}
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		reply(w, nil, err)
		return
	}
	srv.watchStatsCache.put(key, buf.Bytes())
	writeCachedJSON(w, buf.Bytes(), "private, max-age=60")
}

// dropWatchStats forgets the user's cached summaries.
func (srv *Server) dropWatchStats(uid int64) {
	c := srv.watchStatsCache
	if c == nil {
		return
	}
	c.mu.Lock()
	for name := range watchstats.Ranges {
		delete(c.entries, watchStatsKey(uid, name))
	}
	c.mu.Unlock()
}
