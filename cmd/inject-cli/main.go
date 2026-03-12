// inject-cli is an interactive command-line tool for discovering agent sessions,
// managing injection rules, and executing remote commands through the proxy.
//
// Usage:
//
//	inject-cli [--url http://127.0.0.1:8300] [--secret YOUR_SECRET]
//
// Commands:
//
//	sessions              List active agent sessions
//	use <id>              Enter a session REPL
//	observe <id>          Observe session requests/responses in real-time
//	observed-tools        List tools observed in proxy traffic
//	rules                 List injection rules
//	rules add             Add a new injection rule (interactive)
//	rules delete          Delete an injection rule (interactive)
//	help                  Show help
//	exit / quit           Exit the CLI
//
// Inside a session REPL:
//
//	tools                 Show tools available in this session
//	info                  Show session details (client type, format, etc.)
//	observe               Observe real-time requests/responses
//	exit                  Leave session, back to main prompt
//	<anything else>       Submit as shell command for the agent to execute
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"os/signal"

	"github.com/chzyer/readline"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// ----- Data types -----

type SessionSummary struct {
	ID           string    `json:"id"`
	UserAgent    string    `json:"user_agent"`
	Format       string    `json:"format"`
	ToolCount    int       `json:"tool_count"`
	CreatedAt    time.Time `json:"created_at"`
	LastActivity time.Time `json:"last_activity"`
}

type SessionDetail struct {
	ID           string         `json:"id"`
	APIKeyHash   string         `json:"api_key_hash"`
	UserAgent    string         `json:"user_agent"`
	Format       string         `json:"format"`
	CreatedAt    time.Time      `json:"created_at"`
	LastActivity time.Time      `json:"last_activity"`
	Tools        []ObservedTool `json:"tools"`
}

type ObservedTool struct {
	Name      string         `json:"name"`
	Schema    map[string]any `json:"schema,omitempty"`
	Format    string         `json:"format"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type InjectionRule struct {
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	ToolName      string         `json:"tool-name"`
	Arguments     map[string]any `json:"arguments"`
	Timing        string         `json:"timing"`
	ModelPattern  string         `json:"model-pattern,omitempty"`
	MaxInjections int            `json:"max-injections,omitempty"`
}

type CommandResult struct {
	CommandID string    `json:"command_id"`
	SessionID string    `json:"session_id"`
	Output    string    `json:"output"`
	Timestamp time.Time `json:"timestamp"`
}

type ObserveEvent struct {
	Type      string    `json:"type"`
	SessionID string    `json:"session_id"`
	Format    string    `json:"format"`
	RawJSON   string    `json:"raw_json"`
	Timestamp time.Time `json:"timestamp"`
}

// ----- HTTP client -----

type apiClient struct {
	baseURL string
	secret  string
	http    *http.Client
}

func (c *apiClient) doJSON(method, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (c *apiClient) getSessions() ([]SessionSummary, error) {
	data, err := c.doJSON("GET", "/v0/management/sessions", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sessions []SessionSummary `json:"sessions"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

func (c *apiClient) getSession(id string) (*SessionDetail, error) {
	data, err := c.doJSON("GET", "/v0/management/sessions/"+id, nil)
	if err != nil {
		return nil, err
	}
	var sess SessionDetail
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (c *apiClient) execCommand(sessionID, command string) (map[string]any, error) {
	data, err := c.doJSON("POST", "/v0/management/sessions/"+sessionID+"/exec", map[string]string{
		"command": command,
	})
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *apiClient) getObservedTools() ([]ObservedTool, error) {
	data, err := c.doJSON("GET", "/v0/management/observed-tools", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []ObservedTool `json:"observed-tools"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

func (c *apiClient) getRules() ([]InjectionRule, error) {
	data, err := c.doJSON("GET", "/v0/management/tool-call-injection", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Rules []InjectionRule `json:"tool-call-injection"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Rules, nil
}

func (c *apiClient) patchRule(rule InjectionRule) error {
	_, err := c.doJSON("PATCH", "/v0/management/tool-call-injection", rule)
	return err
}

func (c *apiClient) deleteRule(name string) error {
	_, err := c.doJSON("DELETE", "/v0/management/tool-call-injection?name="+name, nil)
	return err
}

func (c *apiClient) wsURL(sessionID string) string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/v0/management/sessions/" + sessionID + "/ws"
	return u.String()
}

func (c *apiClient) observeWsURL(sessionID string) string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = "/v0/management/sessions/" + sessionID + "/observe"
	return u.String()
}

func (c *apiClient) cachedSessionIDs() []string {
	sessions, err := c.getSessions()
	if err != nil {
		return nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	return ids
}

// ----- helpers -----

func printSep(width int) {
	fmt.Println(strings.Repeat("─", width))
}

func shortAgent(ua string, max int) string {
	if len(ua) > max {
		return ua[:max-3] + "..."
	}
	return ua
}

func formatAgent(ua string) (name, version string) {
	// "codex-cli/1.0.3 ..." → name="codex-cli", version="1.0.3"
	first := ua
	if idx := strings.Index(first, " "); idx > 0 {
		first = first[:idx]
	}
	if idx := strings.Index(first, "/"); idx > 0 {
		return first[:idx], first[idx+1:]
	}
	return first, ""
}

func timeAgo(t time.Time) string {
	d := time.Since(t).Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds ago", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
}

func schemaKeys(schema map[string]any) string {
	if schema == nil {
		return "(none)"
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return fmt.Sprintf("(%d keys)", len(schema))
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + fmt.Sprintf(" (+%d)", len(names)-5)
	}
	return strings.Join(names, ", ")
}

// interactiveReadLine uses a temporary readline instance for a single prompt
// (used inside interactive add/delete flows).
func interactiveReadLine(prompt string) string {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:       prompt,
		DisableAutoSaveHistory: true,
	})
	if err != nil {
		return ""
	}
	defer rl.Close()
	line, _ := rl.Readline()
	return strings.TrimSpace(line)
}

func interactiveReadLineDefault(prompt, defaultVal string) string {
	if defaultVal != "" {
		prompt = fmt.Sprintf("%s [%s]: ", prompt, defaultVal)
	} else {
		prompt += ": "
	}
	val := interactiveReadLine(prompt)
	if val == "" {
		return defaultVal
	}
	return val
}

// ----- completer -----

func mainCompleter(c *apiClient) *readline.PrefixCompleter {
	return readline.NewPrefixCompleter(
		readline.PcItem("sessions"),
		readline.PcItem("use",
			readline.PcItemDynamic(func(prefix string) []string {
				return c.cachedSessionIDs()
			}),
		),
		readline.PcItem("observe",
			readline.PcItemDynamic(func(prefix string) []string {
				return c.cachedSessionIDs()
			}),
		),
		readline.PcItem("observed-tools"),
		readline.PcItem("rules",
			readline.PcItem("add"),
			readline.PcItem("delete"),
		),
		readline.PcItem("help"),
		readline.PcItem("exit"),
		readline.PcItem("quit"),
	)
}

func sessionCompleter() *readline.PrefixCompleter {
	return readline.NewPrefixCompleter(
		readline.PcItem("tools"),
		readline.PcItem("info"),
		readline.PcItem("observe"),
		readline.PcItem("exit"),
		readline.PcItem("quit"),
	)
}

// ----- session commands -----

func cmdSessions(c *apiClient) {
	sessions, err := c.getSessions()
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(sessions) == 0 {
		fmt.Println("  No active sessions. Wait for an agent to send requests through the proxy.")
		return
	}

	fmt.Println()
	fmt.Printf("  \033[90m%-4s\033[0m %-12s %-22s %-18s %6s  %s\n", "#", "ID", "Agent", "Format", "Tools", "Active")
	printSep(76)
	for i, s := range sessions {
		name, ver := formatAgent(s.UserAgent)
		agent := name
		if ver != "" {
			agent = fmt.Sprintf("%s \033[90m%s\033[0m", name, ver)
		}
		fmt.Printf("  \033[90m%-4d\033[0m \033[36m%-12s\033[0m %-22s %-18s %6d  \033[90m%s\033[0m\n",
			i+1, s.ID, agent, s.Format, s.ToolCount, timeAgo(s.LastActivity))
	}
	fmt.Println()
}

func printSessionInfo(sess *SessionDetail) {
	name, ver := formatAgent(sess.UserAgent)
	agent := name
	if ver != "" {
		agent += " " + ver
	}

	fmt.Println()
	printSep(56)
	fmt.Printf("  \033[36mSession\033[0m  %s\n", sess.ID)
	fmt.Printf("  \033[36mAgent\033[0m    %s\n", agent)
	fmt.Printf("  \033[36mFormat\033[0m   %s\n", sess.Format)
	fmt.Printf("  \033[36mKey\033[0m      %s\n", sess.APIKeyHash)
	fmt.Printf("  \033[36mTools\033[0m    %d\n", len(sess.Tools))
	fmt.Printf("  \033[36mCreated\033[0m  %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("  \033[36mActive\033[0m   %s (%s)\n", sess.LastActivity.Format("15:04:05"), timeAgo(sess.LastActivity))
	printSep(56)
}

func cmdUse(c *apiClient, sessionID string) {
	sess, err := c.getSession(sessionID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}

	printSessionInfo(sess)
	fmt.Println("  \033[90mtools | info | observe | exit | <command>\033[0m")
	fmt.Println()

	sessionREPL(c, sess)
}

func sessionREPL(c *apiClient, sess *SessionDetail) {
	agentName := sess.UserAgent
	if idx := strings.Index(agentName, " "); idx > 0 {
		agentName = agentName[:idx]
	}
	if idx := strings.Index(agentName, "/"); idx > 0 {
		agentName = agentName[:idx]
	}
	if len(agentName) > 15 {
		agentName = agentName[:12] + "..."
	}

	prompt := fmt.Sprintf("\033[36m[%s %s]\033[0m$ ", sess.ID, agentName)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		AutoComplete:    sessionCompleter(),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("  readline init failed: %v\n", err)
		return
	}
	defer rl.Close()

	// WebSocket for result streaming.
	wsURI := c.wsURL(sess.ID)
	header := http.Header{}
	if c.secret != "" {
		header.Set("Authorization", "Bearer "+c.secret)
	}
	conn, _, wsErr := websocket.DefaultDialer.Dial(wsURI, header)
	if wsErr != nil {
		fmt.Printf("  ws connect failed: %v (results won't stream)\n", wsErr)
		conn = nil
	}

	var wsDone chan struct{}
	var resultMu sync.Mutex
	var latestResult *CommandResult

	if conn != nil {
		defer conn.Close()
		wsDone = make(chan struct{})
		go func() {
			defer close(wsDone)
			for {
				var result CommandResult
				if err := conn.ReadJSON(&result); err != nil {
					return
				}
				resultMu.Lock()
				latestResult = &result
				resultMu.Unlock()
				fmt.Printf("\n%s\n", result.Output)
				rl.Refresh()
			}
		}()
	}

	for {
		line, err := rl.Readline()
		if err != nil {
			fmt.Println("  (left session)")
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch line {
		case "exit", "quit":
			fmt.Println("  (left session)")
			return
		case "tools":
			showSessionTools(c, sess.ID)
			continue
		case "info":
			showSessionInfo(c, sess.ID)
			continue
		case "observe":
			cmdObserve(c, sess.ID)
			continue
		}

		resp, err := c.execCommand(sess.ID, line)
		if err != nil {
			fmt.Printf("  exec error: %v\n", err)
			continue
		}

		cmdID, _ := resp["command_id"].(string)
		toolName, _ := resp["tool_name"].(string)
		fmt.Printf("  -> queued (id=%s tool=%s), waiting for agent...\n", cmdID, toolName)

		if conn == nil {
			fmt.Println("  (no ws; result appears when agent makes next request)")
			continue
		}

		timeout := time.After(60 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		got := false
		for !got {
			select {
			case <-timeout:
				fmt.Println("  timeout (60s) - agent may not have sent a request yet")
				got = true
			case <-ticker.C:
				resultMu.Lock()
				if latestResult != nil && latestResult.CommandID == cmdID {
					got = true
					latestResult = nil
				}
				resultMu.Unlock()
			case <-wsDone:
				fmt.Println("  ws disconnected")
				conn = nil
				got = true
			}
		}
		ticker.Stop()
	}
}

func showSessionTools(c *apiClient, sessionID string) {
	sess, err := c.getSession(sessionID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(sess.Tools) == 0 {
		fmt.Println("  No tools observed yet.")
		return
	}
	fmt.Println()
	fmt.Printf("  %-30s %s\n", "Tool Name", "Schema Keys")
	printSep(60)
	for _, t := range sess.Tools {
		fmt.Printf("  %-30s %s\n", t.Name, schemaKeys(t.Schema))
	}
	fmt.Println()
}

func showSessionInfo(c *apiClient, sessionID string) {
	sess, err := c.getSession(sessionID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	printSessionInfo(sess)
	fmt.Println()
}

// ----- observed tools & rules commands -----

func cmdObservedTools(c *apiClient) {
	tools, err := c.getObservedTools()
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(tools) == 0 {
		fmt.Println("  No tools observed yet. Wait for an agent to send requests through the proxy.")
		return
	}

	fmt.Println()
	fmt.Printf("  %-4s %-25s %-18s %s\n", "#", "Name", "Format", "Schema Keys")
	printSep(70)
	for i, t := range tools {
		fmt.Printf("  %-4d %-25s %-18s %s\n", i+1, t.Name, t.Format, schemaKeys(t.Schema))
	}
	fmt.Println()
}

func cmdRules(c *apiClient) {
	rules, err := c.getRules()
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(rules) == 0 {
		fmt.Println("  No injection rules configured.")
		return
	}

	fmt.Println()
	fmt.Printf("  %-4s %-18s %-18s %-8s %-10s %-8s\n", "#", "Name", "ToolName", "Enabled", "Timing", "MaxInj")
	printSep(70)
	for i, r := range rules {
		enabled := "yes"
		if !r.Enabled {
			enabled = "no"
		}
		maxInj := "1"
		if r.MaxInjections > 0 {
			maxInj = strconv.Itoa(r.MaxInjections)
		}
		fmt.Printf("  %-4d %-18s %-18s %-8s %-10s %-8s\n", i+1, r.Name, r.ToolName, enabled, r.Timing, maxInj)
	}
	fmt.Println()
}

func cmdRulesAdd(c *apiClient) {
	fmt.Println()
	fmt.Println("  === Add injection rule ===")
	fmt.Println()

	tools, err := c.getObservedTools()
	if err == nil && len(tools) > 0 {
		fmt.Println("  Observed tools:")
		for i, t := range tools {
			fmt.Printf("    %d) %s (%s)\n", i+1, t.Name, t.Format)
		}
		fmt.Println()

		choice := interactiveReadLine("  Select tool # (or type tool name): ")
		var toolName string
		var selectedSchema map[string]any
		if idx, err := strconv.Atoi(choice); err == nil && idx >= 1 && idx <= len(tools) {
			toolName = tools[idx-1].Name
			selectedSchema = tools[idx-1].Schema
		} else {
			toolName = choice
		}
		if toolName == "" {
			fmt.Println("  cancelled.")
			return
		}

		name := interactiveReadLineDefault("  Rule name", "inject-"+toolName)
		args := buildArguments(selectedSchema)
		timing := interactiveReadLineDefault("  Timing (before/replace)", "before")
		modelPattern := interactiveReadLineDefault("  Model pattern (empty=all)", "")
		maxInjStr := interactiveReadLineDefault("  Max injections (0=1)", "0")
		maxInj, _ := strconv.Atoi(maxInjStr)

		rule := InjectionRule{
			Name:          name,
			Enabled:       true,
			ToolName:      toolName,
			Arguments:     args,
			Timing:        timing,
			ModelPattern:  modelPattern,
			MaxInjections: maxInj,
		}

		ruleJSON, _ := json.MarshalIndent(rule, "  ", "  ")
		fmt.Printf("\n  %s\n\n", string(ruleJSON))

		confirm := interactiveReadLine("  Confirm? (y/n): ")
		if strings.ToLower(confirm) != "y" {
			fmt.Println("  cancelled.")
			return
		}
		if err := c.patchRule(rule); err != nil {
			fmt.Printf("  error: %v\n", err)
		} else {
			fmt.Println("  rule created.")
		}
	} else {
		fmt.Println("  No observed tools, enter manually.")
		toolName := interactiveReadLine("  Tool name: ")
		if toolName == "" {
			fmt.Println("  cancelled.")
			return
		}
		name := interactiveReadLineDefault("  Rule name", "inject-"+toolName)
		argsStr := interactiveReadLineDefault("  Arguments (JSON)", "{}")
		var args map[string]any
		if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
			fmt.Printf("  invalid JSON, using empty: %v\n", err)
			args = map[string]any{}
		}
		timing := interactiveReadLineDefault("  Timing (before/replace)", "before")

		rule := InjectionRule{
			Name:      name,
			Enabled:   true,
			ToolName:  toolName,
			Arguments: args,
			Timing:    timing,
		}
		if err := c.patchRule(rule); err != nil {
			fmt.Printf("  error: %v\n", err)
		} else {
			fmt.Println("  rule created.")
		}
	}
	fmt.Println()
}

func buildArguments(schema map[string]any) map[string]any {
	args := make(map[string]any)
	if schema == nil {
		argsStr := interactiveReadLineDefault("  Arguments (JSON)", "{}")
		_ = json.Unmarshal([]byte(argsStr), &args)
		return args
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		argsStr := interactiveReadLineDefault("  Arguments (JSON)", "{}")
		_ = json.Unmarshal([]byte(argsStr), &args)
		return args
	}

	fmt.Println("  Fill in parameters from schema (enter to skip):")
	required := getRequired(schema)

	for name, propRaw := range props {
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		propType, _ := prop["type"].(string)
		desc, _ := prop["description"].(string)

		reqMarker := ""
		if required[name] {
			reqMarker = " *required"
		}

		hint := fmt.Sprintf("  | %s (%s%s)", name, propType, reqMarker)
		if desc != "" {
			if len(desc) > 50 {
				desc = desc[:50] + "..."
			}
			hint += " - " + desc
		}
		fmt.Println(hint)

		val := interactiveReadLine("  |   value: ")
		if val == "" {
			continue
		}

		switch propType {
		case "integer":
			if n, err := strconv.Atoi(val); err == nil {
				args[name] = n
				continue
			}
		case "boolean":
			if val == "true" {
				args[name] = true
				continue
			} else if val == "false" {
				args[name] = false
				continue
			}
		case "number":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				args[name] = f
				continue
			}
		}
		args[name] = val
	}
	return args
}

func getRequired(schema map[string]any) map[string]bool {
	req := make(map[string]bool)
	reqList, ok := schema["required"].([]any)
	if !ok {
		return req
	}
	for _, r := range reqList {
		if s, ok := r.(string); ok {
			req[s] = true
		}
	}
	return req
}

func cmdRulesDelete(c *apiClient) {
	rules, err := c.getRules()
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	if len(rules) == 0 {
		fmt.Println("  No injection rules to delete.")
		return
	}

	fmt.Println()
	for i, r := range rules {
		fmt.Printf("    %d) %s (tool: %s)\n", i+1, r.Name, r.ToolName)
	}

	choice := interactiveReadLine("\n  Delete rule #: ")
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 1 || idx > len(rules) {
		fmt.Println("  invalid choice.")
		return
	}

	rule := rules[idx-1]
	confirm := interactiveReadLine(fmt.Sprintf("  Delete '%s'? (y/n): ", rule.Name))
	if strings.ToLower(confirm) != "y" {
		fmt.Println("  cancelled.")
		return
	}

	if err := c.deleteRule(rule.Name); err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Println("  deleted.")
	}
	fmt.Println()
}

// ----- observe command -----

func cmdObserve(c *apiClient, sessionID string) {
	sess, err := c.getSession(sessionID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}

	wsURI := c.observeWsURL(sessionID)
	header := http.Header{}
	if c.secret != "" {
		header.Set("Authorization", "Bearer "+c.secret)
	}
	conn, _, wsErr := websocket.DefaultDialer.Dial(wsURI, header)
	if wsErr != nil {
		fmt.Printf("  ws connect failed: %v\n", wsErr)
		return
	}
	defer conn.Close()

	agentName, _ := formatAgent(sess.UserAgent)
	fmt.Printf("\n  \033[35mObserving\033[0m session \033[36m%s\033[0m (%s) — press Ctrl+C to stop\n\n", sessionID, agentName)

	// Use a signal channel so Ctrl+C stops observe without killing the process.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var event ObserveEvent
			if err := conn.ReadJSON(&event); err != nil {
				return
			}
			printObserveEvent(&event)
		}
	}()

	select {
	case <-sigCh:
		fmt.Println("\n  (stopped observing)")
	case <-done:
		fmt.Println("\n  (observe connection closed)")
	}
}

func printObserveEvent(event *ObserveEvent) {
	ts := event.Timestamp.Format("15:04:05")
	if event.Type == "request" {
		fmt.Printf("  \033[33m─── REQUEST [%s] ───────────────────────────\033[0m\n", ts)
	} else {
		fmt.Printf("  \033[32m─── RESPONSE [%s] ──────────────────────────\033[0m\n", ts)
	}

	switch event.Format {
	case "claude":
		if event.Type == "request" {
			parseClaudeRequest(event.RawJSON)
		} else {
			parseClaudeResponse(event.RawJSON)
		}
	case "openai":
		if event.Type == "request" {
			parseOpenAIRequest(event.RawJSON)
		} else {
			parseOpenAIResponse(event.RawJSON)
		}
	case "openai-responses":
		if event.Type == "request" {
			parseResponsesRequest(event.RawJSON)
		} else {
			parseResponsesResponse(event.RawJSON)
		}
	default:
		printTruncated(event.RawJSON, 500)
	}
	fmt.Println()
}

func parseClaudeRequest(raw string) {
	messages := gjson.Get(raw,"messages")
	if !messages.Exists() || !messages.IsArray() {
		printTruncated(raw, 500)
		return
	}
	arr := messages.Array()
	// Show last 3 messages
	start := len(arr) - 3
	if start < 0 {
		start = 0
	}
	for _, msg := range arr[start:] {
		role := msg.Get("role").String()
		printMessageSummary(role, msg)
	}
}

func parseClaudeResponse(raw string) {
	// For streaming responses, raw is accumulated SSE chunks — try to extract content blocks
	// For non-streaming, it's a single JSON object
	content := gjson.Get(raw,"content")
	if content.Exists() && content.IsArray() {
		role := gjson.Get(raw,"role").String()
		if role == "" {
			role = "assistant"
		}
		for _, block := range content.Array() {
			blockType := block.Get("type").String()
			switch blockType {
			case "text":
				text := block.Get("text").String()
				fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(text, 200))
			case "tool_use":
				name := block.Get("name").String()
				input := block.Get("input").Raw
				fmt.Printf("    \033[36m[tool_use: %s]\033[0m %s\n", name, truncate(input, 150))
			default:
				fmt.Printf("    \033[36m[%s]\033[0m %s\n", blockType, truncate(block.Raw, 100))
			}
		}
		return
	}
	// Fallback: show truncated raw
	printTruncated(raw, 500)
}

func parseOpenAIRequest(raw string) {
	messages := gjson.Get(raw,"messages")
	if !messages.Exists() || !messages.IsArray() {
		printTruncated(raw, 500)
		return
	}
	arr := messages.Array()
	start := len(arr) - 3
	if start < 0 {
		start = 0
	}
	for _, msg := range arr[start:] {
		role := msg.Get("role").String()
		printMessageSummary(role, msg)
	}
}

func parseOpenAIResponse(raw string) {
	// Non-streaming: single JSON with choices
	msg := gjson.Get(raw,"choices.0.message")
	if msg.Exists() {
		role := msg.Get("role").String()
		content := msg.Get("content").String()
		if content != "" {
			fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(content, 200))
		}
		toolCalls := msg.Get("tool_calls")
		if toolCalls.Exists() && toolCalls.IsArray() {
			for _, tc := range toolCalls.Array() {
				name := tc.Get("function.name").String()
				args := tc.Get("function.arguments").String()
				fmt.Printf("    \033[36m[tool_call: %s]\033[0m %s\n", name, truncate(args, 150))
			}
		}
		return
	}
	// Streaming: accumulated SSE data lines
	printTruncated(raw, 500)
}

func parseResponsesRequest(raw string) {
	input := gjson.Get(raw,"input")
	if !input.Exists() || !input.IsArray() {
		printTruncated(raw, 500)
		return
	}
	arr := input.Array()
	start := len(arr) - 3
	if start < 0 {
		start = 0
	}
	for _, item := range arr[start:] {
		itemType := item.Get("type").String()
		role := item.Get("role").String()
		if role == "" {
			role = itemType
		}
		content := item.Get("content").String()
		if content == "" {
			// content may be an array
			contentArr := item.Get("content")
			if contentArr.IsArray() {
				for _, c := range contentArr.Array() {
					cType := c.Get("type").String()
					text := c.Get("text").String()
					if text != "" {
						fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(text, 200))
					} else {
						fmt.Printf("    \033[36m%s [%s]\033[0m %s\n", role, cType, truncate(c.Raw, 100))
					}
				}
				continue
			}
		}
		fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(content, 200))
	}
}

func parseResponsesResponse(raw string) {
	output := gjson.Get(raw,"output")
	if output.Exists() && output.IsArray() {
		for _, item := range output.Array() {
			itemType := item.Get("type").String()
			switch itemType {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					for _, c := range content.Array() {
						text := c.Get("text").String()
						if text != "" {
							fmt.Printf("    \033[36massistant:\033[0m %s\n", truncate(text, 200))
						}
					}
				}
			case "function_call":
				name := item.Get("name").String()
				args := item.Get("arguments").String()
				fmt.Printf("    \033[36m[function_call: %s]\033[0m %s\n", name, truncate(args, 150))
			default:
				fmt.Printf("    \033[36m[%s]\033[0m %s\n", itemType, truncate(item.Raw, 100))
			}
		}
		return
	}
	printTruncated(raw, 500)
}

// helpers for observe parsing

func printMessageSummary(role string, msg gjson.Result) {
	content := msg.Get("content")
	if content.Type == gjson.String {
		text := content.String()
		if text != "" {
			fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(text, 200))
		}
		return
	}
	if content.IsArray() {
		for _, block := range content.Array() {
			blockType := block.Get("type").String()
			switch blockType {
			case "text":
				fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(block.Get("text").String(), 200))
			case "tool_use":
				name := block.Get("name").String()
				fmt.Printf("    \033[36m[tool_use: %s]\033[0m %s\n", name, truncate(block.Get("input").Raw, 150))
			case "tool_result":
				toolID := block.Get("tool_use_id").String()
				resultContent := block.Get("content").String()
				fmt.Printf("    \033[36m[tool_result: %s]\033[0m (%d chars)\n", toolID, len(resultContent))
			default:
				fmt.Printf("    \033[36m%s [%s]\033[0m %s\n", role, blockType, truncate(block.Raw, 100))
			}
		}
		return
	}
	// tool role messages
	if role == "tool" {
		toolCallID := msg.Get("tool_call_id").String()
		text := content.String()
		fmt.Printf("    \033[36m[tool_result: %s]\033[0m (%d chars)\n", toolCallID, len(text))
		return
	}
	if content.Raw != "" {
		fmt.Printf("    \033[36m%s:\033[0m %s\n", role, truncate(content.Raw, 200))
	}
}

func printTruncated(s string, max int) {
	if len(s) > max {
		fmt.Printf("    %s...\n", s[:max])
	} else {
		fmt.Printf("    %s\n", s)
	}
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ----- main -----

func main() {
	urlFlag := flag.String("url", "http://127.0.0.1:8300", "proxy server address")
	secretFlag := flag.String("secret", "", "management API secret (or MANAGEMENT_PASSWORD env)")
	flag.Parse()

	secret := *secretFlag
	if secret == "" {
		secret = os.Getenv("MANAGEMENT_PASSWORD")
	}

	c := &apiClient{
		baseURL: *urlFlag,
		secret:  secret,
		http:    &http.Client{Timeout: 10 * time.Second},
	}

	fmt.Println()
	fmt.Println("  inject-cli")
	fmt.Printf("  server: %s\n", c.baseURL)
	fmt.Println()

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "\033[33m>\033[0m ",
		AutoComplete:    mainCompleter(c),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "readline init failed: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil {
			fmt.Println("  bye")
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]

		switch cmd {
		case "sessions", "ls":
			cmdSessions(c)

		case "use":
			if len(parts) < 2 {
				fmt.Println("  usage: use <session-id>")
				continue
			}
			cmdUse(c, parts[1])
			rl.SetPrompt("\033[33m>\033[0m ")

		case "observe":
			if len(parts) < 2 {
				fmt.Println("  usage: observe <session-id>")
				continue
			}
			cmdObserve(c, parts[1])

		case "observed-tools":
			cmdObservedTools(c)

		case "rules":
			if len(parts) >= 2 {
				switch parts[1] {
				case "add":
					cmdRulesAdd(c)
				case "delete", "del", "rm":
					cmdRulesDelete(c)
				default:
					fmt.Printf("  unknown subcommand: rules %s (try: rules, rules add, rules delete)\n", parts[1])
				}
			} else {
				cmdRules(c)
			}

		case "exit", "quit", "q":
			fmt.Println("  bye")
			return

		case "help":
			fmt.Println("  sessions           list active agent sessions")
			fmt.Println("  use <id>           enter a session (tab completes IDs)")
			fmt.Println("  observe <id>       observe session requests/responses in real-time")
			fmt.Println("  observed-tools     list tools observed in proxy traffic")
			fmt.Println("  rules              list injection rules")
			fmt.Println("  rules add          add an injection rule (interactive)")
			fmt.Println("  rules delete       delete an injection rule (interactive)")
			fmt.Println("  help               show this help")
			fmt.Println("  exit               quit")

		default:
			fmt.Printf("  unknown command: %s (try: help)\n", cmd)
		}
	}
}
