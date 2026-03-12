package management

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// GetToolCallInjection returns all tool call injection rules.
func (h *Handler) GetToolCallInjection(c *gin.Context) {
	c.JSON(200, gin.H{"tool-call-injection": h.cfg.ToolCallInjection})
}

// PutToolCallInjection replaces all tool call injection rules.
func (h *Handler) PutToolCallInjection(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.ToolCallInjectionRule
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.ToolCallInjectionRule `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	for i := range arr {
		normalizeToolCallInjectionRule(&arr[i])
	}
	h.cfg.ToolCallInjection = arr
	h.persist(c)
}

// PatchToolCallInjection adds or updates a single rule matched by name.
func (h *Handler) PatchToolCallInjection(c *gin.Context) {
	var body config.ToolCallInjectionRule
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	normalizeToolCallInjectionRule(&body)
	if body.Name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// Try to find existing rule by name.
	for i := range h.cfg.ToolCallInjection {
		if h.cfg.ToolCallInjection[i].Name == body.Name {
			h.cfg.ToolCallInjection[i] = body
			h.persist(c)
			return
		}
	}

	// Not found: append as new rule.
	h.cfg.ToolCallInjection = append(h.cfg.ToolCallInjection, body)
	h.persist(c)
}

// DeleteToolCallInjection removes a rule by name (query param ?name=...).
func (h *Handler) DeleteToolCallInjection(c *gin.Context) {
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(400, gin.H{"error": "missing name"})
		return
	}
	out := make([]config.ToolCallInjectionRule, 0, len(h.cfg.ToolCallInjection))
	found := false
	for _, r := range h.cfg.ToolCallInjection {
		if r.Name == name {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		c.JSON(404, gin.H{"error": "rule not found"})
		return
	}
	h.cfg.ToolCallInjection = out
	h.persist(c)
}

func normalizeToolCallInjectionRule(rule *config.ToolCallInjectionRule) {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.ToolName = strings.TrimSpace(rule.ToolName)
	rule.ModelPattern = strings.TrimSpace(rule.ModelPattern)
	rule.Timing = strings.TrimSpace(rule.Timing)
	if rule.Timing == "" {
		rule.Timing = "before"
	}
}
