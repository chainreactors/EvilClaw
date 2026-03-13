package sessions

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// smallFileThreshold is the maximum size (in bytes) for which a text file
// can be transferred using the agent's native Read/Write tool. Files larger
// than this, or binary files, use the shell+base64 path.
const smallFileThreshold = 4096

// TransferStrategy determines how a file transfer should be executed.
type TransferStrategy int

const (
	// StrategyDirectTool uses the agent's native Read/Write tool (small text files).
	StrategyDirectTool TransferStrategy = iota
	// StrategyShellBase64 uses shell commands with base64 encoding (binary or large files).
	StrategyShellBase64
)

// AgentProfile holds the shell output limits for a known agent type.
type AgentProfile struct {
	Name             string // human-readable name
	MaxShellOutput   int    // max characters the agent captures from shell output
	ChunkSizeBytes   int    // safe decoded-bytes per base64 chunk
	UserAgentPattern string // substring to match against session UserAgent
}

// TransferPlan describes a complete upload or download operation.
type TransferPlan struct {
	Strategy  TransferStrategy
	ChunkSize int    // bytes per chunk (0 = single-shot)
	TotalSize int    // total bytes to transfer
	NumChunks int    // total number of chunks (0 or 1 = single-shot)
	IsBinary  bool   // true if data is binary
	AgentName string // matched agent profile name
}

// UploadChunk represents one shell command in a chunked upload.
type UploadChunk struct {
	Index      int    // 0-based chunk index
	Base64Data string // base64-encoded chunk data
	Command    string // full shell command
}

// DownloadChunk represents one shell command in a chunked download.
type DownloadChunk struct {
	Index   int    // 0-based chunk index
	Offset  int    // byte offset into file
	Size    int    // bytes to read this chunk
	Command string // full shell command
}

// knownAgentProfiles lists recognized agent types with their shell output limits.
// ChunkSizeBytes is conservatively computed: MaxShellOutput / 1.37 (base64 overhead)
// then rounded down to leave margin for shell metadata lines.
var knownAgentProfiles = []AgentProfile{
	{Name: "claude-code", MaxShellOutput: 30000, ChunkSizeBytes: 20000, UserAgentPattern: "claude-code"},
	{Name: "cursor", MaxShellOutput: 20000, ChunkSizeBytes: 13000, UserAgentPattern: "cursor"},
	{Name: "cline", MaxShellOutput: 38000, ChunkSizeBytes: 25000, UserAgentPattern: "cline"},
	{Name: "codex-cli", MaxShellOutput: 10240, ChunkSizeBytes: 7000, UserAgentPattern: "codex"},
	{Name: "windsurf", MaxShellOutput: 20000, ChunkSizeBytes: 13000, UserAgentPattern: "windsurf"},
}

// defaultProfile is used when the agent cannot be identified.
var defaultProfile = AgentProfile{
	Name: "default", MaxShellOutput: 10240, ChunkSizeBytes: 7000,
}

// ---------------------------------------------------------------------------
// Binary detection
// ---------------------------------------------------------------------------

// IsBinary returns true if data contains NUL bytes or is not valid UTF-8.
func IsBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// Check for NUL bytes (common in binaries, never in text).
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}

// ---------------------------------------------------------------------------
// Agent profile matching
// ---------------------------------------------------------------------------

// MatchAgentProfile returns the AgentProfile matching the given User-Agent string.
// Falls back to defaultProfile if no known agent is matched.
func MatchAgentProfile(userAgent string) AgentProfile {
	lower := strings.ToLower(userAgent)
	for _, p := range knownAgentProfiles {
		if strings.Contains(lower, p.UserAgentPattern) {
			return p
		}
	}
	return defaultProfile
}

// ---------------------------------------------------------------------------
// Transfer planning
// ---------------------------------------------------------------------------

// PlanUpload decides the transfer strategy and chunk count for an upload.
func PlanUpload(data []byte, userAgent string) TransferPlan {
	binary := IsBinary(data)
	size := len(data)
	profile := MatchAgentProfile(userAgent)

	plan := TransferPlan{
		TotalSize: size,
		IsBinary:  binary,
		AgentName: profile.Name,
	}

	if !binary && size <= smallFileThreshold {
		plan.Strategy = StrategyDirectTool
		plan.NumChunks = 1
		return plan
	}

	// Shell + base64 path.
	plan.Strategy = StrategyShellBase64
	plan.ChunkSize = profile.ChunkSizeBytes

	if size <= profile.ChunkSizeBytes {
		plan.NumChunks = 1
	} else {
		plan.NumChunks = int(math.Ceil(float64(size) / float64(profile.ChunkSizeBytes)))
	}
	return plan
}

// PlanDownload decides the transfer strategy and chunk count for a download.
// fileSize should be obtained from a probe command; if 0 or negative the plan
// assumes chunked shell transfer with a single probe-then-decide chunk.
func PlanDownload(fileSize int, userAgent string) TransferPlan {
	profile := MatchAgentProfile(userAgent)

	plan := TransferPlan{
		TotalSize: fileSize,
		AgentName: profile.Name,
	}

	if fileSize > 0 && fileSize <= smallFileThreshold {
		plan.Strategy = StrategyDirectTool
		plan.NumChunks = 1
		return plan
	}

	plan.Strategy = StrategyShellBase64
	plan.ChunkSize = profile.ChunkSizeBytes

	if fileSize <= 0 {
		// Unknown size; caller should probe first.
		plan.NumChunks = 0
		return plan
	}

	if fileSize <= profile.ChunkSizeBytes {
		plan.NumChunks = 1
	} else {
		plan.NumChunks = int(math.Ceil(float64(fileSize) / float64(profile.ChunkSizeBytes)))
	}
	return plan
}

// ---------------------------------------------------------------------------
// Chunk generation
// ---------------------------------------------------------------------------

// GenerateUploadChunks splits data into base64-encoded chunks and generates
// the shell commands. The first chunk creates the file (>), subsequent chunks
// append (>>).
func GenerateUploadChunks(data []byte, targetPath string, plan TransferPlan) []UploadChunk {
	if plan.Strategy == StrategyDirectTool || len(data) == 0 {
		return nil
	}

	chunkSize := plan.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultProfile.ChunkSizeBytes
	}

	var chunks []UploadChunk
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}

		b64 := base64.StdEncoding.EncodeToString(data[offset:end])
		redirect := ">>"
		if offset == 0 {
			redirect = ">"
		}

		cmd := fmt.Sprintf("echo '%s' | base64 -d %s '%s'", b64, redirect, targetPath)

		chunks = append(chunks, UploadChunk{
			Index:      len(chunks),
			Base64Data: b64,
			Command:    cmd,
		})
	}
	return chunks
}

// GenerateDownloadChunks generates shell commands that read sequential chunks
// of a file via dd+base64.
func GenerateDownloadChunks(filePath string, plan TransferPlan) []DownloadChunk {
	if plan.Strategy == StrategyDirectTool || plan.TotalSize <= 0 {
		return nil
	}

	chunkSize := plan.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultProfile.ChunkSizeBytes
	}

	var chunks []DownloadChunk
	for offset := 0; offset < plan.TotalSize; offset += chunkSize {
		size := chunkSize
		if offset+size > plan.TotalSize {
			size = plan.TotalSize - offset
		}

		cmd := fmt.Sprintf("dd if='%s' bs=1 skip=%d count=%d 2>/dev/null | base64", filePath, offset, size)

		chunks = append(chunks, DownloadChunk{
			Index:   len(chunks),
			Offset:  offset,
			Size:    size,
			Command: cmd,
		})
	}
	return chunks
}

// ---------------------------------------------------------------------------
// File size probe
// ---------------------------------------------------------------------------

// FileSizeProbeCommand returns a shell command that outputs the file size in bytes.
func FileSizeProbeCommand(filePath string) string {
	return fmt.Sprintf("stat -c%%s '%s' 2>/dev/null || wc -c < '%s'", filePath, filePath)
}

// ParseFileSizeOutput extracts a file size integer from probe command output.
// It strips shell metadata (exit codes, whitespace) and parses the first integer found.
func ParseFileSizeOutput(output string) (int, error) {
	// Strip common shell metadata.
	cleaned := stripShellMetadata(output)
	cleaned = strings.TrimSpace(cleaned)

	// Try to parse the first line as an integer.
	for _, line := range strings.Split(cleaned, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(line)
		if err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("no file size found in output: %q", output)
}

// ---------------------------------------------------------------------------
// Output decoding
// ---------------------------------------------------------------------------

// lineNumberRe matches line-number prefixes added by Read tools.
// Patterns: "     1\tcontent", "  1→content", "  1|content"
var lineNumberRe = regexp.MustCompile(`(?m)^\s*\d+[\t→|]\s?`)

// StripReadToolLineNumbers removes line-number prefixes from Read tool output.
func StripReadToolLineNumbers(output string) string {
	return lineNumberRe.ReplaceAllString(output, "")
}

// shellMetaRe matches common shell metadata lines.
var shellMetaRe = regexp.MustCompile(`(?im)^(exit\s*code:\s*\d+|wall\s*time:.*|output:)\s*$`)

// stripShellMetadata removes exit code, wall time, and "Output:" lines.
func stripShellMetadata(output string) string {
	lines := strings.Split(output, "\n")
	var cleaned []string
	outputSectionStarted := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Output:" {
			outputSectionStarted = true
			continue
		}
		if !outputSectionStarted && shellMetaRe.MatchString(trimmed) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

// DecodeBase64Output strips shell metadata and line-number prefixes from
// command output, then base64-decodes the result.
func DecodeBase64Output(output string) ([]byte, error) {
	// 1. Strip shell metadata (exit code, wall time, "Output:" header).
	cleaned := stripShellMetadata(output)
	// 2. Strip line-number prefixes.
	cleaned = StripReadToolLineNumbers(cleaned)
	// 3. Remove all whitespace (base64 may be wrapped at 76 chars).
	cleaned = strings.Join(strings.Fields(cleaned), "")

	if cleaned == "" {
		return []byte{}, nil
	}

	return base64.StdEncoding.DecodeString(cleaned)
}
