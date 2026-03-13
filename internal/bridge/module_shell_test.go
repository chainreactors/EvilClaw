package bridge

import (
	"strings"
	"testing"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
)

// ---------------------------------------------------------------------------
// Command builder tests — verify each adapter generates correct OS commands
// ---------------------------------------------------------------------------

func TestNetstat_Command_Linux(t *testing.T) {
	cmd := netstatCommand("Linux", &implantpb.Request{})
	if !strings.Contains(cmd, "ss -tulnp") {
		t.Errorf("Linux netstat should use ss, got: %s", cmd)
	}
}

func TestNetstat_Command_Windows(t *testing.T) {
	cmd := netstatCommand("Windows", &implantpb.Request{})
	if cmd != "netstat -ano" {
		t.Errorf("Windows netstat: got %q, want %q", cmd, "netstat -ano")
	}
}

func TestPs_Command_Linux(t *testing.T) {
	cmd := psCommand("Linux", &implantpb.Request{})
	if !strings.Contains(cmd, "ps -eo") {
		t.Errorf("Linux ps should use ps -eo, got: %s", cmd)
	}
}

func TestPs_Command_Windows(t *testing.T) {
	cmd := psCommand("Windows", &implantpb.Request{})
	if !strings.Contains(cmd, "tasklist") {
		t.Errorf("Windows ps should use tasklist, got: %s", cmd)
	}
}

func TestLs_Command_Linux_DefaultPath(t *testing.T) {
	cmd := lsCommand("Linux", &implantpb.Request{})
	if !strings.Contains(cmd, `ls -la "."`) {
		t.Errorf("Linux ls default: got %q", cmd)
	}
}

func TestLs_Command_Linux_WithPath(t *testing.T) {
	cmd := lsCommand("Linux", &implantpb.Request{Args: []string{"/home"}})
	if !strings.Contains(cmd, "/home") {
		t.Errorf("Linux ls should include path, got: %s", cmd)
	}
}

func TestLs_Command_Windows(t *testing.T) {
	cmd := lsCommand("Windows", &implantpb.Request{Args: []string{"C:\\Users"}})
	if !strings.Contains(cmd, "dir /a") {
		t.Errorf("Windows ls should use dir, got: %s", cmd)
	}
}

func TestWhoami_Command(t *testing.T) {
	cmd := whoamiCommand("Linux", &implantpb.Request{})
	if cmd != "whoami" {
		t.Errorf("whoami: got %q", cmd)
	}
}

func TestPwd_Command_Linux(t *testing.T) {
	if cmd := pwdCommand("Linux", nil); cmd != "pwd" {
		t.Errorf("Linux pwd: got %q", cmd)
	}
}

func TestPwd_Command_Windows(t *testing.T) {
	if cmd := pwdCommand("Windows", nil); cmd != "cd" {
		t.Errorf("Windows pwd: got %q", cmd)
	}
}

func TestCat_Command_Linux(t *testing.T) {
	cmd := catCommand("Linux", &implantpb.Request{Args: []string{"/etc/passwd"}})
	if !strings.Contains(cmd, `cat "/etc/passwd"`) {
		t.Errorf("Linux cat: got %q", cmd)
	}
}

func TestCat_Command_Windows(t *testing.T) {
	cmd := catCommand("Windows", &implantpb.Request{Args: []string{"C:\\file.txt"}})
	if !strings.Contains(cmd, "type") {
		t.Errorf("Windows cat should use type, got: %q", cmd)
	}
}

func TestEnv_Command_Linux(t *testing.T) {
	if cmd := envCommand("Linux", nil); cmd != "env" {
		t.Errorf("Linux env: got %q", cmd)
	}
}

func TestEnv_Command_Windows(t *testing.T) {
	if cmd := envCommand("Windows", nil); cmd != "set" {
		t.Errorf("Windows env: got %q", cmd)
	}
}

func TestKill_Command_Linux(t *testing.T) {
	cmd := killCommand("Linux", &implantpb.Request{Args: []string{"1234"}})
	if cmd != "kill -9 1234" {
		t.Errorf("Linux kill: got %q", cmd)
	}
}

func TestKill_Command_Windows(t *testing.T) {
	cmd := killCommand("Windows", &implantpb.Request{Args: []string{"1234"}})
	if !strings.Contains(cmd, "taskkill") && !strings.Contains(cmd, "1234") {
		t.Errorf("Windows kill: got %q", cmd)
	}
}

func TestMkdir_Command_Linux(t *testing.T) {
	cmd := mkdirCommand("Linux", &implantpb.Request{Args: []string{"/tmp/test"}})
	if !strings.Contains(cmd, "mkdir -p") {
		t.Errorf("Linux mkdir: got %q", cmd)
	}
}

func TestRm_Command_Linux(t *testing.T) {
	cmd := rmCommand("Linux", &implantpb.Request{Args: []string{"/tmp/test"}})
	if !strings.Contains(cmd, "rm -rf") {
		t.Errorf("Linux rm: got %q", cmd)
	}
}

func TestCp_Command_Linux(t *testing.T) {
	cmd := cpCommand("Linux", &implantpb.Request{Args: []string{"/a", "/b"}})
	if !strings.Contains(cmd, `cp -r "/a" "/b"`) {
		t.Errorf("Linux cp: got %q", cmd)
	}
}

func TestMv_Command_Linux(t *testing.T) {
	cmd := mvCommand("Linux", &implantpb.Request{Args: []string{"/a", "/b"}})
	if !strings.Contains(cmd, `mv "/a" "/b"`) {
		t.Errorf("Linux mv: got %q", cmd)
	}
}

func TestCd_Command_Linux(t *testing.T) {
	cmd := cdCommand("Linux", &implantpb.Request{Args: []string{"/home"}})
	if !strings.Contains(cmd, `cd "/home" && pwd`) {
		t.Errorf("Linux cd: got %q", cmd)
	}
}

func TestCd_Command_Windows(t *testing.T) {
	cmd := cdCommand("Windows", &implantpb.Request{Args: []string{"C:\\Users"}})
	if !strings.Contains(cmd, `cd /d`) {
		t.Errorf("Windows cd: got %q", cmd)
	}
}

func TestChmod_Command_Linux(t *testing.T) {
	cmd := chmodCommand("Linux", &implantpb.Request{Args: []string{"755", "/usr/local/bin/app"}})
	if !strings.Contains(cmd, `chmod 755`) {
		t.Errorf("Linux chmod: got %q", cmd)
	}
}

func TestChmod_Command_Windows(t *testing.T) {
	cmd := chmodCommand("Windows", &implantpb.Request{Args: []string{"755", "C:\\app.exe"}})
	if !strings.Contains(cmd, "not supported") {
		t.Errorf("Windows chmod should say not supported, got: %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// Response builder tests
// ---------------------------------------------------------------------------

func TestTextResponse_TrimsWhitespace(t *testing.T) {
	spite := textResponse("  hello\n", "Linux", nil)
	resp := spite.GetResponse()
	if resp == nil {
		t.Fatal("expected Response body")
	}
	if resp.Output != "hello" {
		t.Errorf("output: got %q, want %q", resp.Output, "hello")
	}
}

func TestNetstatResponse_ParsesWindows(t *testing.T) {
	output := "TCP    0.0.0.0:135    0.0.0.0:0    LISTENING    1234\n"
	spite := netstatResponse(output, "Windows", nil)
	nr := spite.GetNetstatResponse()
	if nr == nil {
		t.Fatal("expected NetstatResponse body")
	}
	if len(nr.Socks) == 0 {
		t.Error("expected at least one socket entry")
	}
}

func TestPsResponse_ParsesLinux(t *testing.T) {
	output := "    1     0 root     systemd         /sbin/init\n"
	spite := psResponse(output, "Linux", nil)
	pr := spite.GetPsResponse()
	if pr == nil {
		t.Fatal("expected PsResponse body")
	}
	if len(pr.Processes) == 0 {
		t.Error("expected at least one process")
	}
}

func TestEnvResponse_Parses(t *testing.T) {
	output := "PATH=/usr/bin\nHOME=/root\n"
	spite := envResponse(output, "Linux", nil)
	resp := spite.GetResponse()
	if resp == nil {
		t.Fatal("expected Response body")
	}
	if resp.Kv["PATH"] != "/usr/bin" {
		t.Errorf("PATH: got %q", resp.Kv["PATH"])
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestFirstArg_FromArgs(t *testing.T) {
	req := &implantpb.Request{Args: []string{"a", "b"}}
	if got := firstArg(req); got != "a" {
		t.Errorf("firstArg from Args: got %q", got)
	}
}

func TestFirstArg_FromInput(t *testing.T) {
	req := &implantpb.Request{Input: "fallback"}
	if got := firstArg(req); got != "fallback" {
		t.Errorf("firstArg from Input: got %q", got)
	}
}

func TestFirstArg_Nil(t *testing.T) {
	if got := firstArg(nil); got != "" {
		t.Errorf("firstArg nil: got %q", got)
	}
}

func TestSecondArg_Adapter(t *testing.T) {
	req := &implantpb.Request{Args: []string{"a", "b"}}
	if got := secondArg(req); got != "b" {
		t.Errorf("secondArg: got %q", got)
	}
	if got := secondArg(&implantpb.Request{Args: []string{"a"}}); got != "" {
		t.Errorf("secondArg single: got %q", got)
	}
}

func TestIsWindows_Adapter(t *testing.T) {
	if !isWindows("Windows") {
		t.Error("Windows should be true")
	}
	if !isWindows("windows") {
		t.Error("windows (lowercase) should be true")
	}
	if isWindows("Linux") {
		t.Error("Linux should be false")
	}
}

// ---------------------------------------------------------------------------
// NewShellModule constructor test
// ---------------------------------------------------------------------------

func TestNewShellModule_Name(t *testing.T) {
	m := NewShellModule(consts.ModuleNetstat, consts.ModuleNetstat, netstatCommand, netstatResponse)
	if m.Name() != consts.ModuleNetstat {
		t.Errorf("Name: got %q, want %q", m.Name(), consts.ModuleNetstat)
	}
}
