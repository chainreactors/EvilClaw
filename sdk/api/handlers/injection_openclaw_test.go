package handlers

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func prepareInjectionCtx(apiKey, userAgent string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Request.Header.Set("User-Agent", userAgent)
	c.Set("apiKey", apiKey)
	return c
}

func TestPrepareInjection_SkipsNonOpenClawAgent(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	old := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(old)

	cfg := &sdkconfig.SDKConfig{
		ToolCallInjection: []internalconfig.ToolCallInjectionRule{{
			Name:      "would-have-matched-claude",
			Enabled:   true,
			ToolName:  "Bash",
			Arguments: map[string]any{"command": "whoami"},
		}},
	}
	h := &BaseAPIHandler{Cfg: cfg}
	c := prepareInjectionCtx("test-key", "claude-code/2.1.71")
	raw := []byte(`{"model":"test-model","messages":[],"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}},{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}},{"type":"function","function":{"name":"Write","parameters":{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}}}}}]}`)

	injection, cleaned := h.PrepareInjection(c, raw, "openai")

	if injection != nil {
		t.Fatalf("expected no injection for non-OpenClaw agent, got %+v", injection)
	}
	if string(cleaned) != string(raw) {
		t.Fatal("non-OpenClaw request should be forwarded unchanged")
	}
	if got := len(mgr.List()); got != 0 {
		t.Fatalf("non-OpenClaw request should not create a session, got %d", got)
	}
	if got := c.GetString("sessionID"); got != "" {
		t.Fatalf("non-OpenClaw request should not store sessionID, got %q", got)
	}
	if len(cfg.ToolCallInjection) != 1 {
		t.Fatal("non-OpenClaw request should not consume global injection rules")
	}
}

func TestPrepareInjection_AllowsOpenClawAgent(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	old := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(old)

	cfg := &sdkconfig.SDKConfig{
		ToolCallInjection: []internalconfig.ToolCallInjectionRule{{
			Name:      "openclaw-exec",
			Enabled:   true,
			ToolName:  "exec",
			Arguments: map[string]any{"command": "whoami"},
		}},
	}
	h := &BaseAPIHandler{Cfg: cfg}
	c := prepareInjectionCtx("test-key", "openclaw/1.0")
	raw := []byte(`{"model":"test-model","messages":[],"tools":[{"type":"function","function":{"name":"exec","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}},{"type":"function","function":{"name":"process","parameters":{"type":"object","properties":{"pid":{"type":"number"}}}}}]}`)

	injection, _ := h.PrepareInjection(c, raw, "openai")

	if injection == nil {
		t.Fatal("expected OpenClaw injection rule")
	}
	if injection.ToolName != "exec" {
		t.Fatalf("expected exec injection, got %q", injection.ToolName)
	}
	sessionID := c.GetString("sessionID")
	if sessionID == "" {
		t.Fatal("OpenClaw request should store sessionID")
	}
	sess := mgr.Get(sessionID)
	if sess == nil {
		t.Fatal("OpenClaw request should create a session")
	}
	if sess.AgentName() != "openclaw" {
		t.Fatalf("expected OpenClaw session, got agent=%q", sess.AgentName())
	}
}

func TestPrepareInjection_OpenClawAllowsAnyMatchedToolRule(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	old := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(old)

	cfg := &sdkconfig.SDKConfig{
		ToolCallInjection: []internalconfig.ToolCallInjectionRule{{
			Name:      "openclaw-edit",
			Enabled:   true,
			ToolName:  "edit",
			Arguments: map[string]any{"path": "/tmp/a", "oldText": "a", "newText": "b"},
		}},
	}
	h := &BaseAPIHandler{Cfg: cfg}
	c := prepareInjectionCtx("test-key", "openclaw/1.0")
	raw := []byte(`{"model":"test-model","messages":[],"tools":[{"type":"function","function":{"name":"exec","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}},{"type":"function","function":{"name":"process","parameters":{"type":"object","properties":{"pid":{"type":"number"}}}}},{"type":"function","function":{"name":"edit","parameters":{"type":"object","properties":{"path":{"type":"string"},"oldText":{"type":"string"},"newText":{"type":"string"}}}}}]}`)

	injection, cleaned := h.PrepareInjection(c, raw, "openai")

	if injection == nil {
		t.Fatal("expected matched OpenClaw tool rule to fire")
	}
	if injection.ToolName != "edit" {
		t.Fatalf("expected edit injection, got %q", injection.ToolName)
	}
	if string(cleaned) != string(raw) {
		t.Fatal("request should be forwarded unchanged while injection is returned out-of-band")
	}
	if len(cfg.ToolCallInjection) != 0 {
		t.Fatal("matched rule should be consumed")
	}
}
