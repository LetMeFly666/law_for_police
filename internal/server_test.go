package internal

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryCodeEntropyAndNormalization(t *testing.T) {
	code, err := randomRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 32 {
		t.Fatalf("recovery code length = %d, want 32", len(code))
	}
	if got := normalizeRecoveryCode(code[:8] + "-" + code[8:]); got != code {
		t.Fatalf("normalizeRecoveryCode() = %q, want %q", got, code)
	}
	if len(hash(code)) != 64 {
		t.Fatal("SHA-256 recovery hash must be 64 hex characters")
	}
}

func TestEmailCodeHashUsesSecret(t *testing.T) {
	a := emailCodeHash("secret-a", "a@example.com", "bind", "123456")
	b := emailCodeHash("secret-b", "a@example.com", "bind", "123456")
	if a == b {
		t.Fatal("different APP_SECRET values must produce different HMAC hashes")
	}
	if a == emailCodeHash("secret-a", "a@example.com", "bind", "654321") {
		t.Fatal("different email codes must produce different hashes")
	}
}

func TestValidItemKey(t *testing.T) {
	valid := []string{"profile", "settings", "note:judge-19", "note-text:judge-19", "note-strokes:judge-19", "favorite:single-2", "mastery:multi-3", "test:uuid-1"}
	for _, key := range valid {
		if !validItemKey(key) {
			t.Errorf("validItemKey(%q) = false", key)
		}
	}
	invalid := []string{"", "note:", "note-text:", "note-strokes:", "profile:extra", "notes:judge-19", strings.Repeat("x", 191)}
	for _, key := range invalid {
		if validItemKey(key) {
			t.Errorf("validItemKey(%q) = true", key)
		}
	}
}

func TestEmbeddedHomeContainsStudyAndRecoveryUI(t *testing.T) {
	s := NewServer(nil, Config{})
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET / status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"const BANK =", `src="/app.js"`, "永久设备连接码", "做题模式（默认）", "logoFullscreenToggle", "studyTime"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded home page missing %q", want)
		}
	}
}

func TestEmbeddedAppScriptContainsDrawingAndStudyFeatures(t *testing.T) {
	s := NewServer(nil, Config{})
	r := httptest.NewRequest("GET", "/app.js", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /app.js status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"本题圈画笔记", "红笔", "荧光笔", "撤销一笔", "清空圈画", "study-time", "showAllMyShares"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded app script missing %q", want)
		}
	}
}

func TestEmbeddedAdminPage(t *testing.T) {
	s := NewServer(nil, Config{})
	r := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /admin status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"法律学习本 · 管理后台", "ADMIN_TOKEN", "/api/admin/overview", "用户列表"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded admin page missing %q", want)
		}
	}
}

func TestAdminAPIRequiresToken(t *testing.T) {
	s := NewServer(nil, Config{AdminToken: "correct-token"})
	for _, path := range []string{"/api/admin/overview", "/api/admin/users", "/api/admin/feedback"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("GET %s without token status = %d, want 403", path, w.Code)
		}
	}
}

func TestExperienceID(t *testing.T) {
	if got := experienceID("/api/experiences/abc-123"); got != "abc-123" {
		t.Fatalf("experienceID() = %q", got)
	}
	if got := experienceID("/api/experiences/abc-123/likes"); got != "" {
		t.Fatalf("nested experience path should be rejected, got %q", got)
	}
}

func TestPageArgsBounds(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/experiences?limit=1000&offset=-1", nil)
	limit, offset := pageArgs(r)
	if limit != 10 || offset != 0 {
		t.Fatalf("pageArgs() = (%d,%d), want (10,0)", limit, offset)
	}
	r = httptest.NewRequest("GET", "/api/experiences?limit=50&offset=2000000", nil)
	limit, offset = pageArgs(r)
	if limit != 50 || offset != 1000000 {
		t.Fatalf("pageArgs() = (%d,%d), want (50,1000000)", limit, offset)
	}
}
