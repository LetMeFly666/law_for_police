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
	valid := []string{"profile", "settings", "note:judge-19", "favorite:single-2", "mastery:multi-3", "test:uuid-1"}
	for _, key := range valid {
		if !validItemKey(key) {
			t.Errorf("validItemKey(%q) = false", key)
		}
	}
	invalid := []string{"", "note:", "profile:extra", "notes:judge-19", strings.Repeat("x", 191)}
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
	for _, want := range []string{"const BANK =", "圈画批注", "永久设备连接码", "已为本设备创建学习账号"} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded home page missing %q", want)
		}
	}
}
