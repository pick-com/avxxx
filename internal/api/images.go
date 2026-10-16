package api

// Catalog images (internal/images, docs/DATA_RETENTION_AND_IMAGES.md).
//
// Public:
//
//	GET /api/images/{key...}   image bytes (proxy mode; checks suppression and entity visibility)
//
// Release responses carry "cover" and performer responses "portrait"
// ({url,width,height,srcset}) when a visible image is stored; the fields
// are omitted otherwise, so older clients are unaffected.
//
// Admin (the authenticated admin is the actor; changes write review_events):
//
//	GET  /api/admin/media?entity_type=release&entity_id=42
//	POST /api/admin/media/{id}/suppress {"reason": "..."}
//	POST /api/admin/media/{id}/restore  {"reason": "..."}
//	GET  /api/admin/media/sources
//	PUT  /api/admin/sources/{id}/images {"images_allowed": true, "note": "..."}
//	POST /api/admin/media/jobs {"kind": "fetch" | "purge"}

import (
	"avxxx/internal/images"
	"avxxx/internal/model"
	"avxxx/internal/objstore"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// imageStore backs the public image reads.
type imageStore interface {
	PublicImages(ctx context.Context, entityType, assetType string, ids []int64) (map[int64][]images.PublicAsset, error)
	PublicImageObject(ctx context.Context, key string) (string, error)
}

// mediaAdminStore backs the admin media routes.
type mediaAdminStore interface {
	EntityMedia(ctx context.Context, entityType string, id int64) (store.EntityMedia, error)
	SuppressMediaAsset(ctx context.Context, id int64, reason, actor string) (store.EntityMedia, error)
	RestoreMediaAsset(ctx context.Context, id int64, reason, actor string) (store.EntityMedia, error)
	SourceImagePolicies(ctx context.Context) ([]store.SourceImagePolicy, error)
	SetSourceImages(ctx context.Context, id int64, allowed bool, note, actor string) (store.SourceImagePolicy, error)
	EnqueueKind(ctx context.Context, kind string) (jobID int64, enqueued bool, err error)
}

// imageServing is the image state of the Server.
type imageServing struct {
	st      imageStore
	admin   mediaAdminStore
	objects objstore.Storage
	urls    *images.URLBuilder
}

// setupImages reads S3_* and the URL mode. Storage is optional: without
// it no image fields appear and /api/images/ is 404.
func (srv *Server) setupImages(s *store.Store) {
	if s == nil {
		return
	}
	srv.img.admin = s
	cfg, err := objstore.ConfigFromEnv(os.Getenv)
	if err != nil {
		obs.L(context.Background()).Warn("image storage disabled", "err", err)
		return
	}
	if !cfg.Enabled() {
		return
	}
	objects := objstore.NewS3(cfg, nil)
	urls, err := images.URLConfigFromEnv(os.Getenv, objects)
	if err != nil {
		obs.L(context.Background()).Warn("image URLs disabled", "err", err)
		return
	}
	srv.img.st, srv.img.objects, srv.img.urls = s, objects, urls
}

func (srv *Server) imageRoutes() []route {
	return []route{
		{"GET /api/images/{key...}", srv.serveImage},
		{"GET /api/admin/media", srv.admin(srv.withMediaAdmin(srv.entityMedia))},
		{"GET /api/admin/media/sources", srv.admin(srv.withMediaAdmin(srv.mediaSources))},
		{"POST /api/admin/media/jobs", srv.admin(srv.withMediaAdmin(srv.runMediaJob))},
		{"POST /api/admin/media/{id}/suppress", srv.admin(srv.withMediaAdmin(srv.suppressMedia))},
		{"POST /api/admin/media/{id}/restore", srv.admin(srv.withMediaAdmin(srv.restoreMedia))},
		{"PUT /api/admin/sources/{id}/images", srv.admin(srv.withMediaAdmin(srv.setSourceImages))},
	}
}

func (srv *Server) withMediaAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.img.admin == nil {
			reply(w, nil, errNotFound)
			return
		}
		h(w, r)
	}
}

// ---- public

// serveImage streams a stored image in proxy mode. Every request re-checks
// that the object belongs to a visible image of a public entity, so a
// suppression or takedown stops serving at once; shared caches may keep a
// copy for max-age (24 h, the purge window).
func (srv *Server) serveImage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if srv.img.st == nil || srv.img.objects == nil || objstore.ValidKey(key) != nil {
		http.NotFound(w, r)
		return
	}
	mt, err := srv.img.st.PublicImageObject(r.Context(), key)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			obs.RecordError(w, err)
		}
		http.NotFound(w, r)
		return
	}
	body, info, err := srv.img.objects.Get(r.Context(), key)
	if err != nil {
		if !errors.Is(err, objstore.ErrNotFound) {
			obs.RecordError(w, err)
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
		return
	}
	defer body.Close()
	h := w.Header()
	h.Set("Content-Type", mt)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// Keys embed the content hash, so the key is a strong validator.
	etag := `"` + strings.NewReplacer("/", "-", ".", "-").Replace(key) + `"`
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if info.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	if r.Method == http.MethodHead {
		return
	}
	io.Copy(w, body)
}

// publicImages looks up images for ids; failures are logged and yield no
// images (images never fail a catalog response).
func (srv *Server) publicImages(ctx context.Context, entityType string, ids []int64) map[int64]*model.Image {
	if srv.img.st == nil || srv.img.urls == nil || len(ids) == 0 {
		return nil
	}
	assets, err := srv.img.st.PublicImages(ctx, entityType, images.StoredAssetTypes[entityType], ids)
	if err != nil {
		obs.L(ctx).Warn("image lookup failed", "entity_type", entityType, "err", err)
		return nil
	}
	out := make(map[int64]*model.Image, len(assets))
	for id, vs := range assets {
		if img := srv.img.urls.Build(vs); img != nil {
			out[id] = img
		}
	}
	return out
}

// withCovers sets Cover on releases that have a visible stored cover.
func (srv *Server) withCovers(ctx context.Context, rs []model.Release) {
	ids := make([]int64, len(rs))
	for i, x := range rs {
		ids[i] = x.ID
	}
	imgs := srv.publicImages(ctx, images.EntityRelease, ids)
	for i := range rs {
		rs[i].Cover = imgs[rs[i].ID]
	}
}

// withPortraits sets Portrait on performers that have a visible photo.
func (srv *Server) withPortraits(ctx context.Context, ps []*model.Performer) {
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	imgs := srv.publicImages(ctx, images.EntityPerformer, ids)
	for _, p := range ps {
		p.Portrait = imgs[p.ID]
	}
}

func performerPtrs[T any](xs []T, get func(*T) *model.Performer) []*model.Performer {
	out := make([]*model.Performer, len(xs))
	for i := range xs {
		out[i] = get(&xs[i])
	}
	return out
}

// ---- admin

func (srv *Server) entityMedia(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, err := strconv.ParseInt(q.Get("entity_id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid entity_id", err))
		return
	}
	v, err := srv.img.admin.EntityMedia(r.Context(), q.Get("entity_type"), id)
	reply(w, v, err)
}

func (srv *Server) mediaSources(w http.ResponseWriter, r *http.Request) {
	v, err := srv.img.admin.SourceImagePolicies(r.Context())
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) suppressMedia(w http.ResponseWriter, r *http.Request) {
	srv.mediaDecision(w, r, srv.img.admin.SuppressMediaAsset)
}

func (srv *Server) restoreMedia(w http.ResponseWriter, r *http.Request) {
	srv.mediaDecision(w, r, srv.img.admin.RestoreMediaAsset)
}

func (srv *Server) mediaDecision(w http.ResponseWriter, r *http.Request, apply func(context.Context, int64, string, string) (store.EntityMedia, error)) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := apply(r.Context(), id, in.Reason, actor)
	reply(w, v, err)
}

func (srv *Server) setSourceImages(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		ImagesAllowed *bool  `json:"images_allowed"`
		Note          string `json:"note"`
	}
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.ImagesAllowed == nil {
		reply(w, nil, badRequest("images_allowed is required", nil))
		return
	}
	v, err := srv.img.admin.SetSourceImages(r.Context(), id, *in.ImagesAllowed, in.Note, actor)
	reply(w, v, err)
}

// runMediaJob queues a media_fetch or media_purge job now (the worker
// runs it; one active job per kind).
func (srv *Server) runMediaJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	kind := map[string]string{"fetch": images.JobKindFetch, "purge": images.JobKindPurge}[in.Kind]
	if kind == "" {
		reply(w, nil, badRequest("kind must be fetch or purge", nil))
		return
	}
	id, enqueued, err := srv.img.admin.EnqueueKind(r.Context(), kind)
	reply(w, map[string]any{"job_id": id, "enqueued": enqueued, "kind": kind}, err)
}
