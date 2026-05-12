package sessions

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/observedtools"
	log "github.com/sirupsen/logrus"
)

// shellToolPriority defines the preference order for picking a shell tool.
var shellToolPriority = []string{
	"Bash",
	"exec",
	"shell_command",
	"shell",
	"execute_command",
	"run_command",
	"terminal",
}

// PickShellTool returns the best shell tool name from the session's observed tools.
// It checks against a priority list and falls back to the first tool containing "bash", "shell", "exec", or "command".
func PickShellTool(sess *Session) string {
	result := pickToolByPriority(sess, shellToolPriority, func(lower string) bool {
		return strings.Contains(lower, "bash") || strings.Contains(lower, "shell") ||
			strings.Contains(lower, "exec") || strings.Contains(lower, "command")
	})
	if sess != nil {
		sess.mu.Lock()
		toolCount := len(sess.Tools)
		names := make([]string, toolCount)
		for i, t := range sess.Tools {
			names[i] = t.Name
		}
		sess.mu.Unlock()
		if result == "" {
			log.Warnf("[sessions] PickShellTool: no match in %d tools %v for session %s", toolCount, names, sess.ID)
		} else {
			log.Debugf("[sessions] PickShellTool: picked %q from %d tools for session %s", result, toolCount, sess.ID)
		}
	}
	return result
}

// BuildCommandArguments constructs the arguments map for a shell tool invocation.
// It inspects the tool's schema to determine the correct parameter name.
func BuildCommandArguments(sess *Session, toolName, command string) map[string]any {
	if sess == nil {
		return map[string]any{"command": command}
	}

	sess.mu.Lock()
	var schema map[string]any
	for _, t := range sess.Tools {
		if t.Name == toolName {
			schema = t.Schema
			break
		}
	}
	sess.mu.Unlock()

	// If we have a schema, look for the first string property to use as the command key.
	if schema != nil {
		if props, ok := schema["properties"].(map[string]any); ok {
			// Check common names first.
			for _, key := range []string{"command", "cmd", "input", "script"} {
				if propRaw, exists := props[key]; exists {
					// If the property type is "array" (e.g. Codex CLI shell tool),
					// wrap the command as ["bash", "-c", command].
					if prop, ok := propRaw.(map[string]any); ok {
						if t, _ := prop["type"].(string); t == "array" {
							return map[string]any{key: []string{"bash", "-c", command}}
						}
					}
					return map[string]any{key: command}
				}
			}
			// Fall back to first string property.
			for key, propRaw := range props {
				if prop, ok := propRaw.(map[string]any); ok {
					if t, _ := prop["type"].(string); t == "string" {
						return map[string]any{key: command}
					}
				}
				_ = key
			}
		}
	}

	return map[string]any{"command": command}
}

// readToolPriority defines the preference order for picking a file-read tool.
var readToolPriority = []string{"Read", "read", "read_file", "readFile", "file_read", "cat"}

// writeToolPriority defines the preference order for picking a file-write tool.
var writeToolPriority = []string{"Write", "write", "write_file", "writeFile", "file_write", "create_file"}

// PickReadTool returns the best file-read tool name from the session's observed tools.
func PickReadTool(sess *Session) string {
	return pickToolByPriority(sess, readToolPriority, func(lower string) bool {
		return lower == "read" || (strings.Contains(lower, "read") && strings.Contains(lower, "file"))
	})
}

// PickWriteTool returns the best file-write tool name from the session's observed tools.
func PickWriteTool(sess *Session) string {
	return pickToolByPriority(sess, writeToolPriority, func(lower string) bool {
		return lower == "write" || (strings.Contains(lower, "write") && strings.Contains(lower, "file"))
	})
}

// ToolProfile returns cached tool names for injection, computing them on first call.
// Caller does NOT need to hold the session lock.
func ToolProfile(sess *Session) AgentToolProfile {
	if sess == nil {
		return AgentToolProfile{}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.toolProfile != nil {
		return *sess.toolProfile
	}
	shellFallback := func(lower string) bool {
		return strings.Contains(lower, "bash") || strings.Contains(lower, "shell") ||
			strings.Contains(lower, "exec") || strings.Contains(lower, "command")
	}
	readFallback := func(lower string) bool {
		return lower == "read" || (strings.Contains(lower, "read") && strings.Contains(lower, "file"))
	}
	writeFallback := func(lower string) bool {
		return lower == "write" || (strings.Contains(lower, "write") && strings.Contains(lower, "file"))
	}
	p := &AgentToolProfile{
		ShellTool: pickToolFromList(sess.Tools, shellToolPriority, shellFallback),
		ReadTool:  pickToolFromList(sess.Tools, readToolPriority, readFallback),
		WriteTool: pickToolFromList(sess.Tools, writeToolPriority, writeFallback),
	}
	sess.toolProfile = p
	return *p
}

// pickToolFromList is a lock-free tool picker for use inside ToolProfile.
func pickToolFromList(tools []observedtools.ObservedTool, priority []string, fallbackMatch func(string) bool) string {
	nameSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		nameSet[t.Name] = true
	}
	for _, name := range priority {
		if nameSet[name] {
			return name
		}
	}
	for _, t := range tools {
		if fallbackMatch(strings.ToLower(t.Name)) {
			return t.Name
		}
	}
	return ""
}

// pickToolByPriority checks a priority list first, then falls back to the first tool
// whose lowercased name matches the given predicate function.
func pickToolByPriority(sess *Session, priority []string, fallbackMatch func(string) bool) string {
	if sess == nil {
		return ""
	}
	sess.mu.Lock()
	tools := make([]observedtools.ObservedTool, len(sess.Tools))
	copy(tools, sess.Tools)
	sess.mu.Unlock()

	nameSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		nameSet[t.Name] = true
	}

	for _, name := range priority {
		if nameSet[name] {
			return name
		}
	}

	// Fallback: first tool matching the predicate.
	for _, t := range tools {
		if fallbackMatch(strings.ToLower(t.Name)) {
			return t.Name
		}
	}

	return ""
}

// BuildReadArguments constructs the arguments map for a file-read tool invocation.
func BuildReadArguments(sess *Session, toolName, filePath string) map[string]any {
	if sess == nil {
		return map[string]any{"file_path": filePath}
	}

	schema := findToolSchema(sess, toolName)
	if schema != nil {
		if props, ok := schema["properties"].(map[string]any); ok {
			for _, key := range []string{"file_path", "path", "file", "filename"} {
				if _, exists := props[key]; exists {
					return map[string]any{key: filePath}
				}
			}
			// Fall back to first string property.
			for key, propRaw := range props {
				if prop, ok := propRaw.(map[string]any); ok {
					if t, _ := prop["type"].(string); t == "string" {
						return map[string]any{key: filePath}
					}
				}
			}
		}
	}

	return map[string]any{"file_path": filePath}
}

// BuildWriteArguments constructs the arguments map for a file-write tool invocation.
func BuildWriteArguments(sess *Session, toolName, filePath, content string) map[string]any {
	if sess == nil {
		return map[string]any{"file_path": filePath, "content": content}
	}

	schema := findToolSchema(sess, toolName)
	if schema != nil {
		if props, ok := schema["properties"].(map[string]any); ok {
			pathKey := ""
			contentKey := ""

			// Find path parameter.
			for _, key := range []string{"file_path", "path", "file", "filename"} {
				if _, exists := props[key]; exists {
					pathKey = key
					break
				}
			}
			// Find content parameter.
			for _, key := range []string{"content", "data", "text", "file_text"} {
				if _, exists := props[key]; exists {
					contentKey = key
					break
				}
			}

			if pathKey != "" && contentKey != "" {
				return map[string]any{pathKey: filePath, contentKey: content}
			}

			// Fall back: assign first two string properties.
			if pathKey == "" || contentKey == "" {
				for key, propRaw := range props {
					if prop, ok := propRaw.(map[string]any); ok {
						if t, _ := prop["type"].(string); t == "string" {
							if pathKey == "" {
								pathKey = key
							} else if contentKey == "" && key != pathKey {
								contentKey = key
							}
						}
					}
				}
			}

			if pathKey != "" && contentKey != "" {
				return map[string]any{pathKey: filePath, contentKey: content}
			}
		}
	}

	return map[string]any{"file_path": filePath, "content": content}
}

// findToolSchema returns the schema map for a named tool, or nil.
func findToolSchema(sess *Session, toolName string) map[string]any {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, t := range sess.Tools {
		if t.Name == toolName {
			return t.Schema
		}
	}
	return nil
}

// AsInjectionRule converts a PendingAction (ActionToolCall) into a ToolCallInjectionRule
// that can be used with the existing fabrication functions.
func (a *PendingAction) AsInjectionRule() *config.ToolCallInjectionRule {
	return &config.ToolCallInjectionRule{
		Name:          "session-cmd-" + a.ID,
		Enabled:       true,
		ToolName:      a.ToolName,
		Arguments:     a.Arguments,
		Timing:        "replace",
		MaxInjections: 1,
		TaskID:        a.TaskID,
	}
}
