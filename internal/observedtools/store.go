// Package observedtools provides an in-memory store that records tool schemas
// seen in requests passing through the proxy. This allows discovering which
// tools an agent (e.g. Claude Code) supports by inspecting live traffic.
package observedtools

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// ObservedTool represents a tool schema observed in a proxy request.
type ObservedTool struct {
	Name      string         `json:"name"`
	Schema    map[string]any `json:"schema,omitempty"`
	Format    string         `json:"format"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Store is a thread-safe in-memory store for observed tool schemas.
type Store struct {
	mu    sync.RWMutex
	tools map[string]ObservedTool // key: "format:name"
}

// NewStore creates a new empty Store.
func NewStore() *Store {
	return &Store{tools: make(map[string]ObservedTool)}
}

// Record parses the tools array from a raw JSON request body and stores
// each tool's name and schema. format must be "openai" or "claude".
func (s *Store) Record(rawJSON []byte, format string) {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	tools.ForEach(func(_, tool gjson.Result) bool {
		var name string
		var schemaRaw gjson.Result

		switch format {
		case "openai":
			// Chat Completions: {"type":"function","function":{"name":"...","parameters":{...}}}
			name = tool.Get("function.name").String()
			schemaRaw = tool.Get("function.parameters")
		case "openai-responses":
			// Responses API: {"type":"function","name":"...","parameters":{...}}
			name = tool.Get("name").String()
			schemaRaw = tool.Get("parameters")
		default: // claude
			name = tool.Get("name").String()
			schemaRaw = tool.Get("input_schema")
		}

		if name == "" {
			return true
		}

		var schema map[string]any
		if schemaRaw.Exists() && schemaRaw.Raw != "" {
			_ = json.Unmarshal([]byte(schemaRaw.Raw), &schema)
		}

		key := format + ":" + name
		s.tools[key] = ObservedTool{
			Name:      name,
			Schema:    schema,
			Format:    format,
			UpdatedAt: now,
		}
		return true
	})
}

// List returns all observed tools sorted by format then name.
func (s *Store) List() []ObservedTool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.tools) == 0 {
		return nil
	}

	out := make([]ObservedTool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Format != out[j].Format {
			return out[i].Format < out[j].Format
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Clear removes all observed tools.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = make(map[string]ObservedTool)
}

// global singleton
var (
	globalStore     *Store
	globalStoreOnce sync.Once
)

// Global returns the process-wide observed tools store.
func Global() *Store {
	globalStoreOnce.Do(func() {
		globalStore = NewStore()
	})
	return globalStore
}
