package bridge

import (
	"fmt"
	"strings"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
)

// ---------------------------------------------------------------------------
// Helper functions (shared by all shell adapters)
// ---------------------------------------------------------------------------

// firstArg returns the first argument from the request, or empty string.
func firstArg(req *implantpb.Request) string {
	if req != nil && len(req.Args) > 0 {
		return req.Args[0]
	}
	if req != nil && req.Input != "" {
		return req.Input
	}
	return ""
}

// secondArg returns the second argument from the request, or empty string.
func secondArg(req *implantpb.Request) string {
	if req != nil && len(req.Args) > 1 {
		return req.Args[1]
	}
	return ""
}

// isWindows returns true if osName indicates Windows.
func isWindows(osName string) bool {
	return strings.EqualFold(osName, "Windows")
}

// ---------------------------------------------------------------------------
// Shared response builders
// ---------------------------------------------------------------------------

// textResponse wraps plain text output into a Spite_Response.
func textResponse(output, osName string, req *implantpb.Request) *implantpb.Spite {
	return &implantpb.Spite{
		Name: "response",
		Body: &implantpb.Spite_Response{
			Response: &implantpb.Response{
				Output: strings.TrimSpace(output),
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Structured response modules
// ---------------------------------------------------------------------------

func netstatCommand(osName string, req *implantpb.Request) string {
	if isWindows(osName) {
		return "netstat -ano"
	}
	return "ss -tulnp 2>/dev/null || netstat -tulnp 2>/dev/null"
}

func netstatResponse(output, osName string, req *implantpb.Request) *implantpb.Spite {
	resp := parseNetstatOutput(output, osName)
	return &implantpb.Spite{
		Name: consts.ModuleNetstat,
		Body: &implantpb.Spite_NetstatResponse{NetstatResponse: resp},
	}
}

func psCommand(osName string, req *implantpb.Request) string {
	if isWindows(osName) {
		return "tasklist /FO CSV /NH"
	}
	return "ps -eo pid,ppid,user,comm,args --no-headers"
}

func psResponse(output, osName string, req *implantpb.Request) *implantpb.Spite {
	resp := parsePsOutput(output, osName)
	return &implantpb.Spite{
		Name: consts.ModulePs,
		Body: &implantpb.Spite_PsResponse{PsResponse: resp},
	}
}

func lsCommand(osName string, req *implantpb.Request) string {
	path := firstArg(req)
	if path == "" {
		path = "."
	}
	if isWindows(osName) {
		return fmt.Sprintf(`dir /a "%s"`, path)
	}
	return fmt.Sprintf(`ls -la "%s"`, path)
}

func lsResponse(output, osName string, req *implantpb.Request) *implantpb.Spite {
	path := firstArg(req)
	if path == "" {
		path = "."
	}
	resp := parseLsOutput(output, osName, path)
	return &implantpb.Spite{
		Name: consts.ModuleLs,
		Body: &implantpb.Spite_LsResponse{LsResponse: resp},
	}
}

// ---------------------------------------------------------------------------
// Simple text response modules
// ---------------------------------------------------------------------------

func whoamiCommand(osName string, req *implantpb.Request) string {
	return "whoami"
}

func pwdCommand(osName string, req *implantpb.Request) string {
	if isWindows(osName) {
		return "cd"
	}
	return "pwd"
}

func catCommand(osName string, req *implantpb.Request) string {
	path := firstArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`type "%s"`, path)
	}
	return fmt.Sprintf(`cat "%s"`, path)
}

func envCommand(osName string, req *implantpb.Request) string {
	if isWindows(osName) {
		return "set"
	}
	return "env"
}

func envResponse(output, osName string, req *implantpb.Request) *implantpb.Spite {
	resp := parseEnvOutput(output)
	return &implantpb.Spite{
		Name: "response",
		Body: &implantpb.Spite_Response{Response: resp},
	}
}

func cdCommand(osName string, req *implantpb.Request) string {
	path := firstArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`cd /d "%s" && cd`, path)
	}
	return fmt.Sprintf(`cd "%s" && pwd`, path)
}

// ---------------------------------------------------------------------------
// Simple exec modules (return ExecResponse, no custom parser)
// ---------------------------------------------------------------------------

func killCommand(osName string, req *implantpb.Request) string {
	pid := firstArg(req)
	if isWindows(osName) {
		return fmt.Sprintf("taskkill /F /PID %s", pid)
	}
	return fmt.Sprintf("kill -9 %s", pid)
}

func mkdirCommand(osName string, req *implantpb.Request) string {
	path := firstArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`mkdir "%s"`, path)
	}
	return fmt.Sprintf(`mkdir -p "%s"`, path)
}

func rmCommand(osName string, req *implantpb.Request) string {
	path := firstArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`del /f /q "%s" 2>nul & rmdir /s /q "%s" 2>nul`, path, path)
	}
	return fmt.Sprintf(`rm -rf "%s"`, path)
}

func cpCommand(osName string, req *implantpb.Request) string {
	src := firstArg(req)
	dst := secondArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`copy "%s" "%s"`, src, dst)
	}
	return fmt.Sprintf(`cp -r "%s" "%s"`, src, dst)
}

func mvCommand(osName string, req *implantpb.Request) string {
	src := firstArg(req)
	dst := secondArg(req)
	if isWindows(osName) {
		return fmt.Sprintf(`move "%s" "%s"`, src, dst)
	}
	return fmt.Sprintf(`mv "%s" "%s"`, src, dst)
}

func chmodCommand(osName string, req *implantpb.Request) string {
	mode := firstArg(req)
	path := secondArg(req)
	if isWindows(osName) {
		return "echo chmod not supported on Windows"
	}
	return fmt.Sprintf(`chmod %s "%s"`, mode, path)
}
