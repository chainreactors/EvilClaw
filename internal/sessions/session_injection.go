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
