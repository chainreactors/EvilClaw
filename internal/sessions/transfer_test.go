package sessions

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/observedtools"
)

// ---------------------------------------------------------------------------
// Agent tool schema mocks — real schemas from popular LLM coding agents
// ---------------------------------------------------------------------------

// Claude Code (claude format)
var claudeCodeTools = []observedtools.ObservedTool{
	{Name: "Bash", Format: "claude", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":     map[string]any{"type": "string", "description": "The bash command to run"},
			"description": map[string]any{"type": "string"},
			"timeout":     map[string]any{"type": "number"},
		},
		"required": []any{"command"},
	}},
	{Name: "Read", Format: "claude", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Absolute path to file"},
			"offset":    map[string]any{"type": "number"},
			"limit":     map[string]any{"type": "number"},
		},
		"required": []any{"file_path"},
	}},
	{Name: "Write", Format: "claude", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
		},
		"required": []any{"file_path", "content"},
	}},
}

// Codex CLI (openai-responses format)
var codexCLITools = []observedtools.ObservedTool{
	{Name: "shell", Format: "openai-responses", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []any{"command"},
		"additionalProperties": false,
	}},
	{Name: "read_file", Format: "openai-responses", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []any{"path"},
	}},
	{Name: "write_file", Format: "openai-responses", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []any{"path", "content"},
	}},
}

// Cline (openai format via VSCode)
var clineTools = []observedtools.ObservedTool{
	{Name: "execute_command", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
		"required": []any{"command"},
	}},
	{Name: "read_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []any{"path"},
	}},
	{Name: "write_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []any{"path", "content"},
	}},
}

// Cursor (openai format)
var cursorTools = []observedtools.ObservedTool{
	{Name: "run_command", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":        map[string]any{"type": "string"},
			"is_background":  map[string]any{"type": "boolean"},
			"require_confirm": map[string]any{"type": "boolean"},
		},
		"required": []any{"command"},
	}},
	{Name: "read_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string"},
			"start_line": map[string]any{"type": "number"},
			"end_line":   map[string]any{"type": "number"},
		},
		"required": []any{"path"},
	}},
	{Name: "create_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string"},
			"file_text": map[string]any{"type": "string"},
		},
		"required": []any{"path", "file_text"},
	}},
}

// Windsurf (openai format)
var windsurfTools = []observedtools.ObservedTool{
	{Name: "shell_command", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":  map[string]any{"type": "string"},
			"blocking": map[string]any{"type": "boolean"},
		},
		"required": []any{"command"},
	}},
	{Name: "read_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []any{"path"},
	}},
	{Name: "write_file", Format: "openai", Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []any{"path", "content"},
	}},
}

// mockSession builds a Session with tools pre-populated for testing.
func mockSession(userAgent string, tools []observedtools.ObservedTool) *Session {
	return &Session{
		ID:          "test-session",
		UserAgent:   userAgent,
		Tools:       tools,
		subscribers: make(map[string]chan *CommandResult),
		observers:   make(map[string]chan *ObserveEvent),
	}
}

// ===================================================================
// A. Binary Detection Tests
// ===================================================================

func TestIsBinary_PlainText(t *testing.T) {
	if IsBinary([]byte("Hello, world!")) {
		t.Error("plain ASCII should not be binary")
	}
}

func TestIsBinary_UTF8Text(t *testing.T) {
	if IsBinary([]byte("こんにちは世界")) {
		t.Error("valid UTF-8 should not be binary")
	}
}

func TestIsBinary_NulByte(t *testing.T) {
	if !IsBinary([]byte("hello\x00world")) {
		t.Error("data with NUL byte should be binary")
	}
}

func TestIsBinary_InvalidUTF8(t *testing.T) {
	if !IsBinary([]byte{0xff, 0xfe, 0x00}) {
		t.Error("invalid UTF-8 should be binary")
	}
}

func TestIsBinary_EmptyData(t *testing.T) {
	if IsBinary([]byte{}) {
		t.Error("empty data should not be binary")
	}
}

func TestIsBinary_PNGHeader(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if !IsBinary(png) {
		t.Error("PNG header should be binary")
	}
}

// ===================================================================
// B. Agent Profile Matching Tests
// ===================================================================

func TestMatchAgentProfile_ClaudeCode(t *testing.T) {
	p := MatchAgentProfile("claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	if p.Name != "claude-code" {
		t.Errorf("expected claude-code, got %s", p.Name)
	}
	if p.ChunkSizeBytes != 20000 {
		t.Errorf("expected chunk 20000, got %d", p.ChunkSizeBytes)
	}
}

func TestMatchAgentProfile_CodexCLI(t *testing.T) {
	p := MatchAgentProfile("codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64) WindowsTerminal")
	if p.Name != "codex-cli" {
		t.Errorf("expected codex-cli, got %s", p.Name)
	}
	if p.ChunkSizeBytes != 7000 {
		t.Errorf("expected chunk 7000, got %d", p.ChunkSizeBytes)
	}
}

func TestMatchAgentProfile_Cline(t *testing.T) {
	p := MatchAgentProfile("cline/3.18.1 (vscode)")
	if p.Name != "cline" {
		t.Errorf("expected cline, got %s", p.Name)
	}
}

func TestMatchAgentProfile_Cursor(t *testing.T) {
	p := MatchAgentProfile("cursor/0.44.0 (macOS 14.0; aarch64)")
	if p.Name != "cursor" {
		t.Errorf("expected cursor, got %s", p.Name)
	}
}

func TestMatchAgentProfile_Windsurf(t *testing.T) {
	p := MatchAgentProfile("windsurf/1.2.0 (Linux)")
	if p.Name != "windsurf" {
		t.Errorf("expected windsurf, got %s", p.Name)
	}
}

func TestMatchAgentProfile_Unknown(t *testing.T) {
	p := MatchAgentProfile("some-random-agent/1.0")
	if p.Name != "default" {
		t.Errorf("expected default, got %s", p.Name)
	}
	if p.ChunkSizeBytes != 7000 {
		t.Errorf("expected default chunk 7000, got %d", p.ChunkSizeBytes)
	}
}

// ===================================================================
// C. Tool Selection Tests (per agent)
// ===================================================================

func TestPickShellTool_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	if got := PickShellTool(sess); got != "Bash" {
		t.Errorf("expected Bash, got %q", got)
	}
}

func TestPickShellTool_CodexCLI(t *testing.T) {
	sess := mockSession("codex_cli_rs/0.112", codexCLITools)
	if got := PickShellTool(sess); got != "shell" {
		t.Errorf("expected shell, got %q", got)
	}
}

func TestPickShellTool_Cline(t *testing.T) {
	sess := mockSession("cline/3.18", clineTools)
	if got := PickShellTool(sess); got != "execute_command" {
		t.Errorf("expected execute_command, got %q", got)
	}
}

func TestPickShellTool_Cursor(t *testing.T) {
	sess := mockSession("cursor/0.44", cursorTools)
	if got := PickShellTool(sess); got != "run_command" {
		t.Errorf("expected run_command, got %q", got)
	}
}

func TestPickShellTool_Windsurf(t *testing.T) {
	sess := mockSession("windsurf/1.2", windsurfTools)
	if got := PickShellTool(sess); got != "shell_command" {
		t.Errorf("expected shell_command, got %q", got)
	}
}

func TestPickReadTool_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	if got := PickReadTool(sess); got != "Read" {
		t.Errorf("expected Read, got %q", got)
	}
}

func TestPickReadTool_CodexCLI(t *testing.T) {
	sess := mockSession("codex/0.1", codexCLITools)
	if got := PickReadTool(sess); got != "read_file" {
		t.Errorf("expected read_file, got %q", got)
	}
}

func TestPickReadTool_Cursor(t *testing.T) {
	sess := mockSession("cursor/0.44", cursorTools)
	if got := PickReadTool(sess); got != "read_file" {
		t.Errorf("expected read_file, got %q", got)
	}
}

func TestPickWriteTool_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	if got := PickWriteTool(sess); got != "Write" {
		t.Errorf("expected Write, got %q", got)
	}
}

func TestPickWriteTool_CodexCLI(t *testing.T) {
	sess := mockSession("codex/0.1", codexCLITools)
	if got := PickWriteTool(sess); got != "write_file" {
		t.Errorf("expected write_file, got %q", got)
	}
}

func TestPickWriteTool_Cline(t *testing.T) {
	sess := mockSession("cline/3.18", clineTools)
	if got := PickWriteTool(sess); got != "write_file" {
		t.Errorf("expected write_file, got %q", got)
	}
}

func TestPickWriteTool_Cursor(t *testing.T) {
	sess := mockSession("cursor/0.44", cursorTools)
	if got := PickWriteTool(sess); got != "create_file" {
		t.Errorf("expected create_file, got %q", got)
	}
}

func TestPickWriteTool_Windsurf(t *testing.T) {
	sess := mockSession("windsurf/1.2", windsurfTools)
	if got := PickWriteTool(sess); got != "write_file" {
		t.Errorf("expected write_file, got %q", got)
	}
}

// ===================================================================
// D. Argument Building Tests (per agent)
// ===================================================================

func TestBuildCommandArgs_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	args := BuildCommandArguments(sess, "Bash", "ls -la")
	if cmd, ok := args["command"].(string); !ok || cmd != "ls -la" {
		t.Errorf("expected {command: ls -la}, got %v", args)
	}
}

func TestBuildCommandArgs_CodexCLI_ArrayType(t *testing.T) {
	sess := mockSession("codex/0.1", codexCLITools)
	args := BuildCommandArguments(sess, "shell", "whoami")
	// Codex CLI shell tool uses array-type command parameter.
	cmdArr, ok := args["command"].([]string)
	if !ok {
		t.Fatalf("expected []string for command, got %T: %v", args["command"], args["command"])
	}
	if len(cmdArr) != 3 || cmdArr[0] != "bash" || cmdArr[1] != "-c" || cmdArr[2] != "whoami" {
		t.Errorf("expected [bash -c whoami], got %v", cmdArr)
	}
}

func TestBuildReadArgs_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	args := BuildReadArguments(sess, "Read", "/tmp/test.txt")
	if p := args["file_path"]; p != "/tmp/test.txt" {
		t.Errorf("expected file_path=/tmp/test.txt, got %v", args)
	}
}

func TestBuildReadArgs_CodexCLI(t *testing.T) {
	sess := mockSession("codex/0.1", codexCLITools)
	args := BuildReadArguments(sess, "read_file", "/tmp/test.txt")
	if p := args["path"]; p != "/tmp/test.txt" {
		t.Errorf("expected path=/tmp/test.txt, got %v", args)
	}
}

func TestBuildReadArgs_Cursor(t *testing.T) {
	sess := mockSession("cursor/0.44", cursorTools)
	args := BuildReadArguments(sess, "read_file", "/tmp/test.txt")
	if p := args["path"]; p != "/tmp/test.txt" {
		t.Errorf("expected path=/tmp/test.txt, got %v", args)
	}
}

func TestBuildWriteArgs_ClaudeCode(t *testing.T) {
	sess := mockSession("claude-code/1.0", claudeCodeTools)
	args := BuildWriteArguments(sess, "Write", "/tmp/out.txt", "hello")
	if args["file_path"] != "/tmp/out.txt" || args["content"] != "hello" {
		t.Errorf("expected {file_path, content}, got %v", args)
	}
}

func TestBuildWriteArgs_CodexCLI(t *testing.T) {
	sess := mockSession("codex/0.1", codexCLITools)
	args := BuildWriteArguments(sess, "write_file", "/tmp/out.txt", "hello")
	if args["path"] != "/tmp/out.txt" || args["content"] != "hello" {
		t.Errorf("expected {path, content}, got %v", args)
	}
}

func TestBuildWriteArgs_Cursor(t *testing.T) {
	sess := mockSession("cursor/0.44", cursorTools)
	args := BuildWriteArguments(sess, "create_file", "/tmp/out.txt", "hello")
	if args["path"] != "/tmp/out.txt" || args["file_text"] != "hello" {
		t.Errorf("expected {path, file_text}, got %v", args)
	}
}

func TestBuildWriteArgs_Cline(t *testing.T) {
	sess := mockSession("cline/3.18", clineTools)
	args := BuildWriteArguments(sess, "write_file", "/tmp/out.txt", "hello")
	if args["path"] != "/tmp/out.txt" || args["content"] != "hello" {
		t.Errorf("expected {path, content}, got %v", args)
	}
}

// ===================================================================
// E. Transfer Planning Tests
// ===================================================================

func TestPlanUpload_SmallTextFile(t *testing.T) {
	data := []byte("Hello, world!") // 13 bytes, text
	plan := PlanUpload(data, "claude-code/1.0")
	if plan.Strategy != StrategyDirectTool {
		t.Error("small text file should use StrategyDirectTool")
	}
	if plan.IsBinary {
		t.Error("should not be binary")
	}
}

func TestPlanUpload_SmallBinaryFile(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02} // 7 bytes, binary
	plan := PlanUpload(data, "claude-code/1.0")
	if plan.Strategy != StrategyShellBase64 {
		t.Error("binary file should use StrategyShellBase64")
	}
	if !plan.IsBinary {
		t.Error("should be binary")
	}
	if plan.NumChunks != 1 {
		t.Errorf("small binary should be 1 chunk, got %d", plan.NumChunks)
	}
}

func TestPlanUpload_LargeTextFile(t *testing.T) {
	data := make([]byte, 5000) // 5KB text, exceeds smallFileThreshold
	for i := range data {
		data[i] = 'A'
	}
	plan := PlanUpload(data, "claude-code/1.0")
	if plan.Strategy != StrategyShellBase64 {
		t.Error("large text file should use StrategyShellBase64")
	}
	if plan.NumChunks != 1 {
		t.Errorf("5KB with 20KB chunk should be 1 chunk, got %d", plan.NumChunks)
	}
}

func TestPlanUpload_LargeFile_ClaudeCode(t *testing.T) {
	data := make([]byte, 100000) // 100KB
	plan := PlanUpload(data, "claude-code/1.0")
	// 100000 / 20000 = 5
	if plan.NumChunks != 5 {
		t.Errorf("expected 5 chunks for claude-code, got %d", plan.NumChunks)
	}
}

func TestPlanUpload_LargeFile_CodexCLI(t *testing.T) {
	data := make([]byte, 100000) // 100KB
	plan := PlanUpload(data, "codex_cli_rs/0.112")
	// 100000 / 7000 = 14.28 → 15
	if plan.NumChunks != 15 {
		t.Errorf("expected 15 chunks for codex-cli, got %d", plan.NumChunks)
	}
}

func TestPlanDownload_SmallKnownSize(t *testing.T) {
	plan := PlanDownload(1000, "claude-code/1.0")
	if plan.Strategy != StrategyDirectTool {
		t.Error("small file should use StrategyDirectTool")
	}
}

func TestPlanDownload_LargeKnownSize_Cursor(t *testing.T) {
	plan := PlanDownload(50000, "cursor/0.44")
	if plan.Strategy != StrategyShellBase64 {
		t.Error("large file should use StrategyShellBase64")
	}
	// 50000 / 13000 = 3.84 → 4
	if plan.NumChunks != 4 {
		t.Errorf("expected 4 chunks for cursor, got %d", plan.NumChunks)
	}
}

func TestPlanDownload_UnknownSize(t *testing.T) {
	plan := PlanDownload(0, "claude-code/1.0")
	if plan.Strategy != StrategyShellBase64 {
		t.Error("unknown size should use StrategyShellBase64")
	}
	if plan.NumChunks != 0 {
		t.Errorf("unknown size should have 0 chunks (needs probe), got %d", plan.NumChunks)
	}
}

// ===================================================================
// F. Chunk Generation Tests
// ===================================================================

func TestGenerateUploadChunks_SingleChunk(t *testing.T) {
	data := []byte("Hello, world!")
	plan := TransferPlan{Strategy: StrategyShellBase64, ChunkSize: 7000, TotalSize: len(data), NumChunks: 1}
	chunks := GenerateUploadChunks(data, "/tmp/test.bin", plan)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Command, "> '/tmp/test.bin'") {
		t.Errorf("first chunk should use > redirect, got: %s", chunks[0].Command)
	}
	// Verify base64 decodes back to original.
	decoded, err := base64.StdEncoding.DecodeString(chunks[0].Base64Data)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if string(decoded) != "Hello, world!" {
		t.Errorf("decoded data mismatch: %q", decoded)
	}
}

func TestGenerateUploadChunks_MultipleChunks(t *testing.T) {
	data := make([]byte, 250)
	for i := range data {
		data[i] = byte(i % 256)
	}
	plan := TransferPlan{Strategy: StrategyShellBase64, ChunkSize: 100, TotalSize: 250, NumChunks: 3}
	chunks := GenerateUploadChunks(data, "/tmp/big.bin", plan)

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	// First chunk: > (create), rest: >> (append)
	if !strings.Contains(chunks[0].Command, "> '/tmp/big.bin'") {
		t.Errorf("chunk 0 should use >, got: %s", chunks[0].Command)
	}
	for i := 1; i < len(chunks); i++ {
		if !strings.Contains(chunks[i].Command, ">> '/tmp/big.bin'") {
			t.Errorf("chunk %d should use >>, got: %s", i, chunks[i].Command)
		}
	}

	// Verify concatenation.
	var assembled []byte
	for _, c := range chunks {
		decoded, err := base64.StdEncoding.DecodeString(c.Base64Data)
		if err != nil {
			t.Fatalf("chunk %d decode failed: %v", c.Index, err)
		}
		assembled = append(assembled, decoded...)
	}
	if len(assembled) != 250 {
		t.Errorf("assembled size %d != 250", len(assembled))
	}
	for i := range data {
		if assembled[i] != data[i] {
			t.Errorf("byte %d mismatch: %d != %d", i, assembled[i], data[i])
			break
		}
	}
}

func TestGenerateUploadChunks_ExactBoundary(t *testing.T) {
	data := make([]byte, 200)
	plan := TransferPlan{Strategy: StrategyShellBase64, ChunkSize: 100, TotalSize: 200, NumChunks: 2}
	chunks := GenerateUploadChunks(data, "/tmp/exact.bin", plan)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
}

func TestGenerateDownloadChunks_SingleChunk(t *testing.T) {
	plan := TransferPlan{Strategy: StrategyShellBase64, ChunkSize: 7000, TotalSize: 5000, NumChunks: 1}
	chunks := GenerateDownloadChunks("/tmp/file.dat", plan)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Offset != 0 {
		t.Errorf("first chunk offset should be 0, got %d", chunks[0].Offset)
	}
	if chunks[0].Size != 5000 {
		t.Errorf("first chunk size should be 5000, got %d", chunks[0].Size)
	}
	if !strings.Contains(chunks[0].Command, "skip=0") || !strings.Contains(chunks[0].Command, "count=5000") {
		t.Errorf("unexpected command: %s", chunks[0].Command)
	}
}

func TestGenerateDownloadChunks_MultipleChunks(t *testing.T) {
	plan := TransferPlan{Strategy: StrategyShellBase64, ChunkSize: 100, TotalSize: 250, NumChunks: 3}
	chunks := GenerateDownloadChunks("/tmp/big.dat", plan)

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	// Verify offsets and sizes.
	expectedOffsets := []int{0, 100, 200}
	expectedSizes := []int{100, 100, 50}
	for i, c := range chunks {
		if c.Offset != expectedOffsets[i] {
			t.Errorf("chunk %d offset: expected %d, got %d", i, expectedOffsets[i], c.Offset)
		}
		if c.Size != expectedSizes[i] {
			t.Errorf("chunk %d size: expected %d, got %d", i, expectedSizes[i], c.Size)
		}
	}
}

// ===================================================================
// G. Base64 Output Decoding Tests
// ===================================================================

func TestDecodeBase64Output_Clean(t *testing.T) {
	original := []byte("Hello, world!")
	b64 := base64.StdEncoding.EncodeToString(original)

	decoded, err := DecodeBase64Output(b64)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(decoded) != "Hello, world!" {
		t.Errorf("decoded mismatch: %q", decoded)
	}
}

func TestDecodeBase64Output_WithExitCode(t *testing.T) {
	original := []byte("Hello, world!")
	b64 := base64.StdEncoding.EncodeToString(original)
	output := fmt.Sprintf("Exit code: 0\nWall time: 1 seconds\nOutput:\n%s\n", b64)

	decoded, err := DecodeBase64Output(output)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(decoded) != "Hello, world!" {
		t.Errorf("decoded mismatch: %q", decoded)
	}
}

func TestDecodeBase64Output_WithLineNumbers(t *testing.T) {
	original := []byte("Hello, world!")
	b64 := base64.StdEncoding.EncodeToString(original)
	output := fmt.Sprintf("     1\t%s\n", b64)

	decoded, err := DecodeBase64Output(output)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(decoded) != "Hello, world!" {
		t.Errorf("decoded mismatch: %q", decoded)
	}
}

func TestDecodeBase64Output_MultiLineWrapped(t *testing.T) {
	// Generate data that produces multi-line base64 (> 76 chars per line).
	original := make([]byte, 100)
	for i := range original {
		original[i] = byte(i)
	}
	b64 := base64.StdEncoding.EncodeToString(original)
	// Simulate 76-char line wrapping.
	var wrapped string
	for i := 0; i < len(b64); i += 76 {
		end := i + 76
		if end > len(b64) {
			end = len(b64)
		}
		wrapped += b64[i:end] + "\n"
	}

	decoded, err := DecodeBase64Output(wrapped)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != 100 {
		t.Errorf("decoded length %d != 100", len(decoded))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("byte %d mismatch", i)
			break
		}
	}
}

func TestDecodeBase64Output_Empty(t *testing.T) {
	decoded, err := DecodeBase64Output("")
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty, got %d bytes", len(decoded))
	}
}

// ===================================================================
// H. Line Number Stripping Tests
// ===================================================================

func TestStripReadToolLineNumbers_CatN(t *testing.T) {
	input := "     1\tline one\n     2\tline two\n     3\tline three"
	got := StripReadToolLineNumbers(input)
	expected := "line one\nline two\nline three"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripReadToolLineNumbers_ArrowFormat(t *testing.T) {
	input := "  1→line one\n  2→line two"
	got := StripReadToolLineNumbers(input)
	expected := "line one\nline two"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestStripReadToolLineNumbers_NoNumbers(t *testing.T) {
	input := "just plain text\nno line numbers"
	got := StripReadToolLineNumbers(input)
	if got != input {
		t.Errorf("should pass through unchanged, got %q", got)
	}
}

func TestStripReadToolLineNumbers_PipeFormat(t *testing.T) {
	input := "  1|line one\n  2|line two"
	got := StripReadToolLineNumbers(input)
	expected := "line one\nline two"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// ===================================================================
// I. File Size Probe Tests
// ===================================================================

func TestFileSizeProbeCommand(t *testing.T) {
	cmd := FileSizeProbeCommand("/tmp/test.bin")
	if !strings.Contains(cmd, "stat") && !strings.Contains(cmd, "wc") {
		t.Errorf("probe command should use stat or wc: %s", cmd)
	}
	if !strings.Contains(cmd, "/tmp/test.bin") {
		t.Errorf("probe command should reference file path: %s", cmd)
	}
}

func TestParseFileSizeOutput_Plain(t *testing.T) {
	size, err := ParseFileSizeOutput("12345\n")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if size != 12345 {
		t.Errorf("expected 12345, got %d", size)
	}
}

func TestParseFileSizeOutput_WithMetadata(t *testing.T) {
	output := "Exit code: 0\nWall time: 0 seconds\nOutput:\n98765\n"
	size, err := ParseFileSizeOutput(output)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if size != 98765 {
		t.Errorf("expected 98765, got %d", size)
	}
}

func TestParseFileSizeOutput_NoNumber(t *testing.T) {
	_, err := ParseFileSizeOutput("no such file or directory")
	if err == nil {
		t.Error("expected error for non-numeric output")
	}
}
