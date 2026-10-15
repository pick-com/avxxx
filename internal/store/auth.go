package store

import (
  "context"
  "crypto/rand"
  "crypto/sha256"
  "encoding/hex"
  "errors"
  "strings"
  "time"
)

func passwordHash(password string) string { h := sha256.Sum256([]byte(password)); return hex.EncodeToString(h[:]) }

func (s *Store) Register(ctx context.Context, email, password, displayName string) (map[string]any, error) {
  email = strings.ToLower(strings.TrimSpace(email)); if len(email) < 3 || len(password) < 8 { return nil, errors.New("email and password (min 8 characters) are required") }
  var id int64; err := s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,$2,$3) RETURNING id`, email, passwordHash(password), strings.TrimSpace(displayName)).Scan(&id)
  if err != nil { return nil, errors.New("email already registered") }; return s.createSession(ctx, id, email)
}
func (s *Store) Login(ctx context.Context, email, password string) (map[string]any, error) {
  var id int64; email = strings.ToLower(strings.TrimSpace(email)); if err := s.QueryRow(ctx, `SELECT id FROM users WHERE email=$1 AND password_hash=$2`, email, passwordHash(password)).Scan(&id); err != nil { return nil, errors.New("invalid email or password") }; return s.createSession(ctx, id, email)
}
func (s *Store) createSession(ctx context.Context, id int64, email string) (map[string]any, error) {
  b := make([]byte, 32); if _, err := rand.Read(b); err != nil { return nil, err }; token := hex.EncodeToString(b); exp := time.Now().Add(30 * 24 * time.Hour)
  _, err := s.Exec(ctx, `INSERT INTO user_sessions(token,user_id,expires_at) VALUES($1,$2,$3)`, token, id, exp); if err != nil { return nil, err }; return map[string]any{"token": token, "user": map[string]any{"id": id, "email": email}}, nil
}
func (s *Store) UserByToken(ctx context.Context, token string) (map[string]any, error) { var id int64; var email string; err := s.QueryRow(ctx, `SELECT u.id,u.email FROM user_sessions ss JOIN users u ON u.id=ss.user_id WHERE ss.token=$1 AND ss.expires_at>now()`, token).Scan(&id,&email); if err != nil { return nil, errors.New("unauthorized") }; return map[string]any{"id":id,"email":email},nil }
func (s *Store) UserIDByToken(ctx context.Context, token string) (int64, error) { var id int64; err:=s.QueryRow(ctx, `SELECT user_id FROM user_sessions WHERE token=$1 AND expires_at>now()`, token).Scan(&id); return id,err }
func (s *Store) SaveWatch(ctx context.Context, userID int64, slug string, progress, duration int) error { var releaseID int64; if err:=s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status='published'`,slug).Scan(&releaseID); err!=nil{return err}; _,err:=s.Exec(ctx,`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,$3,NULLIF($4,0),now()) ON CONFLICT(user_id,release_id) DO UPDATE SET progress_seconds=EXCLUDED.progress_seconds,duration_seconds=COALESCE(EXCLUDED.duration_seconds,watch_history.duration_seconds),watched_at=now()`,userID,releaseID,progress,duration); return err }
func (s *Store) WatchHistory(ctx context.Context, userID int64) ([]map[string]any,error) { rows,err:=s.Query(ctx,`SELECT r.slug,r.canonical_title,w.progress_seconds,COALESCE(w.duration_seconds,0),w.watched_at FROM watch_history w JOIN releases r ON r.id=w.release_id WHERE w.user_id=$1 ORDER BY w.watched_at DESC LIMIT 100`,userID); if err!=nil{return nil,err}; defer rows.Close(); out:=[]map[string]any{}; for rows.Next(){var slug,title string;var progress,duration int;var at time.Time;if err=rows.Scan(&slug,&title,&progress,&duration,&at);err!=nil{return nil,err};out=append(out,map[string]any{"slug":slug,"title":title,"progress_seconds":progress,"duration_seconds":duration,"watched_at":at})};return out,rows.Err() }
func (s *Store) SetFavorite(ctx context.Context,userID int64,slug string,enabled bool) error { var id int64; if err:=s.QueryRow(ctx,`SELECT id FROM releases WHERE slug=$1 AND status='published'`,slug).Scan(&id);err!=nil{return err}; if enabled {_,err:=s.Exec(ctx,`INSERT INTO favorites(user_id,release_id) VALUES($1,$2) ON CONFLICT DO NOTHING`,userID,id);return err};_,err:=s.Exec(ctx,`DELETE FROM favorites WHERE user_id=$1 AND release_id=$2`,userID,id);return err }
func (s *Store) Favorites(ctx context.Context,userID int64)([]map[string]any,error){rows,err:=s.Query(ctx,`SELECT r.slug,r.canonical_title,f.created_at FROM favorites f JOIN releases r ON r.id=f.release_id WHERE f.user_id=$1 ORDER BY f.created_at DESC`,userID);if err!=nil{return nil,err};defer rows.Close();out:=[]map[string]any{};for rows.Next(){var slug,title string;var at time.Time;if err=rows.Scan(&slug,&title,&at);err!=nil{return nil,err};out=append(out,map[string]any{"slug":slug,"title":title,"created_at":at})};return out,rows.Err()}
