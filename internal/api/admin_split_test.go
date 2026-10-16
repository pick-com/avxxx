package api

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubSplitter struct {
	err      error
	req      store.SplitRequest
	undoID   int64
	undoBy   string
	calls    int
	previews int
}

func (s *stubSplitter) SplitPreview(_ context.Context, req store.SplitRequest) (store.SplitPreview, error) {
	s.previews++
	s.req = req
	return store.SplitPreview{EntityType: req.EntityType, EntityID: req.EntityID, Version: 3}, s.err
}

func (s *stubSplitter) SplitEntity(_ context.Context, req store.SplitRequest) (store.SplitResult, error) {
	s.calls++
	s.req = req
	return store.SplitResult{ReviewEventID: 5, NewID: 9, NewSlug: "airi-s"}, s.err
}

func (s *stubSplitter) UndoSplit(_ context.Context, id int64, actor, _ string) (store.UndoSplitResult, error) {
	s.undoID, s.undoBy = id, actor
	return store.UndoSplitResult{RevertedEventID: id}, s.err
}

func splitServer(st *stubSplitter) http.Handler {
	srv := &Server{splitter: st, adminToken: "secret", adminAuth: newFakeAdmins()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func splitDo(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	asAdmin(req, "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSplitHandlers(t *testing.T) {
	st := &stubSplitter{}
	h := splitServer(st)

	rec := splitDo(h, "GET", "/api/admin/performers/4/split-preview?source_records=1,%202&media_links=a,b", "")
	if rec.Code != 200 || st.req.EntityType != "performer" || st.req.EntityID != 4 || !reflect.DeepEqual(st.req.SourceRecordIDs, []int64{1, 2}) ||
		!reflect.DeepEqual(st.req.MediaLinks, []string{"a", "b"}) {
		t.Fatalf("preview: %d %s %+v", rec.Code, rec.Body, st.req)
	}
	if rec = splitDo(h, "GET", "/api/admin/releases/4/split-preview?source_records=x", ""); rec.Code != 400 {
		t.Errorf("bad ids: %d", rec.Code)
	}

	rec = splitDo(h, "POST", "/api/admin/releases/8/split", `{"source_records":[3],"version":2,"reason":"wrong code","move_user_data":{"favorites":[7]}}`)
	if rec.Code != 200 || st.req.EntityType != "release" || st.req.Version != 2 || st.req.Actor != "alice" ||
		!reflect.DeepEqual(st.req.MoveUserData, map[string][]int64{"favorites": {7}}) || !strings.Contains(rec.Body.String(), `"new_slug":"airi-s"`) {
		t.Fatalf("split: %d %s %+v", rec.Code, rec.Body, st.req)
	}
	for _, body := range []string{`{"version":2}`, `{"source_records":[3]}`, `{"source_records":[3],"version":2,"bogus":1}`, `not json`} {
		if rec = splitDo(h, "POST", "/api/admin/releases/8/split", body); rec.Code != 400 {
			t.Errorf("body %s: %d, want 400", body, rec.Code)
		}
	}

	if rec = splitDo(h, "POST", "/api/admin/splits/12/undo", ""); rec.Code != 200 || st.undoID != 12 || st.undoBy != "alice" {
		t.Errorf("undo: %d %s", rec.Code, rec.Body)
	}

	for _, c := range []struct {
		err  error
		want int
	}{
		{&store.ConflictError{Msg: "stale"}, 409},
		{&store.ValidationError{Msg: "bad selection"}, 400},
		{pgx.ErrNoRows, 404},
	} {
		st.err = c.err
		if rec = splitDo(h, "POST", "/api/admin/performers/8/split", `{"source_records":[3],"version":2}`); rec.Code != c.want {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.want)
		}
		if rec = splitDo(h, "POST", "/api/admin/splits/1/undo", `{}`); rec.Code != c.want {
			t.Errorf("undo %v: %d, want %d", c.err, rec.Code, c.want)
		}
	}

	none := &Server{adminToken: "secret", adminAuth: newFakeAdmins()}
	mux := http.NewServeMux()
	for _, rt := range none.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	if rec = splitDo(mux, "POST", "/api/admin/splits/1/undo", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no splitter: %d, want 503", rec.Code)
	}
}
