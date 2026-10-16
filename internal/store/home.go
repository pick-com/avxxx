package store

// Home curation (home_slots), site announcements and their dismissals, and
// the visitor 高分短片 shelf (migrations/038_home_ops.sql; GET /api/home/*,
// /api/announcements, AdminOps /api/admin/home-slots|announcements). Every
// admin write appends a review_events row with the actor in the same
// transaction (entity_type home_slot / announcement).

import (
	"avxxx/internal/community"
	"avxxx/internal/homeops"
	"avxxx/internal/model"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// homeInvalid turns a homeops validation error into a ValidationError (400).
func homeInvalid(err error) error {
	var he *homeops.Error
	if errors.As(err, &he) {
		return &ValidationError{Msg: he.Msg}
	}
	return err
}

// HomeSlot is one curated home entry with its resolved target.
type HomeSlot struct {
	ID         int64         `json:"id"`
	SlotType   string        `json:"slot_type"`
	TargetType string        `json:"target_type"`
	ReleaseID  *int64        `json:"release_id,omitempty"`
	SeriesID   *int64        `json:"series_id,omitempty"`
	LinkURL    string        `json:"link_url,omitempty"`
	Texts      homeops.Texts `json:"texts"`
	Langs      []string      `json:"langs"`
	Regions    []string      `json:"regions"`
	StartsAt   time.Time     `json:"starts_at"`
	EndsAt     *time.Time    `json:"ends_at"`
	Priority   int           `json:"priority"`
	Enabled    bool          `json:"enabled"`
	CreatedBy  string        `json:"created_by"`
	UpdatedBy  string        `json:"updated_by"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	// Release is the target release (release slots) or the newest published
	// release of the target series (its cover stands in for the series).
	Release *model.Release `json:"release,omitempty"`
	Series  *HomeSeries    `json:"series,omitempty"`
	// TargetPublic: the target is visible on the site (links always are).
	TargetPublic bool `json:"target_public"`
}

// HomeSeries is the series target of a slot.
type HomeSeries struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	ReleaseCount int    `json:"release_count"`
}

// HomeSlotInput creates or replaces a slot. The target is release_id or
// release_slug, series_id or series_slug, or link_url.
type HomeSlotInput struct {
	SlotType    string        `json:"slot_type"`
	TargetType  string        `json:"target_type"`
	ReleaseID   int64         `json:"release_id"`
	ReleaseSlug string        `json:"release_slug"`
	SeriesID    int64         `json:"series_id"`
	SeriesSlug  string        `json:"series_slug"`
	LinkURL     string        `json:"link_url"`
	Texts       homeops.Texts `json:"texts"`
	Langs       []string      `json:"langs"`
	Regions     []string      `json:"regions"`
	StartsAt    time.Time     `json:"starts_at"`
	EndsAt      *time.Time    `json:"ends_at"`
	Priority    int           `json:"priority"`
	Enabled     *bool         `json:"enabled"`
}

// HomeSlotFilter selects the admin list: slots whose window overlaps
// [From, To) (either may be nil) and of one slot type ("" = all).
type HomeSlotFilter struct {
	From, To *time.Time
	SlotType string
}

const homeSlotColumns = `h.id,h.slot_type,h.target_type,h.release_id,h.series_id,COALESCE(h.link_url,''),h.texts,h.langs,h.regions,
	h.starts_at,h.ends_at,h.priority,h.enabled,h.created_by,h.updated_by,h.created_at,h.updated_at,
	tr.id,tr.slug,tr.canonical_title,COALESCE(tr.title_ja,''),tr.release_date,COALESCE(tr.runtime_minutes,0),COALESCE(tr.maker_name,''),tr.status,
	se.id,se.slug,se.name,COALESCE(sc.n,0),` + homeTargetPublic

// homeTargetPublic is true when the slot's target is visible on the site.
const homeTargetPublic = `(h.target_type='link'
	OR (h.target_type='release' AND tr.status='published' AND tr.merged_into_id IS NULL)
	OR (h.target_type='series' AND COALESCE(sc.n,0)>0))`

// homeSlotFrom joins the target release: the slot's own release, or the
// newest published release of its series.
const homeSlotFrom = ` FROM home_slots h
	LEFT JOIN series se ON se.id=h.series_id
	LEFT JOIN LATERAL (SELECT count(*)::int n FROM releases x WHERE x.series_id=h.series_id AND x.status='published' AND x.merged_into_id IS NULL) sc ON h.series_id IS NOT NULL
	LEFT JOIN LATERAL ((SELECT ` + homeTargetCols + ` FROM releases x WHERE x.id=h.release_id)
		UNION ALL
		(SELECT ` + homeTargetCols + ` FROM releases x WHERE x.series_id=h.series_id AND x.status='published' AND x.merged_into_id IS NULL
		ORDER BY x.release_date DESC NULLS LAST, x.id DESC LIMIT 1) LIMIT 1) tr ON true`

const homeTargetCols = `x.id,x.slug,x.canonical_title,x.title_ja,x.release_date,x.runtime_minutes,x.maker_name,x.status,x.merged_into_id`

func scanHomeSlot(row pgx.Row) (HomeSlot, error) {
	var h HomeSlot
	var texts []byte
	var rid, sid *int64
	var rslug, rtitle, rja, rmaker, rstatus, sslug, sname *string
	var rdate *time.Time
	var rrun *int
	var scount int
	err := row.Scan(&h.ID, &h.SlotType, &h.TargetType, &h.ReleaseID, &h.SeriesID, &h.LinkURL, &texts, &h.Langs, &h.Regions,
		&h.StartsAt, &h.EndsAt, &h.Priority, &h.Enabled, &h.CreatedBy, &h.UpdatedBy, &h.CreatedAt, &h.UpdatedAt,
		&rid, &rslug, &rtitle, &rja, &rdate, &rrun, &rmaker, &rstatus,
		&sid, &sslug, &sname, &scount, &h.TargetPublic)
	if err != nil {
		return h, err
	}
	h.Texts = homeops.Texts{}
	if len(texts) > 0 {
		if err := json.Unmarshal(texts, &h.Texts); err != nil {
			return h, err
		}
	}
	if h.Langs == nil {
		h.Langs = []string{}
	}
	if h.Regions == nil {
		h.Regions = []string{}
	}
	if rid != nil {
		h.Release = &model.Release{ID: *rid, Slug: homeStr(rslug), CanonicalTitle: homeStr(rtitle), TitleJA: homeStr(rja), ReleaseDate: rdate,
			MakerName: homeStr(rmaker), Status: homeStr(rstatus)}
		if rrun != nil {
			h.Release.RuntimeMinutes = *rrun
		}
	}
	if sid != nil {
		h.Series = &HomeSeries{ID: *sid, Slug: homeStr(sslug), Name: homeStr(sname), ReleaseCount: scount}
	}
	return h, nil
}

func homeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func collectHomeSlots(rows pgx.Rows, err error) ([]HomeSlot, error) {
	return collect[HomeSlot](rows, err)(func(r pgx.Rows) (HomeSlot, error) { return scanHomeSlot(r) })
}

// HomeSlots is the AdminOps list: every slot (enabled or not, public target
// or not) overlapping the filter window, by type, priority, start.
func (s *Store) HomeSlots(ctx context.Context, f HomeSlotFilter) ([]HomeSlot, error) {
	b := &sqlBuilder{}
	if f.SlotType != "" {
		b.cond("h.slot_type=" + b.arg(f.SlotType))
	}
	if f.To != nil {
		b.cond("h.starts_at<" + b.arg(*f.To))
	}
	if f.From != nil {
		b.cond("(h.ends_at IS NULL OR h.ends_at>" + b.arg(*f.From) + ")")
	}
	return collectHomeSlots(s.Query(ctx, "SELECT "+homeSlotColumns+homeSlotFrom+b.whereSQL()+
		" ORDER BY h.slot_type, h.priority DESC, h.starts_at DESC, h.id LIMIT 500", b.args...))
}

// HomeSlot returns one slot (pgx.ErrNoRows when missing).
func (s *Store) HomeSlot(ctx context.Context, id int64) (HomeSlot, error) {
	return scanHomeSlot(s.QueryRow(ctx, "SELECT "+homeSlotColumns+homeSlotFrom+" WHERE h.id=$1", id))
}

// LiveHomeSlots returns the slots live at `at` for a language ("" = zh-CN)
// and region ("" = unknown: region-targeted slots are left out) whose
// target is public, highest priority first. Used by GET /api/home/slots
// (at = now) and the AdminOps preview (any time).
func (s *Store) LiveHomeSlots(ctx context.Context, at time.Time, lang, region string) ([]HomeSlot, error) {
	if lang == "" {
		lang = "zh-CN"
	}
	return collectHomeSlots(s.Query(ctx, "SELECT "+homeSlotColumns+homeSlotFrom+`
		WHERE h.enabled AND h.starts_at<=$1 AND (h.ends_at IS NULL OR h.ends_at>$1)
		AND (cardinality(h.langs)=0 OR $2=ANY(h.langs))
		AND (cardinality(h.regions)=0 OR $3=ANY(h.regions))
		AND `+homeTargetPublic+`
		ORDER BY h.slot_type, h.priority DESC, h.starts_at DESC, h.id LIMIT 60`, at, lang, region))
}

type homeSlotRow struct {
	slotType, targetType string
	releaseID, seriesID  *int64
	link                 *string
	texts                []byte
	langs, regions       []string
	startsAt             time.Time
	endsAt               *time.Time
	priority             int
	enabled              bool
}

// normalizeHomeSlot validates in and resolves slugs to ids.
func (s *Store) normalizeHomeSlot(ctx context.Context, q dbtx, in HomeSlotInput) (homeSlotRow, error) {
	var r homeSlotRow
	r.slotType, r.targetType = strings.TrimSpace(in.SlotType), strings.TrimSpace(in.TargetType)
	if !homeops.ValidSlotType(r.slotType) {
		return r, invalid("slot_type must be hero or shelf")
	}
	if !homeops.ValidTargetType(r.targetType) {
		return r, invalid("target_type must be release, series or link")
	}
	texts, err := homeops.NormalizeTexts(in.Texts)
	if err != nil {
		return r, homeInvalid(err)
	}
	if r.langs, err = homeops.NormalizeLangs(in.Langs); err != nil {
		return r, homeInvalid(err)
	}
	if r.regions, err = homeops.NormalizeRegions(in.Regions); err != nil {
		return r, homeInvalid(err)
	}
	if err = homeops.CheckWindow(in.StartsAt, in.EndsAt); err != nil {
		return r, homeInvalid(err)
	}
	r.startsAt, r.endsAt, r.priority = in.StartsAt, in.EndsAt, in.Priority
	r.enabled = in.Enabled == nil || *in.Enabled
	switch r.targetType {
	case homeops.TargetRelease:
		id := in.ReleaseID
		if id == 0 && strings.TrimSpace(in.ReleaseSlug) != "" {
			err = q.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, strings.TrimSpace(in.ReleaseSlug)).Scan(&id)
		} else if id != 0 {
			err = q.QueryRow(ctx, `SELECT id FROM releases WHERE id=$1`, id).Scan(&id)
		} else {
			return r, invalid("release_id or release_slug is required")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return r, invalid("release not found")
		} else if err != nil {
			return r, err
		}
		r.releaseID = &id
	case homeops.TargetSeries:
		id := in.SeriesID
		if id == 0 && strings.TrimSpace(in.SeriesSlug) != "" {
			err = q.QueryRow(ctx, `SELECT id FROM series WHERE slug=$1`, strings.TrimSpace(in.SeriesSlug)).Scan(&id)
		} else if id != 0 {
			err = q.QueryRow(ctx, `SELECT id FROM series WHERE id=$1`, id).Scan(&id)
		} else {
			return r, invalid("series_id or series_slug is required")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return r, invalid("series not found")
		} else if err != nil {
			return r, err
		}
		r.seriesID = &id
	case homeops.TargetLink:
		link, err := homeops.NormalizeLink(in.LinkURL)
		if err != nil {
			return r, homeInvalid(err)
		}
		if link == "" {
			return r, invalid("link_url is required")
		}
		if !homeops.HasAny(texts, homeops.Title) {
			return r, invalid("a link slot needs a title")
		}
		r.link = &link
	}
	if r.texts, err = json.Marshal(texts); err != nil {
		return r, err
	}
	return r, nil
}

// homeEvent appends the review_events row of an AdminOps write.
func homeEvent(ctx context.Context, q dbtx, entity string, id int64, action string, before, after any, reason, actor string) error {
	var b, a []byte
	var err error
	if before != nil {
		if b, err = json.Marshal(before); err != nil {
			return err
		}
	}
	if after != nil {
		if a, err = json.Marshal(after); err != nil {
			return err
		}
	}
	_, err = q.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES($1,$2,$3,$4::jsonb,$5::jsonb,NULLIF($6,''),$7)`, entity, id, action, nullJSON(b), nullJSON(a), reason, actor)
	return err
}

func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// slotAudit is the review_events snapshot of a slot (no joined target).
func slotAudit(h HomeSlot) map[string]any {
	return map[string]any{"slot_type": h.SlotType, "target_type": h.TargetType, "release_id": h.ReleaseID, "series_id": h.SeriesID,
		"link_url": h.LinkURL, "texts": h.Texts, "langs": h.Langs, "regions": h.Regions, "starts_at": h.StartsAt, "ends_at": h.EndsAt,
		"priority": h.Priority, "enabled": h.Enabled}
}

func (s *Store) homeSlotTx(ctx context.Context, tx pgx.Tx, id int64) (HomeSlot, error) {
	return scanHomeSlot(tx.QueryRow(ctx, "SELECT "+homeSlotColumns+homeSlotFrom+" WHERE h.id=$1", id))
}

// CreateHomeSlot inserts a slot and its review event.
func (s *Store) CreateHomeSlot(ctx context.Context, in HomeSlotInput, actor string) (HomeSlot, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return HomeSlot{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.normalizeHomeSlot(ctx, tx, in)
	if err != nil {
		return HomeSlot{}, err
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO home_slots(slot_type,target_type,release_id,series_id,link_url,texts,langs,regions,starts_at,ends_at,priority,enabled,created_by,updated_by)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13,$13) RETURNING id`,
		r.slotType, r.targetType, r.releaseID, r.seriesID, r.link, string(r.texts), r.langs, r.regions, r.startsAt, r.endsAt, r.priority, r.enabled, actor).Scan(&id); err != nil {
		return HomeSlot{}, err
	}
	h, err := s.homeSlotTx(ctx, tx, id)
	if err != nil {
		return HomeSlot{}, err
	}
	if err = homeEvent(ctx, tx, "home_slot", id, "create", nil, slotAudit(h), "", actor); err != nil {
		return HomeSlot{}, err
	}
	return h, tx.Commit(ctx)
}

// UpdateHomeSlot replaces a slot (pgx.ErrNoRows when missing).
func (s *Store) UpdateHomeSlot(ctx context.Context, id int64, in HomeSlotInput, actor string) (HomeSlot, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return HomeSlot{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM home_slots WHERE id=$1 FOR UPDATE`, id); err != nil {
		return HomeSlot{}, err
	}
	before, err := s.homeSlotTx(ctx, tx, id)
	if err != nil {
		return HomeSlot{}, err
	}
	r, err := s.normalizeHomeSlot(ctx, tx, in)
	if err != nil {
		return HomeSlot{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE home_slots SET slot_type=$2,target_type=$3,release_id=$4,series_id=$5,link_url=$6,texts=$7::jsonb,langs=$8,regions=$9,
		starts_at=$10,ends_at=$11,priority=$12,enabled=$13,updated_by=$14,updated_at=now() WHERE id=$1`,
		id, r.slotType, r.targetType, r.releaseID, r.seriesID, r.link, string(r.texts), r.langs, r.regions, r.startsAt, r.endsAt, r.priority, r.enabled, actor); err != nil {
		return HomeSlot{}, err
	}
	h, err := s.homeSlotTx(ctx, tx, id)
	if err != nil {
		return HomeSlot{}, err
	}
	if err = homeEvent(ctx, tx, "home_slot", id, "update", slotAudit(before), slotAudit(h), "", actor); err != nil {
		return HomeSlot{}, err
	}
	return h, tx.Commit(ctx)
}

// DeleteHomeSlot removes a slot; the review event keeps its last state.
func (s *Store) DeleteHomeSlot(ctx context.Context, id int64, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := s.homeSlotTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM home_slots WHERE id=$1`, id); err != nil {
		return err
	}
	if err = homeEvent(ctx, tx, "home_slot", id, "delete", slotAudit(before), nil, "", actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReorderHomeSlots applies an AdminOps drag order: ids (all of one slot
// type, first = top) get priorities len(ids)*10 down to 10. One review event
// per slot whose priority changed.
func (s *Store) ReorderHomeSlots(ctx context.Context, slotType string, ids []int64, actor string) (int, error) {
	if !homeops.ValidSlotType(slotType) {
		return 0, invalid("slot_type must be hero or shelf")
	}
	if len(ids) == 0 || len(ids) > homeops.MaxReorder {
		return 0, invalid("ids must list 1 to %d slots", homeops.MaxReorder)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			return 0, invalid("ids lists slot %d twice", id)
		}
		seen[id] = true
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,slot_type,priority FROM home_slots WHERE id=ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return 0, err
	}
	prev := map[int64]int{}
	for rows.Next() {
		var id int64
		var typ string
		var p int
		if err := rows.Scan(&id, &typ, &p); err != nil {
			rows.Close()
			return 0, err
		}
		if typ != slotType {
			rows.Close()
			return 0, invalid("slot %d is not a %s slot", id, slotType)
		}
		prev[id] = p
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(prev) != len(ids) {
		return 0, invalid("unknown slot id")
	}
	changed := 0
	for i, id := range ids {
		p := (len(ids) - i) * 10
		if prev[id] == p {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE home_slots SET priority=$2,updated_by=$3,updated_at=now() WHERE id=$1`, id, p, actor); err != nil {
			return 0, err
		}
		if err = homeEvent(ctx, tx, "home_slot", id, "reorder", map[string]any{"priority": prev[id]}, map[string]any{"priority": p}, "", actor); err != nil {
			return 0, err
		}
		changed++
	}
	return changed, tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Announcements

// Announcement is one site announcement.
type Announcement struct {
	ID          int64         `json:"id"`
	Severity    string        `json:"severity"`
	Texts       homeops.Texts `json:"texts"`
	LinkURL     string        `json:"link_url,omitempty"`
	Langs       []string      `json:"langs"`
	Regions     []string      `json:"regions"`
	StartsAt    time.Time     `json:"starts_at"`
	EndsAt      *time.Time    `json:"ends_at"`
	Dismissible bool          `json:"dismissible"`
	Enabled     bool          `json:"enabled"`
	CreatedBy   string        `json:"created_by"`
	UpdatedBy   string        `json:"updated_by"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// AnnouncementInput creates or replaces an announcement.
type AnnouncementInput struct {
	Severity    string        `json:"severity"`
	Texts       homeops.Texts `json:"texts"`
	LinkURL     string        `json:"link_url"`
	Langs       []string      `json:"langs"`
	Regions     []string      `json:"regions"`
	StartsAt    time.Time     `json:"starts_at"`
	EndsAt      *time.Time    `json:"ends_at"`
	Dismissible *bool         `json:"dismissible"`
	Enabled     *bool         `json:"enabled"`
}

const announcementColumns = `id,severity,texts,COALESCE(link_url,''),langs,regions,starts_at,ends_at,dismissible,enabled,created_by,updated_by,created_at,updated_at`

func scanAnnouncement(row pgx.Row) (Announcement, error) {
	var a Announcement
	var texts []byte
	err := row.Scan(&a.ID, &a.Severity, &texts, &a.LinkURL, &a.Langs, &a.Regions, &a.StartsAt, &a.EndsAt, &a.Dismissible, &a.Enabled,
		&a.CreatedBy, &a.UpdatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, err
	}
	a.Texts = homeops.Texts{}
	if len(texts) > 0 {
		if err := json.Unmarshal(texts, &a.Texts); err != nil {
			return a, err
		}
	}
	if a.Langs == nil {
		a.Langs = []string{}
	}
	if a.Regions == nil {
		a.Regions = []string{}
	}
	return a, nil
}

func collectAnnouncements(rows pgx.Rows, err error) ([]Announcement, error) {
	return collect[Announcement](rows, err)(func(r pgx.Rows) (Announcement, error) { return scanAnnouncement(r) })
}

// Announcements is the AdminOps list, newest start first.
func (s *Store) Announcements(ctx context.Context) ([]Announcement, error) {
	return collectAnnouncements(s.Query(ctx, `SELECT `+announcementColumns+` FROM announcements ORDER BY starts_at DESC, id DESC LIMIT 500`))
}

// LiveAnnouncements returns the announcements live at `at` for a language
// and region: critical first, then newest.
func (s *Store) LiveAnnouncements(ctx context.Context, at time.Time, lang, region string) ([]Announcement, error) {
	if lang == "" {
		lang = "zh-CN"
	}
	return collectAnnouncements(s.Query(ctx, `SELECT `+announcementColumns+` FROM announcements
		WHERE enabled AND starts_at<=$1 AND (ends_at IS NULL OR ends_at>$1)
		AND (cardinality(langs)=0 OR $2=ANY(langs)) AND (cardinality(regions)=0 OR $3=ANY(regions))
		ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, starts_at DESC, id DESC LIMIT 5`, at, lang, region))
}

type announcementRow struct {
	severity             string
	texts                []byte
	link                 *string
	langs, regions       []string
	startsAt             time.Time
	endsAt               *time.Time
	dismissible, enabled bool
}

func normalizeAnnouncement(in AnnouncementInput) (announcementRow, error) {
	var r announcementRow
	r.severity = strings.TrimSpace(in.Severity)
	if r.severity == "" {
		r.severity = homeops.SeverityInfo
	}
	if !homeops.ValidSeverity(r.severity) {
		return r, invalid("severity must be info, warning or critical")
	}
	texts, err := homeops.NormalizeTexts(in.Texts)
	if err != nil {
		return r, homeInvalid(err)
	}
	if !homeops.HasAny(texts, homeops.Body) {
		return r, invalid("an announcement needs a body in at least one language")
	}
	if r.texts, err = json.Marshal(texts); err != nil {
		return r, err
	}
	link, err := homeops.NormalizeLink(in.LinkURL)
	if err != nil {
		return r, homeInvalid(err)
	}
	if link != "" {
		r.link = &link
	}
	if r.langs, err = homeops.NormalizeLangs(in.Langs); err != nil {
		return r, homeInvalid(err)
	}
	if r.regions, err = homeops.NormalizeRegions(in.Regions); err != nil {
		return r, homeInvalid(err)
	}
	if err = homeops.CheckWindow(in.StartsAt, in.EndsAt); err != nil {
		return r, homeInvalid(err)
	}
	r.startsAt, r.endsAt = in.StartsAt, in.EndsAt
	r.dismissible = in.Dismissible == nil || *in.Dismissible
	r.enabled = in.Enabled == nil || *in.Enabled
	return r, nil
}

// CreateAnnouncement inserts an announcement and its review event.
func (s *Store) CreateAnnouncement(ctx context.Context, in AnnouncementInput, actor string) (Announcement, error) {
	r, err := normalizeAnnouncement(in)
	if err != nil {
		return Announcement{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Announcement{}, err
	}
	defer tx.Rollback(ctx)
	a, err := scanAnnouncement(tx.QueryRow(ctx, `INSERT INTO announcements(severity,texts,link_url,langs,regions,starts_at,ends_at,dismissible,enabled,created_by,updated_by)
		VALUES($1,$2::jsonb,$3,$4,$5,$6,$7,$8,$9,$10,$10) RETURNING `+announcementColumns,
		r.severity, string(r.texts), r.link, r.langs, r.regions, r.startsAt, r.endsAt, r.dismissible, r.enabled, actor))
	if err != nil {
		return Announcement{}, err
	}
	if err = homeEvent(ctx, tx, "announcement", a.ID, "create", nil, announcementAudit(a), "", actor); err != nil {
		return Announcement{}, err
	}
	return a, tx.Commit(ctx)
}

func announcementAudit(a Announcement) map[string]any {
	return map[string]any{"severity": a.Severity, "texts": a.Texts, "link_url": a.LinkURL, "langs": a.Langs, "regions": a.Regions,
		"starts_at": a.StartsAt, "ends_at": a.EndsAt, "dismissible": a.Dismissible, "enabled": a.Enabled}
}

// UpdateAnnouncement replaces an announcement (pgx.ErrNoRows when missing).
func (s *Store) UpdateAnnouncement(ctx context.Context, id int64, in AnnouncementInput, actor string) (Announcement, error) {
	r, err := normalizeAnnouncement(in)
	if err != nil {
		return Announcement{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Announcement{}, err
	}
	defer tx.Rollback(ctx)
	before, err := scanAnnouncement(tx.QueryRow(ctx, `SELECT `+announcementColumns+` FROM announcements WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return Announcement{}, err
	}
	a, err := scanAnnouncement(tx.QueryRow(ctx, `UPDATE announcements SET severity=$2,texts=$3::jsonb,link_url=$4,langs=$5,regions=$6,starts_at=$7,ends_at=$8,
		dismissible=$9,enabled=$10,updated_by=$11,updated_at=now() WHERE id=$1 RETURNING `+announcementColumns,
		id, r.severity, string(r.texts), r.link, r.langs, r.regions, r.startsAt, r.endsAt, r.dismissible, r.enabled, actor))
	if err != nil {
		return Announcement{}, err
	}
	if err = homeEvent(ctx, tx, "announcement", id, "update", announcementAudit(before), announcementAudit(a), "", actor); err != nil {
		return Announcement{}, err
	}
	return a, tx.Commit(ctx)
}

// DeleteAnnouncement removes an announcement and its dismissals.
func (s *Store) DeleteAnnouncement(ctx context.Context, id int64, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := scanAnnouncement(tx.QueryRow(ctx, `DELETE FROM announcements WHERE id=$1 RETURNING `+announcementColumns, id))
	if err != nil {
		return err
	}
	if err = homeEvent(ctx, tx, "announcement", id, "delete", announcementAudit(before), nil, "", actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DismissAnnouncement records a signed-in dismissal. A missing or
// non-dismissible announcement is pgx.ErrNoRows (404).
func (s *Store) DismissAnnouncement(ctx context.Context, userID, id int64) error {
	tag, err := s.Exec(ctx, `INSERT INTO announcement_dismissals(user_id,announcement_id)
		SELECT $1,id FROM announcements WHERE id=$2 AND dismissible ON CONFLICT DO NOTHING`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var ok bool
		if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM announcement_dismissals WHERE user_id=$1 AND announcement_id=$2)`, userID, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return pgx.ErrNoRows
		}
	}
	return nil
}

// DismissedAnnouncements lists the announcement ids a user dismissed that
// are still enabled and not over (older ones no longer matter).
func (s *Store) DismissedAnnouncements(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.Query(ctx, `SELECT d.announcement_id FROM announcement_dismissals d JOIN announcements a ON a.id=d.announcement_id
		WHERE d.user_id=$1 AND a.enabled AND (a.ends_at IS NULL OR a.ends_at>now()) ORDER BY d.announcement_id`, userID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

// ---------------------------------------------------------------------------
// 高分短片 (visitor home shelf)

// Top-rated short shelf tiers (PRD 4.1 visitor home, PRD 19 ratings).
const (
	// ShortMaxMinutes is the default runtime cap of a 短片.
	ShortMaxMinutes = 60
	// ShortFewVotes is the second-tier vote minimum used when too few
	// releases reach community.MinRatingVotes. Their score stays hidden.
	ShortFewVotes = 5
)

// Basis values of TopRatedShort.
const (
	ShortBasisRated    = "rated"     // every item has >= MinRatingVotes votes
	ShortBasisFewVotes = "few_votes" // some items have ShortFewVotes..MinRatingVotes-1 votes
	ShortBasisFallback = "fallback"  // filled with the newest short releases
)

// RatedRelease is a release with its public rating.
type RatedRelease struct {
	model.Release
	// Score is the 10-point Bayesian score; nil below MinRatingVotes votes.
	Score *float64 `json:"score"`
	Votes int      `json:"votes"`
}

// TopRatedShort returns up to limit published releases of 1..maxMinutes
// minutes: by Bayesian score among releases with MinRatingVotes votes,
// then ShortFewVotes votes, then the newest short releases, and the basis
// of the weakest tier used.
func (s *Store) TopRatedShort(ctx context.Context, maxMinutes, limit int) ([]RatedRelease, string, error) {
	if maxMinutes <= 0 {
		maxMinutes = ShortMaxMinutes
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	prior, err := s.RatingPrior(ctx)
	if err != nil {
		return nil, "", err
	}
	out := []RatedRelease{}
	seen := []int64{}
	basis := ShortBasisRated
	for _, min := range []int{community.MinRatingVotes, ShortFewVotes} {
		if len(out) >= limit {
			break
		}
		rows, err := s.Query(ctx, `WITH agg AS (
			SELECT release_id, count(*)::int n, sum(weight)::float8 w, sum(weight*score)::float8 ws FROM ratings
			WHERE status='active' GROUP BY release_id HAVING count(*)>=$1)
			SELECT `+releaseColumns+`, agg.n, agg.w, agg.ws FROM agg JOIN releases r ON r.id=agg.release_id
			WHERE r.status='published' AND r.merged_into_id IS NULL AND r.runtime_minutes BETWEEN 1 AND $2 AND NOT (r.id=ANY($3))
			ORDER BY ($4*$5::float8+agg.ws)/($4+agg.w) DESC, agg.n DESC, r.id DESC LIMIT $6`,
			min, maxMinutes, seen, community.PriorWeight, prior, limit-len(out))
		if err != nil {
			return nil, "", err
		}
		got, err := collect[RatedRelease](rows, nil)(func(r pgx.Rows) (RatedRelease, error) {
			var x RatedRelease
			var agg community.RatingAggregate
			err := r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status,
				&agg.Count, &agg.WeightSum, &agg.ScoreSum)
			x.Votes = agg.Count
			x.Score = community.DisplayScore(agg, prior)
			return x, err
		})
		if err != nil {
			return nil, "", err
		}
		if min != community.MinRatingVotes && len(got) > 0 {
			basis = ShortBasisFewVotes
		}
		for _, x := range got {
			out = append(out, x)
			seen = append(seen, x.ID)
		}
	}
	if len(out) < limit {
		rows, err := s.Query(ctx, `SELECT `+releaseColumns+` FROM releases r
			WHERE r.status='published' AND r.merged_into_id IS NULL AND r.runtime_minutes BETWEEN 1 AND $1 AND NOT (r.id=ANY($2))
			ORDER BY r.release_date DESC NULLS LAST, r.id DESC LIMIT $3`, maxMinutes, seen, limit-len(out))
		if err != nil {
			return nil, "", err
		}
		got, err := collect[RatedRelease](rows, nil)(func(r pgx.Rows) (RatedRelease, error) {
			var x RatedRelease
			err := r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status)
			return x, err
		})
		if err != nil {
			return nil, "", err
		}
		if len(got) > 0 {
			basis = ShortBasisFallback
		}
		out = append(out, got...)
	}
	return out, basis, nil
}
