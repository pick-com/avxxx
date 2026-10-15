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
