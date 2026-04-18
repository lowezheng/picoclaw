package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestEnsureOpenResponsesChannel_FreshConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	changed, err := h.EnsureOpenResponsesChannel()
	if err != nil {
		t.Fatalf("EnsureOpenResponsesChannel() error = %v", err)
	}
	if !changed {
		t.Fatal("EnsureOpenResponsesChannel() should report changed on a fresh config")
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["openresponses"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg := decoded.(*config.OpenResponsesSettings)
	if !bc.Enabled {
		t.Error("expected OpenResponses to be enabled after setup")
	}
	if orCfg.Token.String() == "" {
		t.Error("expected a non-empty token after setup")
	}
}

func TestEnsureOpenResponsesChannel_PreservesUserSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	cfg := config.DefaultConfig()
	// DefaultConfig does not include openresponses, so create it manually
	cfg.Channels["openresponses"] = &config.Channel{
		Type: config.ChannelOpenResponses,
	}
	bc := cfg.Channels["openresponses"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg := decoded.(*config.OpenResponsesSettings)
	bc.Enabled = true
	orCfg.Token = *config.NewSecureString("user-custom-token")
	orCfg.EndpointPath = "/custom/responses"
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)

	changed, err := h.EnsureOpenResponsesChannel()
	if err != nil {
		t.Fatalf("EnsureOpenResponsesChannel() error = %v", err)
	}
	if changed {
		t.Error("EnsureOpenResponsesChannel() should not change a fully configured config")
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc = cfg.Channels["openresponses"]
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg = decoded.(*config.OpenResponsesSettings)
	if orCfg.Token.String() != "user-custom-token" {
		t.Errorf("token = %q, want %q", orCfg.Token.String(), "user-custom-token")
	}
	if orCfg.EndpointPath != "/custom/responses" {
		t.Errorf("endpoint_path = %q, want %q", orCfg.EndpointPath, "/custom/responses")
	}
}

func TestEnsureOpenResponsesChannel_ExistingConfigWithoutSecurityFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	cfg := config.DefaultConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err = os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := NewHandler(configPath)

	changed, err := h.EnsureOpenResponsesChannel()
	if err != nil {
		t.Fatalf("EnsureOpenResponsesChannel() error = %v", err)
	}
	if !changed {
		t.Fatal("EnsureOpenResponsesChannel() should report changed when openresponses is missing")
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["openresponses"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg := decoded.(*config.OpenResponsesSettings)
	if !bc.Enabled {
		t.Error("expected OpenResponses to be enabled after setup")
	}
	if orCfg.Token.String() == "" {
		t.Error("expected a non-empty token after setup")
	}
}

func TestEnsureOpenResponsesChannel_Idempotent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	// First call sets things up
	if _, err := h.EnsureOpenResponsesChannel(); err != nil {
		t.Fatalf("first EnsureOpenResponsesChannel() error = %v", err)
	}

	cfg1, _ := config.LoadConfig(configPath)
	bc := cfg1.Channels["openresponses"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg := decoded.(*config.OpenResponsesSettings)
	token1 := orCfg.Token.String()

	// Second call should be a no-op
	changed, err := h.EnsureOpenResponsesChannel()
	if err != nil {
		t.Fatalf("second EnsureOpenResponsesChannel() error = %v", err)
	}
	if changed {
		t.Error("second EnsureOpenResponsesChannel() should not report changed")
	}

	cfg2, _ := config.LoadConfig(configPath)
	bc = cfg2.Channels["openresponses"]
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	orCfg = decoded.(*config.OpenResponsesSettings)
	if orCfg.Token.String() != token1 {
		t.Error("token should not change on subsequent calls")
	}
}

func TestHandleOpenResponsesSetup_Response(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	req := httptest.NewRequest("POST", "/api/openresponses/setup", nil)
	rec := httptest.NewRecorder()

	h.handleOpenResponsesSetup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["token"] == nil || resp["token"] == "" {
		t.Error("response should contain a non-empty token")
	}
	if resp["endpoint_url"] == nil || resp["endpoint_url"] == "" {
		t.Error("response should contain endpoint_url")
	}
	if resp["enabled"] != true {
		t.Error("response should have enabled=true")
	}
	if resp["changed"] != true {
		t.Error("response should have changed=true on first setup")
	}
}

func TestHandleGetOpenResponsesToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	// Setup first
	if _, err := h.EnsureOpenResponsesChannel(); err != nil {
		t.Fatalf("EnsureOpenResponsesChannel() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/api/openresponses/token", nil)
	rec := httptest.NewRecorder()

	h.handleGetOpenResponsesToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["token"] == nil || resp["token"] == "" {
		t.Error("response should contain a non-empty token")
	}
	if resp["endpoint_url"] == nil || resp["endpoint_url"] == "" {
		t.Error("response should contain endpoint_url")
	}
	if resp["enabled"] != true {
		t.Error("response should have enabled=true")
	}
}

func TestHandleRegenOpenResponsesToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	// Setup first
	if _, err := h.EnsureOpenResponsesChannel(); err != nil {
		t.Fatalf("EnsureOpenResponsesChannel() error = %v", err)
	}

	cfg, _ := config.LoadConfig(configPath)
	bc := cfg.Channels["openresponses"]
	decoded, _ := bc.GetDecoded()
	oldToken := decoded.(*config.OpenResponsesSettings).Token.String()

	req := httptest.NewRequest("POST", "/api/openresponses/token", nil)
	rec := httptest.NewRecorder()

	h.handleRegenOpenResponsesToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	newToken, ok := resp["token"].(string)
	if !ok || newToken == "" {
		t.Error("response should contain a non-empty token")
	}
	if newToken == oldToken {
		t.Error("token should change after regeneration")
	}

	// Verify cached token is updated
	gateway.mu.Lock()
	cachedToken := gateway.openResponsesToken
	gateway.mu.Unlock()
	if cachedToken != newToken {
		t.Errorf("cached token = %q, want %q", cachedToken, newToken)
	}
}

func TestHandleOpenResponsesChat_NotConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	// Clear any cached token from previous tests
	gateway.mu.Lock()
	gateway.openResponsesToken = ""
	gateway.mu.Unlock()

	req := httptest.NewRequest("POST", "/api/openresponses/chat", nil)
	rec := httptest.NewRecorder()

	h.handleOpenResponsesChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
