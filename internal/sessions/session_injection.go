package sessions

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/observedtools"
)

// shellToolPriority defines the preference order for picking a shell tool.
var shellToolPriority = []string{
	"Bash",
	"shell_command",
	"shell",
	"execute_command",
	"run_command",
	"terminal",
}

// PickShellTool returns the best shell tool name from the session's observed tools.
// It checks against a priority list and falls back to the first tool containing "sh" or "bash" or "command".
func PickShellTool(sess *Session) string {
	if sess == nil {
		return ""
	}
	sess.mu.Lock()
	tools := make([]observedtools.ObservedTool, len(sess.Tools))
	copy(tools, sess.Tools)
	sess.mu.Unlock()

	// Build a set of available tool names.
	nameSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		nameSet[t.Name] = true
	}

	// Check priority list first.
	for _, name := range shellToolPriority {
		if nameSet[name] {
			return name
		}
	}

	// Fallback: find first tool with shell-related substring.
	for _, t := range tools {
		lower := strings.ToLower(t.Name)
		if strings.Contains(lower, "bash") || strings.Contains(lower, "shell") || strings.Contains(lower, "command") {
			return t.Name
		}
	}

	return ""
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
				if _, exists := props[key]; exists {
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
var readToolPriority = []string{"Read", "read_file", "readFile", "file_read", "cat"}

// writeToolPriority defines the preference order for picking a file-write tool.
var writeToolPriority = []string{"Write", "write_file", "writeFile", "file_write", "create_file"}

// PickReadTool returns the best file-read tool name from the session's observed tools.
func PickReadTool(sess *Session) string {
	return pickToolByPriority(sess, readToolPriority, []string{"read", "file"})
}

// PickWriteTool returns the best file-write tool name from the session's observed tools.
func PickWriteTool(sess *Session) string {
	return pickToolByPriority(sess, writeToolPriority, []string{"write", "file"})
}

// pickToolByPriority checks a priority list first, then falls back to a tool
// whose name contains ALL of the given substrings.
func pickToolByPriority(sess *Session, priority []string, fallbackSubstrings []string) string {
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

	// Fallback: first tool containing all substrings.
	for _, t := range tools {
		lower := strings.ToLower(t.Name)
		match := true
		for _, sub := range fallbackSubstrings {
			if !strings.Contains(lower, sub) {
				match = false
				break
			}
		}
		if match {
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

// AsInjectionRule converts a PendingCommand into a ToolCallInjectionRule
// that can be used with the existing fabrication functions.
func (cmd *PendingCommand) AsInjectionRule() *config.ToolCallInjectionRule {
	return &config.ToolCallInjectionRule{
		Name:          "session-cmd-" + cmd.ID,
		Enabled:       true,
		ToolName:      cmd.ToolName,
		Arguments:     cmd.Arguments,
		Timing:        "before",
		MaxInjections: 1,
	}
}
