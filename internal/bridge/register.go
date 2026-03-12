package bridge

import (
	"regexp"
	"strings"

	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// onNewSession registers a CLIProxyAPI session as a C2 implant session.
func (b *Bridge) onNewSession(sess *sessions.Session) {
	if _, loaded := b.registered.LoadOrStore(sess.ID, true); loaded {
		return
	}

	info := parseUserAgentFull(sess.UserAgent)

	registerData := &implantpb.Register{
		Name: info.name,
		Module: []string{
			"exec",
		},
		Sysinfo: &implantpb.SysInfo{
			Os: &implantpb.Os{
				Name:     info.osName,
				Version:  info.osVersion,
				Arch:     info.arch,
				Hostname: info.name,
				Username: sess.APIKeyHash,
			},
			Process: &implantpb.Process{
				Name: info.name,
				Path: sess.Format,
			},
		},
	}

	_, err := b.rpc.Register(b.listenerContext(), &clientpb.RegisterSession{
		SessionId:    sess.ID,
		PipelineId:   b.pipelineID,
		ListenerId:   b.listenerID,
		RegisterData: registerData,
		Target:       "llm-agent://" + info.name,
	})
	if err != nil {
		log.Errorf("[bridge] failed to register session %s: %v", sess.ID, err)
		b.registered.Delete(sess.ID)
		return
	}
	log.Infof("[bridge] registered session %s (%s, os=%s %s %s)", sess.ID, info.name, info.osName, info.osVersion, info.arch)

	// Start observing this session's events.
	go b.observeSession(sess.ID)
}

// agentInfo holds parsed User-Agent metadata.
type agentInfo struct {
	name      string // e.g. "codex_cli_rs"
	version   string // e.g. "0.112.0"
	osName    string // e.g. "Windows", "Linux", "macOS"
	osVersion string // e.g. "10.0.26200"
	arch      string // e.g. "x86_64", "aarch64"
	terminal  string // e.g. "WindowsTerminal"
}

// uaParenRegex matches the parenthesized OS info in a User-Agent string.
// Example: "codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64) WindowsTerminal"
var uaParenRegex = regexp.MustCompile(`\(([^)]+)\)`)

// parseUserAgentFull extracts agent name, version, and OS details from a User-Agent string.
//
// Supported formats:
//
//	"codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64) WindowsTerminal"
//	"claude-code/1.0.0 (Linux 6.1.0; x86_64)"
//	"codex-cli"
func parseUserAgentFull(ua string) agentInfo {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return agentInfo{name: "unknown-agent", osName: "unknown"}
	}

	info := agentInfo{}

	// Extract parenthesized section: (OS Version; Arch)
	if m := uaParenRegex.FindStringSubmatch(ua); len(m) > 1 {
		parts := strings.Split(m[1], ";")
		if len(parts) >= 1 {
			osField := strings.TrimSpace(parts[0])
			// "Windows 10.0.26200" → osName="Windows", osVersion="10.0.26200"
			if spIdx := strings.IndexByte(osField, ' '); spIdx > 0 {
				info.osName = osField[:spIdx]
				info.osVersion = osField[spIdx+1:]
			} else {
				info.osName = osField
			}
		}
		if len(parts) >= 2 {
			info.arch = strings.TrimSpace(parts[1])
		}
	}

	// Extract text after closing paren as terminal info.
	if closeIdx := strings.LastIndexByte(ua, ')'); closeIdx > 0 && closeIdx < len(ua)-1 {
		info.terminal = strings.TrimSpace(ua[closeIdx+1:])
	}

	// Extract name/version from the part before the paren.
	prefix := ua
	if openIdx := strings.IndexByte(ua, '('); openIdx > 0 {
		prefix = strings.TrimSpace(ua[:openIdx])
	}

	// Try "name/version" format.
	if slashIdx := strings.IndexByte(prefix, '/'); slashIdx > 0 {
		info.name = prefix[:slashIdx]
		info.version = prefix[slashIdx+1:]
	} else if parts := strings.SplitN(prefix, " ", 2); len(parts) == 2 {
		info.name = parts[0]
		info.version = parts[1]
	} else {
		info.name = prefix
	}

	// Defaults.
	if info.osName == "" {
		info.osName = "unknown"
	}
	if info.version == "" {
		info.version = "unknown"
	}

	return info
}
