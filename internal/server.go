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
		if r.URL.Path == "/" || r.URL.Path == "/admin" || r.URL.Path == "/admin/" || strings.HasPrefix(r.URL.Path, "/api/") {
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
	s.Mux.Handle("GET /api/experiences", s.optionalAuth(http.HandlerFunc(s.listExperiences)))
	s.Mux.Handle("GET /api/experiences/mine", s.requireAuth(http.HandlerFunc(s.myExperiences)))
	s.Mux.Handle("POST /api/experiences", s.requireAuth(http.HandlerFunc(s.createExperience)))
	s.Mux.Handle("PATCH /api/experiences/", s.requireAuth(http.HandlerFunc(s.updateExperience)))
	s.Mux.Handle("DELETE /api/experiences/", s.requireAuth(http.HandlerFunc(s.deleteExperience)))
	s.Mux.Handle("POST /api/experiences/", s.requireAuth(http.HandlerFunc(s.toggleExperienceLike)))
	s.Mux.Handle("GET /api/experiences/", s.optionalAuth(http.HandlerFunc(s.experienceLikes)))
	s.Mux.Handle("GET /api/study-time", s.requireAuth(http.HandlerFunc(s.getStudyTime)))
	s.Mux.Handle("POST /api/study-time", s.requireAuth(http.HandlerFunc(s.addStudyTime)))
	s.Mux.Handle("POST /api/feedback", s.optionalAuth(http.HandlerFunc(s.createFeedback)))
	s.Mux.HandleFunc("GET /api/admin/overview", s.admin(s.adminOverview))
	s.Mux.HandleFunc("GET /api/admin/users", s.admin(s.adminUsers))
	s.Mux.HandleFunc("GET /api/admin/feedback", s.admin(s.listFeedback))
	s.Mux.HandleFunc("PATCH /api/admin/feedback/", s.admin(s.updateFeedback))
	s.Mux.HandleFunc("PATCH /api/admin/users/", s.admin(s.updateAdminUser))
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
	encryptedRecovery, encErr := encryptRecoveryCode(s.Cfg.AppSecret, recovery)
	if encErr != nil {
		internalError(w, encErr)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,recovery_hash,recovery_code_ciphertext,nickname,created_at) VALUES(?,?,?,?,?)`, userID, hash(recovery), encryptedRecovery, nickname, now); err != nil {
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
	encrypted, err := encryptRecoveryCode(s.Cfg.AppSecret, code)
	if err != nil {
		internalError(w, err)
		return
	}
	id := getIdentity(r)
	if _, err = s.DB.ExecContext(r.Context(), `UPDATE users SET recovery_hash=?,recovery_code_ciphertext=? WHERE id=?`, hash(code), encrypted, id.UserID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_code": code})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := getIdentity(r)
	var nickname, email string
	var encrypted sql.NullString
	var verified bool
	var devices int
	err := s.DB.QueryRowContext(r.Context(), `SELECT nickname,COALESCE(email,''),email_verified,recovery_code_ciphertext FROM users WHERE id=?`, id.UserID).Scan(&nickname, &email, &verified, &encrypted)
	if err != nil {
		internalError(w, err)
		return
	}
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM devices WHERE user_id=?`, id.UserID).Scan(&devices)
	recoveryCode := ""
	if encrypted.Valid && encrypted.String != "" {
		recoveryCode, err = decryptRecoveryCode(s.Cfg.AppSecret, encrypted.String)
		if err != nil {
			log.Printf("decrypt recovery code failed user=%s: %v", id.UserID, err)
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": id.UserID, "nickname": nickname, "email": email, "email_verified": verified, "device_count": devices, "recovery_code": recoveryCode})
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
	for _, prefix := range []string{"note:", "note-text:", "note-strokes:", "favorite:", "mastery:", "test:"} {
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

func pageArgs(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 10
	}
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	if offset > 1_000_000 {
		offset = 1_000_000
	}
	return
}

func (s *Server) listExperiences(w http.ResponseWriter, r *http.Request) {
	qid := strings.TrimSpace(r.URL.Query().Get("question_id"))
	if qid == "" || len(qid) > 80 {
		writeError(w, http.StatusBadRequest, "question_id 无效")
		return
	}
	limit, offset := pageArgs(r)
	var total int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM experiences WHERE question_id=?`, qid).Scan(&total); err != nil {
		internalError(w, err)
		return
	}
	id := getIdentity(r)
	order := `likes_count DESC,e.created_at DESC`
	if r.URL.Query().Get("sort") == "new" {
		order = `e.created_at DESC`
	}
	query := `SELECT e.id,e.question_id,COALESCE(NULLIF(u.nickname,''),e.nickname),e.content,e.created_at,e.updated_at,
		(SELECT COUNT(*) FROM experience_likes l WHERE l.experience_id=e.id) AS likes_count,
		EXISTS(SELECT 1 FROM experience_likes ml WHERE ml.experience_id=e.id AND ml.user_id=?) AS liked_by_me
		FROM experiences e JOIN users u ON u.id=e.user_id WHERE e.question_id=? ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	rows, err := s.DB.QueryContext(r.Context(), query, id.UserID, qid, limit, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var eid, q, nickname, content string
		var created, updated int64
		var likes int
		var liked bool
		if err := rows.Scan(&eid, &q, &nickname, &content, &created, &updated, &likes, &liked); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": eid, "question_id": q, "nickname": nickname, "content": content, "created_at": created, "updated_at": updated, "likes_count": likes, "liked_by_me": liked})
	}
	if err := rows.Err(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) myExperiences(w http.ResponseWriter, r *http.Request) {
	id := getIdentity(r)
	limit, offset := pageArgs(r)
	var total int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM experiences WHERE user_id=?`, id.UserID).Scan(&total); err != nil {
		internalError(w, err)
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT e.id,e.question_id,COALESCE(NULLIF(u.nickname,''),e.nickname),e.content,e.created_at,e.updated_at,
		(SELECT COUNT(*) FROM experience_likes l WHERE l.experience_id=e.id),EXISTS(SELECT 1 FROM experience_likes ml WHERE ml.experience_id=e.id AND ml.user_id=?)
		FROM experiences e JOIN users u ON u.id=e.user_id WHERE e.user_id=? ORDER BY e.created_at DESC LIMIT ? OFFSET ?`, id.UserID, id.UserID, limit, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var eid, q, n, c string
		var at, updated int64
		var likes int
		var liked bool
		if err := rows.Scan(&eid, &q, &n, &c, &at, &updated, &likes, &liked); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": eid, "question_id": q, "nickname": n, "content": c, "created_at": at, "updated_at": updated, "likes_count": likes, "liked_by_me": liked})
	}
	if err := rows.Err(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
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
	if req.QuestionID == "" || len(req.QuestionID) > 80 || len([]rune(req.Content)) < 1 || len([]rune(req.Content)) > 2000 {
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
	if _, err = s.DB.ExecContext(r.Context(), `INSERT INTO experiences(id,user_id,question_id,nickname,content,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, pid, id.UserID, req.QuestionID, req.Nickname, req.Content, at, at); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": pid, "question_id": req.QuestionID, "nickname": req.Nickname, "content": req.Content, "created_at": at, "updated_at": at, "likes_count": 0, "liked_by_me": false})
}

func experienceID(path string) string {
	id := strings.TrimPrefix(path, "/api/experiences/")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}
func (s *Server) updateExperience(w http.ResponseWriter, r *http.Request) {
	id := experienceID(r.URL.Path)
	if id == "" {
		writeError(w, 400, "心得编号无效")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &req, 8<<10) {
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if len([]rune(req.Content)) < 1 || len([]rune(req.Content)) > 2000 {
		writeError(w, 400, "心得内容长度应为 1—2000 字")
		return
	}
	who := getIdentity(r)
	res, err := s.DB.ExecContext(r.Context(), `UPDATE experiences SET content=?,updated_at=? WHERE id=? AND user_id=?`, req.Content, time.Now().UnixMilli(), id, who.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "心得不存在或无权编辑")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) deleteExperience(w http.ResponseWriter, r *http.Request) {
	id := experienceID(r.URL.Path)
	if id == "" {
		writeError(w, 400, "心得编号无效")
		return
	}
	who := getIdentity(r)
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM experiences WHERE id=? AND user_id=?`, id, who.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "心得不存在或无权删除")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) toggleExperienceLike(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/like") {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/experiences/"), "/like")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, 400, "心得编号无效")
		return
	}
	who := getIdentity(r)
	var exists int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM experiences WHERE id=?`, id).Scan(&exists); err != nil {
		internalError(w, err)
		return
	}
	if exists == 0 {
		writeError(w, 404, "心得不存在")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT IGNORE INTO experience_likes(experience_id,user_id,created_at) VALUES(?,?,?)`, id, who.UserID, time.Now().UnixMilli())
	if err != nil {
		internalError(w, err)
		return
	}
	inserted, _ := res.RowsAffected()
	liked := inserted > 0
	if !liked {
		if _, err = s.DB.ExecContext(r.Context(), `DELETE FROM experience_likes WHERE experience_id=? AND user_id=?`, id, who.UserID); err != nil {
			internalError(w, err)
			return
		}
	}
	var count int
	if err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM experience_likes WHERE experience_id=?`, id).Scan(&count); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"liked": liked, "likes_count": count})
}
func (s *Server) experienceLikes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/experiences/")
	if !strings.HasSuffix(path, "/likes") {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(path, "/likes")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, 400, "心得编号无效")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT COALESCE(NULLIF(u.nickname,''),'匿名同学') FROM experience_likes l JOIN users u ON u.id=l.user_id WHERE l.experience_id=? ORDER BY l.created_at DESC LIMIT 100`, id)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]string, 0)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]string{"nickname": n})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) getStudyTime(w http.ResponseWriter, r *http.Request) {
	who := getIdentity(r)
	var total int64
	err := s.DB.QueryRowContext(r.Context(), `SELECT total_ms FROM study_time WHERE user_id=?`, who.UserID).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		total = 0
	} else if err != nil {
		internalError(w, err)
		return
	}
	batchApplied := false
	if batchID := r.URL.Query().Get("batch_id"); batchID != "" && len(batchID) == 36 {
		if err := s.DB.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM study_time_batches WHERE user_id=? AND batch_id=?)`, who.UserID, batchID).Scan(&batchApplied); err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"total_ms": total, "batch_applied": batchApplied})
}
func (s *Server) addStudyTime(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeltaMS int64  `json:"delta_ms"`
		BatchID string `json:"batch_id"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	if req.DeltaMS < 1 || req.DeltaMS > 60000 || len(req.BatchID) != 36 || strings.Count(req.BatchID, "-") != 4 {
		writeError(w, 400, "学习时长批次数据无效")
		return
	}
	who := getIdentity(r)
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `INSERT IGNORE INTO study_time_batches(user_id,batch_id,delta_ms,created_at) VALUES(?,?,?,?)`, who.UserID, req.BatchID, req.DeltaMS, time.Now().UnixMilli())
	if err != nil {
		internalError(w, err)
		return
	}
	inserted, _ := result.RowsAffected()
	if inserted > 0 {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO study_time(user_id,total_ms,updated_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE total_ms=total_ms+VALUES(total_ms),updated_at=VALUES(updated_at)`, who.UserID, req.DeltaMS, time.Now().UnixMilli()); err != nil {
			internalError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	var total int64
	if err = s.DB.QueryRowContext(r.Context(), `SELECT total_ms FROM study_time WHERE user_id=?`, who.UserID).Scan(&total); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"total_ms": total})
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
	if len([]rune(req.Content)) < 1 || len([]rune(req.Content)) > 3000 {
		writeError(w, http.StatusBadRequest, "反馈内容不能为空，最多 3000 字")
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
	mailSent := s.notifyFeedback(req.Nickname, req.Email, req.Category, req.Content, pid)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": pid, "notification_sent": mailSent})
}

func (s *Server) notifyFeedback(nickname, email, category, content, id string) bool {
	if !s.Cfg.FeedbackNotificationEnabled() {
		return false
	}
	to := s.Cfg.FeedbackNotificationTo
	body := fmt.Sprintf("收到新的法律学习本反馈\r\n编号：%s\r\n昵称：%s\r\n联系邮箱：%s\r\n类型：%s\r\n\r\n%s", id, nickname, email, category, content)
	msg := []byte("To: " + to + "\r\nFrom: " + s.Cfg.SMTPFrom + "\r\nSubject: [法律学习本] 新反馈\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body)
	addr := net.JoinHostPort(s.Cfg.SMTPHost, s.Cfg.SMTPPort)
	var auth smtp.Auth
	if s.Cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.Cfg.SMTPUser, s.Cfg.SMTPPassword, s.Cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, s.Cfg.SMTPFrom, []string{to}, msg); err != nil {
		log.Printf("send feedback notification failed id=%s: %v", id, err)
		return false
	}
	return true
}

type adminNoteFlags struct {
	text    bool
	strokes int
}

type adminNoteStats struct {
	notes          int
	annotatedNotes int
	strokes        int
	bytes          int64
}

// collectAdminNoteStats counts one logical note per question. The v1 `note:`
// record is still recognized, while current clients store text and strokes
// independently to prevent editing one from overwriting the other.
func (s *Server) collectAdminNoteStats(ctx context.Context) (map[string]adminNoteStats, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT user_id,item_key,value_json FROM user_items WHERE item_key LIKE 'note:%' OR item_key LIKE 'note-text:%' OR item_key LIKE 'note-strokes:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type logicalNote struct {
		text    bool
		strokes int
	}
	byUser := make(map[string]map[string]*logicalNote)
	bytesByUser := make(map[string]int64)
	for rows.Next() {
		var userID, key, raw string
		if err := rows.Scan(&userID, &key, &raw); err != nil {
			return nil, err
		}
		bytesByUser[userID] += int64(len(raw))
		qid := ""
		if strings.HasPrefix(key, "note-text:") {
			qid = strings.TrimPrefix(key, "note-text:")
		}
		if strings.HasPrefix(key, "note-strokes:") {
			qid = strings.TrimPrefix(key, "note-strokes:")
		}
		if strings.HasPrefix(key, "note:") {
			qid = strings.TrimPrefix(key, "note:")
		}
		if qid == "" {
			continue
		}
		if byUser[userID] == nil {
			byUser[userID] = make(map[string]*logicalNote)
		}
		if byUser[userID][qid] == nil {
			byUser[userID][qid] = &logicalNote{}
		}
		entry := byUser[userID][qid]
		switch {
		case strings.HasPrefix(key, "note-text:"):
			var text string
			if json.Unmarshal([]byte(raw), &text) == nil && strings.TrimSpace(text) != "" {
				entry.text = true
			}
		case strings.HasPrefix(key, "note-strokes:"):
			var paths []struct {
				Deleted bool `json:"deleted"`
			}
			if json.Unmarshal([]byte(raw), &paths) != nil {
				var payload struct {
					Strokes []struct {
						Deleted bool `json:"deleted"`
					} `json:"strokes"`
				}
				_ = json.Unmarshal([]byte(raw), &payload)
				paths = payload.Strokes
			}
			active := 0
			for _, stroke := range paths {
				if !stroke.Deleted {
					active++
				}
			}
			if active > entry.strokes {
				entry.strokes = active
			}
		case strings.HasPrefix(key, "note:"):
			var legacy struct {
				Text    string            `json:"text"`
				Strokes []json.RawMessage `json:"strokes"`
			}
			if json.Unmarshal([]byte(raw), &legacy) == nil {
				if strings.TrimSpace(legacy.Text) != "" {
					entry.text = true
				}
				if len(legacy.Strokes) > entry.strokes {
					entry.strokes = len(legacy.Strokes)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]adminNoteStats, len(byUser))
	for userID, notes := range byUser {
		stat := adminNoteStats{bytes: bytesByUser[userID]}
		for _, note := range notes {
			if note.text || note.strokes > 0 {
				stat.notes++
			}
			if note.strokes > 0 {
				stat.annotatedNotes++
			}
			stat.strokes += note.strokes
		}
		out[userID] = stat
	}
	return out, nil
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var users, devices, activeDevices, experiences, feedback, feedbackNew, studyMillis int64
	queries := []struct {
		query string
		dest  *int64
	}{
		{`SELECT COUNT(*) FROM users`, &users},
		{`SELECT COUNT(*) FROM devices`, &devices},
		{`SELECT COUNT(*) FROM devices WHERE last_seen>=?`, &activeDevices},
		{`SELECT COUNT(*) FROM experiences`, &experiences},
		{`SELECT COUNT(*) FROM feedback`, &feedback},
		{`SELECT COUNT(*) FROM feedback WHERE status='new'`, &feedbackNew},
		{`SELECT COALESCE(SUM(total_ms),0) FROM study_time`, &studyMillis},
	}
	for i, item := range queries {
		var err error
		if i == 2 {
			err = s.DB.QueryRowContext(ctx, item.query, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(item.dest)
		} else {
			err = s.DB.QueryRowContext(ctx, item.query).Scan(item.dest)
		}
		if err != nil {
			internalError(w, err)
			return
		}
	}
	notes, err := s.collectAdminNoteStats(ctx)
	if err != nil {
		internalError(w, err)
		return
	}
	var noteCount, annotatedCount, strokeCount int64
	var noteBytes int64
	for _, stat := range notes {
		noteCount += int64(stat.notes)
		annotatedCount += int64(stat.annotatedNotes)
		strokeCount += int64(stat.strokes)
		noteBytes += stat.bytes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": users, "devices": devices, "active_devices_24h": activeDevices,
		"notes": noteCount, "annotated_notes": annotatedCount, "strokes": strokeCount,
		"note_storage_bytes": noteBytes, "experiences": experiences,
		"feedback": feedback, "feedback_new": feedbackNew, "study_time_ms": studyMillis,
		"generated_at": time.Now().UnixMilli(),
	})
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	} else if offset > 10_000_000 {
		offset = 10_000_000
	}
	var total int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		internalError(w, err)
		return
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id,u.nickname,COALESCE(u.email,''),u.email_verified,u.created_at,
		(SELECT COUNT(*) FROM devices d WHERE d.user_id=u.id),
		(SELECT COALESCE(MAX(last_seen),0) FROM devices d WHERE d.user_id=u.id),
		COALESCE((SELECT total_ms FROM study_time st WHERE st.user_id=u.id),0)
		FROM users u ORDER BY u.created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	type adminUser struct {
		ID             string `json:"id"`
		Nickname       string `json:"nickname"`
		Email          string `json:"email"`
		EmailVerified  bool   `json:"email_verified"`
		CreatedAt      int64  `json:"created_at"`
		Devices        int    `json:"devices"`
		LastSeen       int64  `json:"last_seen"`
		Notes          int    `json:"notes"`
		AnnotatedNotes int    `json:"annotated_notes"`
		Strokes        int    `json:"strokes"`
		NoteBytes      int64  `json:"note_bytes"`
		StudyTimeMS    int64  `json:"study_time_ms"`
	}
	items := make([]adminUser, 0, limit)
	for rows.Next() {
		var item adminUser
		if err := rows.Scan(&item.ID, &item.Nickname, &item.Email, &item.EmailVerified, &item.CreatedAt, &item.Devices, &item.LastSeen, &item.StudyTimeMS); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		internalError(w, err)
		return
	}
	rows.Close()
	noteStats, err := s.collectAdminNoteStats(ctx)
	if err != nil {
		internalError(w, err)
		return
	}
	for i := range items {
		if stat, ok := noteStats[items[i].ID]; ok {
			items[i].Notes = stat.notes
			items[i].AnnotatedNotes = stat.annotatedNotes
			items[i].Strokes = stat.strokes
			items[i].NoteBytes = stat.bytes
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "limit": limit, "offset": offset, "items": items})
}

func (s *Server) updateAdminUser(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
	const suffix = "/study-time"
	if !strings.HasSuffix(path, suffix) {
		http.NotFound(w, r)
		return
	}
	userID := strings.TrimSuffix(path, suffix)
	if userID == "" || strings.Contains(userID, "/") {
		writeError(w, 400, "用户编号无效")
		return
	}
	var req struct {
		TotalMS int64 `json:"total_ms"`
	}
	if !decodeJSON(w, r, &req, 4096) {
		return
	}
	if req.TotalMS < 0 || req.TotalMS > 1000*365*24*60*60*1000 {
		writeError(w, 400, "学习时长数值无效")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO study_time(user_id,total_ms,updated_at) VALUES(?,?,?) ON DUPLICATE KEY UPDATE total_ms=VALUES(total_ms),updated_at=VALUES(updated_at)`, userID, req.TotalMS, time.Now().UnixMilli())
	if err != nil {
		internalError(w, err)
		return
	}
	_ = res
	writeJSON(w, 200, map[string]any{"ok": true, "total_ms": req.TotalMS})
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
	encrypted, encErr := encryptRecoveryCode(s.Cfg.AppSecret, code)
	if encErr != nil {
		internalError(w, encErr)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET recovery_hash=?,recovery_code_ciphertext=? WHERE id=?`, hash(code), encrypted, userID); err != nil {
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
	} else if path == "admin" || path == "admin/" {
		path = "admin.html"
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
	if strings.HasSuffix(path, ".webmanifest") {
		typ = "application/manifest+json; charset=utf-8"
	}
	if typ == "" {
		if strings.HasSuffix(path, ".js") {
			typ = "text/javascript; charset=utf-8"
		} else if strings.HasSuffix(path, ".webmanifest") {
			typ = "application/manifest+json; charset=utf-8"
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
