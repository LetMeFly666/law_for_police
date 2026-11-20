package internal

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareAppVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.0.0", "1.0.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"1.2.0", "1.1.9", 1},
		{"2.0.0", "1.99.99", 1},
	}
	for _, tc := range cases {
		if got := compareAppVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareAppVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAppUpdatesAfterVersion(t *testing.T) {
	original := appUpdateReleases
	appUpdateReleases = []appUpdateRelease{
		{Version: "1.0.0", Title: "first", Items: []string{"first item"}},
		{Version: "1.0.1", Title: "second", Items: []string{"second item"}},
	}
	defer func() { appUpdateReleases = original }()
	updates := appUpdatesAfter("0.0.0")
	if len(updates) != 2 || updates[0].Version != "1.0.0" || updates[1].Version != "1.0.1" {
		t.Fatalf("updates after 0.0.0 = %#v, want both releases", updates)
	}
	updates = appUpdatesAfter("1.0.0")
	if len(updates) != 1 || updates[0].Version != "1.0.1" {
		t.Fatalf("updates after 1.0.0 = %#v, want only 1.0.1", updates)
	}
	if got := len(appUpdatesAfter("1.0.1")); got != 0 {
		t.Fatalf("updates after latest version = %d, want 0", got)
	}
	show, markRead, pending := appUpdateClaim("0.0.0", false)
	if show || markRead || len(pending) != 0 {
		t.Fatalf("disabled update claim = (%v, %v, %#v), want no popup and no read marker", show, markRead, pending)
	}
	show, markRead, pending = appUpdateClaim("0.0.0", true)
	if !show || !markRead || len(pending) != 2 {
		t.Fatalf("enabled update claim = (%v, %v, %#v), want both unseen releases", show, markRead, pending)
	}
}

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
	for _, want := range []string{"const BANK =", `src="/app.js"`, "永久设备连接码", "做题模式（默认）", "logoFullscreenToggle", "studyTime", "shuffleOptionsToggle", "showUpdatesToggle", "appUpdateModal"} {
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
	for _, want := range []string{"本题圈画笔记", "画笔", "荧光笔", "撤销一笔", "恢复一笔", "清空本题圈画", "redoStack", "study-time", "loadMoreMyShares", "displayOptions", "optionOrderCache", "app-updates/claim"} {
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
	for _, want := range []string{"法律学习本 · 管理后台", "ADMIN_TOKEN", "/api/admin/overview", "用户列表", "data-recovery-user", "重置连接码"} {
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
	r := httptest.NewRequest("POST", "/api/admin/users/example/recovery-code", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("admin recovery reset without token status = %d, want 403", w.Code)
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

func TestRecoveryCodeEncryptionRoundTripAndAuthentication(t *testing.T) {
	secret := "a-long-test-secret-that-is-not-used-in-production"
	code, err := randomRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := encryptRecoveryCode(secret, code)
	if err != nil {
		t.Fatal(err)
	}
	if ciphertext == code || strings.Contains(ciphertext, code) {
		t.Fatal("recovery code ciphertext must not contain the plaintext code")
	}
	got, err := decryptRecoveryCode(secret, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if got != code {
		t.Fatalf("decrypted code = %q, want %q", got, code)
	}
	if _, err := decryptRecoveryCode("another-secret-that-is-also-long-enough", ciphertext); err == nil {
		t.Fatal("decrypting with a different APP_SECRET must fail")
	}
}

func TestFeedbackNotificationRequiresConfiguredRecipient(t *testing.T) {
	if (Config{SMTPHost: "smtp.example.com", SMTPFrom: "from@example.com"}).FeedbackNotificationEnabled() {
		t.Fatal("feedback email must remain disabled when FEEDBACK_NOTIFICATION_TO is empty")
	}
	if !(Config{SMTPHost: "smtp.example.com", SMTPFrom: "from@example.com", FeedbackNotificationTo: "notify@example.com"}).FeedbackNotificationEnabled() {
		t.Fatal("feedback email should be enabled when SMTP and recipient are configured")
	}
}

func TestEmbeddedAppScriptContainsUpdatedDrawingAndSyncFeatures(t *testing.T) {
	s := NewServer(nil, Config{})
	r := httptest.NewRequest("GET", "/app.js", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /app.js status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"本题圈画笔记", "画笔", "荧光笔", "撤销一笔", "恢复一笔", "redoStack", "refreshRecoveryCode", "textNotePreview"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded app script missing %q", want)
		}
	}
}

func TestEmbeddedPWAAssets(t *testing.T) {
	s := NewServer(nil, Config{})
	for path, contentType := range map[string]string{
		"/manifest.webmanifest":       "application/manifest+json",
		"/service-worker.js":          "javascript",
		"/icons/apple-touch-icon.png": "image/png",
	} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("GET %s status = %d", path, w.Code)
			continue
		}
		if !strings.Contains(w.Header().Get("Content-Type"), contentType) {
			t.Errorf("GET %s Content-Type = %q, want containing %q", path, w.Header().Get("Content-Type"), contentType)
		}
	}
}

func TestRequestClientIPUsesTrustedLocalProxyHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:8787"
	r.Header.Set("X-Real-IP", "203.0.113.8")
	if got := requestClientIP(r); got != "203.0.113.8" {
		t.Fatalf("requestClientIP() = %q, want trusted proxy IP", got)
	}
	r.RemoteAddr = "198.51.100.4:12345"
	r.Header.Set("X-Real-IP", "203.0.113.8")
	if got := requestClientIP(r); got != "198.51.100.4" {
		t.Fatalf("requestClientIP() trusted untrusted forwarded header: %q", got)
	}
}

func TestMasteredQuestionStaysMasteredAfterAnotherCorrectAnswer(t *testing.T) {
	s := NewServer(nil, Config{})
	r := httptest.NewRequest("GET", "/app.js", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{"old.status==='mastered'&&correct?'mastered'", "m.status==='mastered'?'':'<button class=\"btn soft\" id=\"selfMastered\">标记掌握</button>'", "✓ 已达到掌握标准，无需重复标记。"} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js missing mastered-state behavior %q", want)
		}
	}
}
