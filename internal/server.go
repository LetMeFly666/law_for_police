package internal

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	DB  *sql.DB
	Cfg Config
	Mux *http.ServeMux
}

type identity struct{ UserID, DeviceID string }
type ctxKey string

const identityKey ctxKey = "identity"

type apiError struct {
	Detail string `json:"detail"`
}

func NewServer(db *sql.DB, cfg Config) *Server {
	s := &Server{DB: db, Cfg: cfg, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.Mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.Mux.HandleFunc("GET /api/health", s.health)
	s.Mux.HandleFunc("POST /api/anonymous", s.createAnonymous)
	s.Mux.Handle("POST /api/sync/exchange", s.requireAuth(http.HandlerFunc(s.syncExchange)))
	s.Mux.Handle("POST /api/sync/join", s.requireAuth(http.HandlerFunc(s.joinByRecoveryCode)))
	s.Mux.Handle("POST /api/recovery/rotate", s.requireAuth(http.HandlerFunc(s.rotateRecoveryCode)))
	s.Mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.me)))
	s.Mux.Handle("POST /api/email/request", s.requireAuth(http.HandlerFunc(s.requestBindEmail)))
	s.Mux.Handle("POST /api/email/verify", s.requireAuth(http.HandlerFunc(s.verifyBindEmail)))
	s.Mux.Handle("POST /api/recovery/email/request", http.HandlerFunc(s.requestEmailRecovery))
	s.Mux.Handle("POST /api/recovery/email/verify", s.requireAuth(http.HandlerFunc(s.verifyEmailRecovery)))
	s.Mux.HandleFunc("GET /api/experiences", s.listExperiences)
	s.Mux.Handle("GET /api/experiences/mine", s.requireAuth(http.HandlerFunc(s.myExperiences)))
	s.Mux.Handle("POST /api/experiences", s.requireAuth(http.HandlerFunc(s.createExperience)))
	s.Mux.Handle("POST /api/feedback", s.optionalAuth(http.HandlerFunc(s.createFeedback)))
	s.Mux.HandleFunc("GET /api/admin/feedback", s.admin(s.listFeedback))
	s.Mux.HandleFunc("PATCH /api/admin/feedback/", s.admin(s.updateFeedback))
	s.Mux.HandleFunc("/", s.staticFiles)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "law-study-go"})
}

func (s *Server) createAnonymous(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	userID, err := randomUUID()
	if err != nil {
		internalError(w, err)
		return
	}
	deviceID, err := randomUUID()
	if err != nil {
		internalError(w, err)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	recovery, err := randomRecoveryCode()
	if err != nil {
		internalError(w, err)
		return
	}
	nickname := randomNicknameServer()
	now := time.Now().UnixMilli()
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,recovery_hash,nickname,created_at) VALUES(?,?,?,?)`, userID, hash(recovery), nickname, now); err != nil {
		internalError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO devices(id,user_id,token_hash,created_at,last_seen) VALUES(?,?,?,?,?)`, deviceID, userID, hash(token), now, now); err != nil {
		internalError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "recovery_code": recovery, "user_id": userID, "nickname": nickname, "created": true})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "设备身份无效，请使用永久连接码恢复账号")
			return
		}
		next.ServeHTTP(w, contextWithIdentity(r, id))
	})
}

func (s *Server) optionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, err := s.authenticate(r); err == nil {
			r = contextWithIdentity(r, id)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(r *http.Request) (identity, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return identity{}, errors.New("missing token")
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if len(token) < 32 || len(token) > 200 {
		return identity{}, errors.New("invalid token")
	}
	var id identity
	err := s.DB.QueryRowContext(r.Context(), `SELECT user_id,id FROM devices WHERE token_hash=?`, hash(token)).Scan(&id.UserID, &id.DeviceID)
	if err != nil {
		return identity{}, err
	}
	_, _ = s.DB.ExecContext(r.Context(), `UPDATE devices SET last_seen=? WHERE id=? AND last_seen<?`, time.Now().UnixMilli(), id.DeviceID, time.Now().Add(-5*time.Minute).UnixMilli())
	return id, nil
}

func (s *Server) joinByRecoveryCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	code := normalizeRecoveryCode(req.Code)
	if len(code) < 30 || len(code) > 40 {
		writeError(w, http.StatusBadRequest, "永久设备连接码格式不正确")
		return
	}
	var userID string
	err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM users WHERE recovery_hash=?`, hash(code)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "连接码无效或已被轮换")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	id := getIdentity(r)
	if _, err = s.DB.ExecContext(r.Context(), `UPDATE devices SET user_id=?,last_seen=? WHERE id=?`, userID, time.Now().UnixMilli(), id.DeviceID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user_id": userID})
}

func (s *Server) rotateRecoveryCode(w http.ResponseWriter, r *http.Request) {
	code, err := randomRecoveryCode()
	if err != nil {
		internalError(w, err)
		return
	}
	id := getIdentity(r)
	if _, err = s.DB.ExecContext(r.Context(), `UPDATE users SET recovery_hash=? WHERE id=?`, hash(code), id.UserID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_code": code})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id := getIdentity(r)
	var nickname, email string
	var verified bool
	var devices int
	err := s.DB.QueryRowContext(r.Context(), `SELECT nickname,COALESCE(email,''),email_verified FROM users WHERE id=?`, id.UserID).Scan(&nickname, &email, &verified)
	if err != nil {
		internalError(w, err)
		return
	}
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices WHERE user_id=?`, id.UserID).Scan(&devices)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": id.UserID, "nickname": nickname, "email": email, "email_verified": verified, "device_count": devices})
}

type syncChange struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt int64           `json:"updated_at"`
}

func (s *Server) syncExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cursor  int64        `json:"cursor"`
		Changes []syncChange `json:"changes"`
	}
	if !decodeJSON(w, r, &req, 2<<20) {
		return
	}
	if len(req.Changes) > 500 || req.Cursor < 0 {
		writeError(w, http.StatusBadRequest, "单次同步记录数量或游标无效")
		return
	}
	id := getIdentity(r)
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	conflicts := make([]map[string]any, 0)
	var revision int64
	if err := tx.QueryRowContext(r.Context(), `SELECT sync_version FROM users WHERE id=? FOR UPDATE`, id.UserID).Scan(&revision); err != nil {
		internalError(w, err)
		return
	}
	baseRevision := revision
	for _, change := range req.Changes {
		if !validItemKey(change.Key) || len(change.Value) == 0 || len(change.Value) > 1<<20 || change.UpdatedAt < 1 {
			writeError(w, http.StatusBadRequest, "同步数据格式无效")
			return
		}
		var parsed any
		if err := json.Unmarshal(change.Value, &parsed); err != nil {
			writeError(w, http.StatusBadRequest, "同步数据不是有效 JSON")
			return
		}
		var oldJSON string
		var oldUpdated int64
		err := tx.QueryRowContext(r.Context(), `SELECT value_json,updated_at FROM user_items WHERE user_id=? AND item_key=? FOR UPDATE`, id.UserID, change.Key).Scan(&oldJSON, &oldUpdated)
		if errors.Is(err, sql.ErrNoRows) || err == nil && change.UpdatedAt >= oldUpdated {
			// Serialize per-account writes and assign a monotonically increasing
			// revision. This lets dormant devices fetch only the latest version
			// of each item without retaining every historical drawing snapshot.
			revision++
			if _, err = tx.ExecContext(r.Context(), `INSERT INTO user_items(user_id,item_key,value_json,updated_at,sync_version) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE value_json=VALUES(value_json),updated_at=VALUES(updated_at),sync_version=VALUES(sync_version)`, id.UserID, change.Key, string(change.Value), change.UpdatedAt, revision); err != nil {
				internalError(w, err)
				return
			}
			if change.Key == "profile" {
				var profile struct {
					Nickname string `json:"nickname"`
				}
				if json.Unmarshal(change.Value, &profile) == nil && strings.TrimSpace(profile.Nickname) != "" {
					if _, err = tx.ExecContext(r.Context(), `UPDATE users SET nickname=? WHERE id=?`, cleanText(profile.Nickname, 60), id.UserID); err != nil {
						internalError(w, err)
						return
					}
				}
			}
		} else if err == nil {
			var oldValue any
			if json.Unmarshal([]byte(oldJSON), &oldValue) == nil {
				conflicts = append(conflicts, map[string]any{"key": change.Key, "value": oldValue, "updated_at": oldUpdated})
			}
		} else {
			internalError(w, err)
			return
		}
	}
	if revision != baseRevision {
		if _, err = tx.ExecContext(r.Context(), `UPDATE users SET sync_version=? WHERE id=?`, revision, id.UserID); err != nil {
			internalError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	// Read from a consistent snapshot. A cursor of zero naturally downloads
	// the current account snapshot; an old device only receives item revisions
	// newer than its last cursor. Deletions are represented as tombstones.
	readTx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		internalError(w, err)
		return
	}
	defer readTx.Rollback()
	var snapshotVersion int64
	if err := readTx.QueryRowContext(r.Context(), `SELECT sync_version FROM users WHERE id=?`, id.UserID).Scan(&snapshotVersion); err != nil {
		internalError(w, err)
		return
	}
	cursor := req.Cursor
	if cursor > snapshotVersion {
		cursor = 0 // handles a restored database or stale browser cursor
	}
	rows, err := readTx.QueryContext(r.Context(), `SELECT item_key,value_json,updated_at,sync_version FROM user_items WHERE user_id=? AND sync_version>? AND sync_version<=? ORDER BY sync_version LIMIT 501`, id.UserID, cursor, snapshotVersion)
	if err != nil {
		internalError(w, err)
		return
	}
	changes := make([]map[string]any, 0, 500)
	hasMore := false
	lastVersion := cursor
	for rows.Next() {
		var version, updated int64
		var key, raw string
		if err := rows.Scan(&key, &raw, &updated, &version); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		if len(changes) == 500 {
			hasMore = true
			break
		}
		var value any
		if json.Unmarshal([]byte(raw), &value) != nil {
			rows.Close()
			internalError(w, errors.New("stored user item is invalid JSON"))
			return
		}
		changes = append(changes, map[string]any{"key": key, "value": value, "updated_at": updated})
		lastVersion = version
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		internalError(w, err)
		return
	}
	rows.Close()
	if hasMore {
		cursor = lastVersion
	} else {
		cursor = snapshotVersion
	}
	if err := readTx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cursor": cursor, "changes": changes, "conflicts": conflicts, "has_more": hasMore})
}

func validItemKey(k string) bool {
	if k == "profile" || k == "settings" || k == "test-clear" {
		return true
	}
	if len(k) == 0 || len(k) > 190 {
		return false
	}
	for _, prefix := range []string{"note:", "favorite:", "mastery:", "test:"} {
		if strings.HasPrefix(k, prefix) && len(k) > len(prefix) {
			for _, r := range k[len(prefix):] {
				if r < 0x20 || r == 0x7f {
					return false
				}
			}
			return true
		}
	}
	return false
}

func (s *Server) listExperiences(w http.ResponseWriter, r *http.Request) {
	qid := strings.TrimSpace(r.URL.Query().Get("question_id"))
	if qid == "" || len(qid) > 80 {
		writeError(w, http.StatusBadRequest, "question_id 无效")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,question_id,nickname,content,created_at FROM experiences WHERE question_id=? ORDER BY created_at DESC LIMIT 200`, qid)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, q, n, c string
		var at int64
		if err := rows.Scan(&id, &q, &n, &c, &at); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "question_id": q, "nickname": n, "content": c, "created_at": at})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) myExperiences(w http.ResponseWriter, r *http.Request) {
	id := getIdentity(r)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,question_id,nickname,content,created_at FROM experiences WHERE user_id=? ORDER BY created_at DESC LIMIT 500`, id.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var pid, q, n, c string
		var at int64
		if err := rows.Scan(&pid, &q, &n, &c, &at); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": pid, "question_id": q, "nickname": n, "content": c, "created_at": at})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createExperience(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Nickname   string `json:"nickname"`
		Content    string `json:"content"`
	}
	if !decodeJSON(w, r, &req, 16<<10) {
		return
	}
	req.QuestionID = strings.TrimSpace(req.QuestionID)
	req.Nickname = cleanText(req.Nickname, 60)
	req.Content = strings.TrimSpace(req.Content)
	if req.QuestionID == "" || len(req.QuestionID) > 80 || len([]rune(req.Content)) < 2 || len([]rune(req.Content)) > 2000 {
		writeError(w, http.StatusBadRequest, "心得内容或题目编号无效")
		return
	}
	id := getIdentity(r)
	pid, err := randomUUID()
	if err != nil {
		internalError(w, err)
		return
	}
	at := time.Now().UnixMilli()
	if _, err = s.DB.ExecContext(r.Context(), `INSERT INTO experiences(id,user_id,question_id,nickname,content,created_at) VALUES(?,?,?,?,?,?)`, pid, id.UserID, req.QuestionID, req.Nickname, req.Content, at); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": pid, "question_id": req.QuestionID, "nickname": req.Nickname, "content": req.Content, "created_at": at})
}

func (s *Server) createFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nickname string `json:"nickname"`
		Email    string `json:"email"`
		Category string `json:"category"`
		Content  string `json:"content"`
	}
	if !decodeJSON(w, r, &req, 16<<10) {
		return
	}
	req.Nickname = cleanText(req.Nickname, 60)
	req.Email = strings.TrimSpace(req.Email)
	req.Category = cleanText(req.Category, 40)
	req.Content = strings.TrimSpace(req.Content)
	if len([]rune(req.Content)) < 5 || len([]rune(req.Content)) > 3000 {
		writeError(w, http.StatusBadRequest, "反馈内容至少 5 个字，最多 3000 字")
		return
	}
	var userID any
	if id, ok := r.Context().Value(identityKey).(identity); ok {
		userID = id.UserID
	}
	pid, err := randomUUID()
	if err != nil {
		internalError(w, err)
		return
	}
	_, err = s.DB.ExecContext(r.Context(), `INSERT INTO feedback(id,user_id,nickname,email,category,content,created_at) VALUES(?,?,?,?,?,?,?)`, pid, userID, req.Nickname, req.Email, req.Category, req.Content, time.Now().UnixMilli())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": pid})
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.Cfg.AdminToken)) != 1 {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}
func (s *Server) listFeedback(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,nickname,email,category,content,status,created_at FROM feedback ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, n, e, c, content, status string
		var at int64
		if err := rows.Scan(&id, &n, &e, &c, &content, &status, &at); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "nickname": n, "email": e, "category": c, "content": content, "status": status, "created_at": at})
	}
	writeJSON(w, http.StatusOK, items)
}
func (s *Server) updateFeedback(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/feedback/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "反馈编号无效")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	if req.Status != "new" && req.Status != "reviewing" && req.Status != "resolved" && req.Status != "rejected" {
		writeError(w, http.StatusBadRequest, "状态无效")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE feedback SET status=? WHERE id=?`, req.Status, id)
	if err != nil {
		internalError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requestBindEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "邮箱地址格式不正确")
		return
	}
	id := getIdentity(r)
	s.issueEmailCode(w, r, email, "bind", id.UserID)
}
func (s *Server) verifyBindEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	id := getIdentity(r)
	if !s.consumeEmailCode(r, email, "bind", req.Code, id.UserID) {
		writeError(w, http.StatusUnauthorized, "验证码错误或已过期")
		return
	}
	_, err := s.DB.ExecContext(r.Context(), `UPDATE users SET email=?,email_verified=1 WHERE id=?`, email, id.UserID)
	if err != nil {
		if strings.Contains(err.Error(), "Duplicate entry") || strings.Contains(err.Error(), "Error 1062") {
			writeError(w, http.StatusConflict, "该邮箱已绑定其他账号")
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email, "verified": true})
}
func (s *Server) requestEmailRecovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !validEmail(email) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	var userID string
	err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM users WHERE email=? AND email_verified=1`, email).Scan(&userID)
	if err == nil {
		s.issueEmailCode(w, r, email, "recover", userID)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) verifyEmailRecovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	var userID string
	if !s.consumeEmailCode(r, email, "recover", req.Code, "") {
		writeError(w, http.StatusUnauthorized, "验证码错误或已过期")
		return
	}
	err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM users WHERE email=? AND email_verified=1`, email).Scan(&userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "邮箱尚未绑定或账号不可恢复")
		return
	}
	code, err := randomRecoveryCode()
	if err != nil {
		internalError(w, err)
		return
	}
	id := getIdentity(r)
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET recovery_hash=? WHERE id=?`, hash(code), userID); err != nil {
		internalError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE devices SET user_id=?,last_seen=? WHERE id=?`, userID, time.Now().UnixMilli(), id.DeviceID); err != nil {
		internalError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "recovery_code": code, "user_id": userID})
}

func (s *Server) issueEmailCode(w http.ResponseWriter, r *http.Request, email, purpose, userID string) {
	if !s.Cfg.SMTPEnabled() {
		writeError(w, http.StatusServiceUnavailable, "服务器尚未配置 SMTP 邮件服务")
		return
	}
	var recent int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM email_codes WHERE email=? AND purpose=? AND created_at>?`, email, purpose, time.Now().Add(-60*time.Second).UnixMilli()).Scan(&recent)
	if recent > 0 {
		writeError(w, http.StatusTooManyRequests, "验证码发送过于频繁，请稍后再试")
		return
	}
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		internalError(w, err)
		return
	}
	code := fmt.Sprintf("%06d", (int(raw[0])<<16|int(raw[1])<<8|int(raw[2]))%1000000)
	now := time.Now().UnixMilli()
	if _, err := s.DB.ExecContext(r.Context(), `INSERT INTO email_codes(email,purpose,user_id,code_hash,expires_at,created_at) VALUES(?,?,?,?,?,?)`, email, purpose, nullIfEmpty(userID), emailCodeHash(s.Cfg.AppSecret, email, purpose, code), now+10*60*1000, now); err != nil {
		internalError(w, err)
		return
	}
	subject := "Law Study Verification Code"
	body := fmt.Sprintf("你的验证码是：%s\r\n\r\n验证码 10 分钟内有效。若非本人操作，请忽略此邮件。", code)
	msg := []byte("To: " + email + "\r\nFrom: " + s.Cfg.SMTPFrom + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body)
	addr := net.JoinHostPort(s.Cfg.SMTPHost, s.Cfg.SMTPPort)
	var auth smtp.Auth
	if s.Cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.Cfg.SMTPUser, s.Cfg.SMTPPassword, s.Cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, s.Cfg.SMTPFrom, []string{email}, msg); err != nil {
		_, _ = s.DB.ExecContext(r.Context(), `DELETE FROM email_codes WHERE email=? AND purpose=?`, email, purpose)
		log.Printf("send email failed purpose=%s: %v", purpose, err)
		writeError(w, http.StatusBadGateway, "邮件发送失败，请检查 SMTP 配置")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in": 600})
}

func (s *Server) consumeEmailCode(r *http.Request, email, purpose, code, userID string) bool {
	if !validEmail(email) || len(code) != 6 {
		return false
	}
	var stored string
	var rowUser sql.NullString
	var expires int64
	var attempts int
	err := s.DB.QueryRowContext(r.Context(), `SELECT code_hash,user_id,expires_at,attempts FROM email_codes WHERE email=? AND purpose=? ORDER BY id DESC LIMIT 1`, email, purpose).Scan(&stored, &rowUser, &expires, &attempts)
	if err != nil || time.Now().UnixMilli() > expires || attempts >= 5 {
		return false
	}
	if !constantEqual(stored, emailCodeHash(s.Cfg.AppSecret, email, purpose, code)) {
		_, _ = s.DB.ExecContext(r.Context(), `UPDATE email_codes SET attempts=attempts+1 WHERE email=? AND purpose=? ORDER BY id DESC LIMIT 1`, email, purpose)
		return false
	}
	if userID != "" && (!rowUser.Valid || rowUser.String != userID) {
		return false
	}
	_, _ = s.DB.ExecContext(r.Context(), `DELETE FROM email_codes WHERE email=? AND purpose=?`, email, purpose)
	return true
}

func (s *Server) staticFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, ".") {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/" + path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	typ := mime.TypeByExtension("." + fileExt(path))
	if typ == "" {
		if strings.HasSuffix(path, ".js") {
			typ = "text/javascript; charset=utf-8"
		} else if strings.HasSuffix(path, ".json") {
			typ = "application/json; charset=utf-8"
		} else {
			typ = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "请求 JSON 格式无效或内容过大")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "请求中包含多余内容")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Detail: msg})
}
func internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "服务端处理失败")
}
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		w.Header().Set("Allow", want)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}
func getIdentity(r *http.Request) identity {
	id, _ := r.Context().Value(identityKey).(identity)
	return id
}
func contextWithIdentity(r *http.Request, id identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityKey, id))
}
func emailCodeHash(secret, email, purpose, code string) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write([]byte(email + ":" + purpose + ":" + code))
	return hex.EncodeToString(m.Sum(nil))
}
func hash(s string) string           { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func constantEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func randomRecoveryCode() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), "="), nil
}
func normalizeRecoveryCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "", "\n", "", "\r", "").Replace(s)
}
func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	v := hex.EncodeToString(b)
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
func validEmail(v string) bool {
	if len(v) > 254 {
		return false
	}
	a, err := mail.ParseAddress(v)
	return err == nil && strings.EqualFold(a.Address, v) && strings.Contains(v, ".")
}
func cleanText(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func randomNicknameServer() string {
	adjectives := []string{"认真", "沉稳", "好学", "敏锐", "自律", "踏实", "清醒", "专注", "从容", "勤勉", "理性", "坚韧"}
	nouns := []string{"白鹭", "松鼠", "海獭", "山雀", "青竹", "星河", "小鹿", "云杉", "鲸鱼", "石榴", "远山", "月桂"}
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return adjectives[int(b[0])%len(adjectives)] + nouns[int(b[1])%len(nouns)] + fmt.Sprintf("%02d", b[0])
}
func fileExt(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return ""
}
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
