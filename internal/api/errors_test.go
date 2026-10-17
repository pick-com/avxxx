package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stubAccounts is an in-memory accountStore. Only the token "good" is valid.
type stubAccounts struct {
	pingErr     error
	registerErr error
	loginErr    error
	idErr       error // returned by UserIDByToken for the "good" token
	calls       []string
	historyQ    string      // q passed to WatchHistory
	mergedAt    []time.Time // MergeWatch `at` arguments
	mergeKept   bool        // MergeWatch: the account's entry is newer, nothing written
}

func (s *stubAccounts) Ping(context.Context) error {
	s.calls = append(s.calls, "Ping")
	return s.pingErr
}

func (s *stubAccounts) Register(_ context.Context, email, _, _ string) (map[string]any, error) {
	s.calls = append(s.calls, "Register")
	if s.registerErr != nil {
		return nil, s.registerErr
	}
	return map[string]any{"token": "t", "user": map[string]any{"id": 1, "email": email}}, nil
}

func (s *stubAccounts) Login(_ context.Context, email, _ string) (map[string]any, error) {
	s.calls = append(s.calls, "Login")
	if s.loginErr != nil {
		return nil, s.loginErr
	}
	return map[string]any{"token": "t", "user": map[string]any{"id": 1, "email": email}}, nil
}

func (s *stubAccounts) UserByToken(_ context.Context, token string) (map[string]any, error) {
	s.calls = append(s.calls, "UserByToken")
	if token != "good" {
		return nil, errors.New("unauthorized") // what store.UserByToken returns
	}
	return map[string]any{"id": 1, "email": "a@b.c"}, nil
}

func (s *stubAccounts) UserIDByToken(_ context.Context, token string) (int64, error) {
	s.calls = append(s.calls, "UserIDByToken")
	if token != "good" {
		return 0, pgx.ErrNoRows
	}
	return 1, s.idErr
}

func (s *stubAccounts) WatchHistory(_ context.Context, _ int64, q string) ([]map[string]any, error) {
	s.historyQ = q
	return []map[string]any{}, nil
}
func (s *stubAccounts) SaveWatch(context.Context, int64, string, int, int) error {
	s.calls = append(s.calls, "SaveWatch")
	return nil
}
func (s *stubAccounts) MergeWatch(_ context.Context, _ int64, _ string, _, _ int, at time.Time) (bool, error) {
	s.calls = append(s.calls, "MergeWatch")
	s.mergedAt = append(s.mergedAt, at)
	return !s.mergeKept, nil
}
func (s *stubAccounts) Favorites(context.Context, int64) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
func (s *stubAccounts) SetFavorite(context.Context, int64, string, bool) error { return nil }

func TestErrorStatus(t *testing.T) {
	dup := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	tests := []struct {
		name string
		err  error
		want int
		msg  string
	}{
		{"bad request", badRequest("invalid JSON body", errors.New("eof")), 400, "invalid JSON body"},
		{"bad request sentinel", errBadRequest, 400, "bad request"},
		{"unauthorized", unauthorized(errors.New("x")), 401, "unauthorized"},
		{"unauthorized sentinel", errUnauthorized, 401, "unauthorized"},
		{"not found sentinel", errNotFound, 404, "not found"},
		{"conflict sentinel", errConflict, 409, "conflict"},
		{"wrapped http error", fmt.Errorf("ctx: %w", errUnauthorized), 401, "unauthorized"},
		{"email taken", fmt.Errorf("%w: %w", store.ErrEmailTaken, dup), 409, "email already registered"},
		{"invalid credentials", store.ErrInvalidCredentials, 401, "invalid email or password"},
		{"no rows", fmt.Errorf("lookup: %w", pgx.ErrNoRows), 404, "not found"},
		{"unique violation", fmt.Errorf("insert: %w", dup), 409, "conflict"},
		{"other pg error", &pgconn.PgError{Code: "23503", Message: "secret detail"}, 500, "request failed"},
		{"source disabled fallback", errors.New("source disabled"), 409, "conflict"},
		{"unknown", errors.New("dial tcp 10.0.0.1: secret"), 500, "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := errorStatus(tt.err)
			if code != tt.want || msg != tt.msg {
				t.Errorf("errorStatus = %d %q, want %d %q", code, msg, tt.want, tt.msg)
			}
		})
	}
}

func TestHTTPErrorIs(t *testing.T) {
	if !errors.Is(badRequest("x", nil), errBadRequest) {
		t.Error("badRequest should match errBadRequest")
	}
	if errors.Is(badRequest("x", nil), errUnauthorized) {
		t.Error("badRequest should not match errUnauthorized")
	}
	cause := errors.New("cause")
	if !errors.Is(unauthorized(cause), cause) {
		t.Error("unauthorized should unwrap to its cause")
	}
}

func TestReplyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	reply(rec, nil, errors.New("internal secret"))
	if rec.Code != 500 {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"request failed"}` {
		t.Errorf("500 body = %s", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}

	rec = httptest.NewRecorder()
	reply(rec, map[string]bool{"ok": true}, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Errorf("success reply = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"":             "",
		"Bearer abc":   "abc",
		"Bearer  abc ": "abc",
		"Bearer ":      "",
		"Basic abc":    "",
		"abc":          "",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if got := bearerToken(r); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestUserIDHelper(t *testing.T) {
	st := &stubAccounts{}
	srv := &Server{accounts: st}

	r := httptest.NewRequest("GET", "/", nil)
	if _, err := srv.userID(r); !errors.Is(err, errUnauthorized) {
		t.Errorf("missing token: err %v, want unauthorized", err)
	}
	if len(st.calls) != 0 {
		t.Errorf("missing token should not hit the store, calls %v", st.calls)
	}

	r.Header.Set("Authorization", "Bearer bad")
	if _, err := srv.userID(r); !errors.Is(err, errUnauthorized) {
		t.Errorf("unknown token: err %v, want unauthorized", err)
	}

	st.idErr = errors.New("connection reset")
	r.Header.Set("Authorization", "Bearer good")
	if _, err := srv.userID(r); err == nil || errors.Is(err, errUnauthorized) {
		t.Errorf("db failure: err %v, want non-401 error", err)
	}
}

// GET /api/me/watch-history?q= hands the search text to the store (播放记录 search).
func TestWatchHistoryQuery(t *testing.T) {
	st := &stubAccounts{}
	srv := &Server{accounts: st}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	for _, tc := range []struct{ path, want string }{
		{"/api/me/watch-history", ""},
		{"/api/me/watch-history?q=abc-123", "abc-123"},
		{"/api/me/watch-history?q=%E4%B8%89%E4%B8%8A", "三上"},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.Header.Set("Authorization", "Bearer good")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 || st.historyQ != tc.want {
			t.Errorf("%s: status %d, q %q; want 200, %q", tc.path, rec.Code, st.historyQ, tc.want)
		}
	}
}

func TestAccountAndHealthStatusCodes(t *testing.T) {
	dup := fmt.Errorf("%w: %w", store.ErrEmailTaken, &pgconn.PgError{Code: "23505"})
	tests := []struct {
		name   string
		stub   stubAccounts
		method string
		path   string
		auth   string
		body   string
		want   int
		errMsg string // expected {"error": ...}; empty for success
	}{
		{"me/watch-history no token", stubAccounts{}, "GET", "/api/me/watch-history", "", "", 401, "unauthorized"},
		{"me/watch-history bad token", stubAccounts{}, "GET", "/api/me/watch-history", "Bearer bad", "", 401, "unauthorized"},
		{"me/watch-history POST no token", stubAccounts{}, "POST", "/api/me/watch-history", "", `{}`, 401, "unauthorized"},
		{"me/favorites no token", stubAccounts{}, "GET", "/api/me/favorites", "", "", 401, "unauthorized"},
		{"me/favorites POST bad token", stubAccounts{}, "POST", "/api/me/favorites", "Bearer bad", `{}`, 401, "unauthorized"},
		{"me/favorites db error", stubAccounts{idErr: errors.New("db down")}, "GET", "/api/me/favorites", "Bearer good", "", 500, "request failed"},
		{"me/favorites ok", stubAccounts{}, "GET", "/api/me/favorites", "Bearer good", "", 200, ""},
		{"auth/me no token", stubAccounts{}, "GET", "/api/auth/me", "", "", 401, "unauthorized"},
		{"auth/me bad token", stubAccounts{}, "GET", "/api/auth/me", "Bearer bad", "", 401, "unauthorized"},
		{"auth/me ok", stubAccounts{}, "GET", "/api/auth/me", "Bearer good", "", 200, ""},
		{"register bad json", stubAccounts{}, "POST", "/api/auth/register", "", `{`, 400, "invalid JSON body"},
		{"register short password", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"short"}`, 400, "email and password (min 8 characters) are required"},
		{"register short email", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":" a ","password":"longenough"}`, 400, "email and password (min 8 characters) are required"},
		{"register without 18+", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"longenough"}`, 400, "adult confirmation required"},
		{"register without birth date", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"longenough","adult_confirmed":true}`, 200, ""},
		{"register under 18", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"longenough","adult_confirmed":true,"birth_year":2020,"birth_month":1}`, 403, "age restricted"},
		{"register taken", stubAccounts{registerErr: dup}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"longenough","adult_confirmed":true,"birth_year":1990,"birth_month":1}`, 409, "email already registered"},
		{"register ok", stubAccounts{}, "POST", "/api/auth/register", "", `{"email":"a@b.c","password":"longenough","adult_confirmed":true,"birth_year":1990,"birth_month":1}`, 200, ""},
		{"login bad json", stubAccounts{}, "POST", "/api/auth/login", "", `not json`, 400, "invalid JSON body"},
		{"login wrong credentials", stubAccounts{loginErr: store.ErrInvalidCredentials}, "POST", "/api/auth/login", "", `{"email":"a@b.c","password":"x"}`, 401, "invalid email or password"},
		{"login db error", stubAccounts{loginErr: errors.New("login lookup: secret")}, "POST", "/api/auth/login", "", `{"email":"a@b.c","password":"x"}`, 500, "request failed"},
		{"login ok", stubAccounts{}, "POST", "/api/auth/login", "", `{"email":"a@b.c","password":"x"}`, 200, ""},
		{"health db down", stubAccounts{pingErr: errors.New("dial: secret")}, "GET", "/api/health", "", "", 503, "database unavailable"},
		{"health ok", stubAccounts{}, "GET", "/api/health", "", "", 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.stub
			srv := &Server{accounts: &st}
			mux := http.NewServeMux()
			for _, rt := range srv.routes() {
				mux.HandleFunc(rt.pattern, rt.handler)
			}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("content type %q, want application/json", ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not a single JSON value: %q", rec.Body.String())
			}
			if tt.errMsg == "" {
				if _, isErr := body["error"]; isErr {
					t.Errorf("unexpected error body %v", body)
				}
				return
			}
			if len(body) != 1 || body["error"] != tt.errMsg {
				t.Errorf("body %v, want {\"error\": %q}", body, tt.errMsg)
			}
		})
	}
}

func TestMediaLinkAdminBadRequests(t *testing.T) {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	tests := []struct{ name, path, body, msg string }{
		{"bad id", "/api/admin/releases/abc/media-link", `{"source":"s","media_id":"m"}`, "invalid id"},
		{"zero id", "/api/admin/releases/0/media-link", `{"source":"s","media_id":"m"}`, "invalid id"},
		{"bad json by release", "/api/admin/releases/1/media-link", `{`, "invalid JSON body"},
		{"missing fields by release", "/api/admin/releases/1/media-link", `{}`, "source and media_id are required"},
		{"bad json by identifier", "/api/admin/media-links/by-identifier", `[`, "invalid JSON body"},
		{"missing fields by identifier", "/api/admin/media-links/by-identifier", `{"source":"s","media_id":"m"}`, "source, media_id and identifier are required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			want := `{"error":` + mustJSON(tt.msg) + `}`
			if got := strings.TrimSpace(rec.Body.String()); got != want {
				t.Errorf("body %s, want %s", got, want)
			}
		})
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAdminToken(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	tests := []struct {
		name, token, header string
		want                int
	}{
		{"unset token rejects everything", "", "Bearer ", 401},
		{"unset token rejects bare scheme", "", "", 401},
		{"missing header", "secret", "", 401},
		{"wrong token", "secret", "Bearer secreT", 401},
		{"prefix of token", "secret", "Bearer secre", 401},
		{"wrong scheme", "secret", "secret", 401},
		{"correct token", "secret", "Bearer secret", http.StatusTeapot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &Server{adminToken: tt.token}
			req := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			srv.admin(ok)(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestAdminTokenReadOnceAtConstruction(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "first")
	h := New(nil, nil)
	t.Setenv("ADMIN_TOKEN", "second")

	// The route handler rejects bad input with 400 only after the admin check passes.
	try := func(token string) int {
		req := httptest.NewRequest("POST", "/api/admin/releases/abc/media-link", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := try("first"); got != http.StatusBadRequest {
		t.Errorf("token from construction: status %d, want 400", got)
	}
	if got := try("second"); got != http.StatusUnauthorized {
		t.Errorf("token changed after construction: status %d, want 401", got)
	}
}
