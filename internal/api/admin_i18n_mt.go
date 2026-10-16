package api

// 机器翻译 in the translation workbench (ROADMAP 机器翻译, tech plan 3.10):
//
//	GET  /api/admin/i18n/machine-translate  whether OpenRouter is configured, models, daily cap and usage, queue size
//	POST /api/admin/i18n/machine-translate  {"lang", "items": [{"entity_type","entity_id","field"}]} queue a selection
//
// The API never calls the provider: a request queues the selection
// (mt_requests) and enqueues the sourceless 'i18n_machine' job, which
// cmd/audit runs (internal/mtjob). Output lands as status 'machine' drafts
// with the model id and time, and is never public until submitted and
// reviewed. Fields with a human translation are skipped.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/store"
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// mtAdmin is the subset of *store.Store used by the machine translation
// routes.
type mtAdmin interface {
	QueueMachineTranslations(ctx context.Context, lang string, refs []i18n.Ref, by string) (int, error)
	MachineTranslationStatus(ctx context.Context, provider string, day time.Time) (store.MTStatus, error)
	EnqueueKindNow(ctx context.Context, kind string) (int64, bool, error)
}

// maxMTSelection bounds one machine-translation request.
const maxMTSelection = 200

func (srv *Server) adminI18nMTRoutes() []route {
	return []route{
		{"GET /api/admin/i18n/machine-translate", srv.admin(srv.withMT(srv.mtStatus))},
		{"POST /api/admin/i18n/machine-translate", srv.admin(srv.withMT(srv.mtQueue))},
	}
}

// setupMT reads the OpenRouter configuration (the key only decides
// "configured"; it is never sent anywhere from the API process).
func (srv *Server) setupMT(s mtAdmin) {
	srv.mt = s
	cfg, _ := i18n.OpenRouterConfigFromEnv(os.Getenv)
	srv.mtCfg = cfg
}

func (srv *Server) withMT(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.mt == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

func (srv *Server) mtNow() time.Time {
	if srv.clock != nil {
		return srv.clock()
	}
	return time.Now()
}

func (srv *Server) mtStatus(w http.ResponseWriter, r *http.Request) {
	st, err := srv.mt.MachineTranslationStatus(r.Context(), store.MTProviderOpenRouter, srv.mtNow())
	if err != nil {
		reply(w, nil, err)
		return
	}
	models := srv.mtCfg.Models
	if models == nil {
		models = []string{}
	}
	reply(w, map[string]any{
		"configured": srv.mtCfg.Enabled(),
		"provider":   store.MTProviderOpenRouter,
		"models":     models,
		"daily_cap":  srv.mtCfg.DailyCap,
		"used_today": st.UsedToday,
		"pending":    st.Pending,
		"failing":    st.Failing,
		"max_items":  maxMTSelection,
	}, nil)
}

// mtQueue queues a selection for machine translation and starts the job.
// 409 when machine translation is not configured.
func (srv *Server) mtQueue(w http.ResponseWriter, r *http.Request) {
	if !srv.mtCfg.Enabled() {
		reply(w, nil, &httpError{status: http.StatusConflict, msg: "machine translation is not configured (OPENROUTER_API_KEY)"})
		return
	}
	var body struct {
		Lang  string `json:"lang"`
		Items []struct {
			EntityType string `json:"entity_type"`
			EntityID   string `json:"entity_id"`
			Field      string `json:"field"`
		} `json:"items"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		reply(w, nil, err)
		return
	}
	body.Lang = strings.TrimSpace(body.Lang)
	if !i18n.Supported(body.Lang) {
		reply(w, nil, badRequest("unsupported lang", nil))
		return
	}
	if len(body.Items) == 0 || len(body.Items) > maxMTSelection {
		reply(w, nil, badRequest("items must hold 1-200 fields", nil))
		return
	}
	refs := make([]i18n.Ref, 0, len(body.Items))
	for _, it := range body.Items {
		ref := i18n.Ref{EntityType: it.EntityType, EntityID: it.EntityID, Field: it.Field}
		if err := ref.Validate(); err != nil {
			reply(w, nil, badRequest(err.Error(), nil))
			return
		}
		refs = append(refs, ref)
	}
	queued, err := srv.mt.QueueMachineTranslations(r.Context(), body.Lang, refs, adminUser(r))
	if err != nil {
		reply(w, nil, err)
		return
	}
	out := map[string]any{"requested": len(refs), "queued": queued, "skipped": len(refs) - queued}
	if queued > 0 {
		id, enqueued, err := srv.mt.EnqueueKindNow(r.Context(), store.JobKindMachineTranslation)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out["job_id"], out["enqueued"] = id, enqueued
	}
	reply(w, out, nil)
}
