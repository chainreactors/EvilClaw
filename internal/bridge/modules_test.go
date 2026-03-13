package bridge

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

// ---------------------------------------------------------------------------
// Realistic Windows & Linux command output fixtures
// ---------------------------------------------------------------------------

const windowsNetstatOutput = `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:53             0.0.0.0:0              LISTENING       13936
  TCP    0.0.0.0:80             0.0.0.0:0              LISTENING       48704
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1720
  TCP    0.0.0.0:445            0.0.0.0:0              LISTENING       4
  TCP    10.235.171.228:49440   4.145.79.82:443        ESTABLISHED     6136
  TCP    10.235.171.228:49732   188.253.121.196:43000  ESTABLISHED     13936
  TCP    [::]:80                [::]:0                 LISTENING       48704
  TCP    [::1]:5004             [::1]:53410            ESTABLISHED     48704
  UDP    0.0.0.0:53             *:*                                    13936
  UDP    0.0.0.0:500            *:*                                    3920
  UDP    0.0.0.0:3702           *:*                                    7016
  UDP    [::]:53                *:*                                    13936
`

const linuxSSOutput = `State  Recv-Q  Send-Q  Local Address:Port  Peer Address:Port  Process
LISTEN 0       128     0.0.0.0:22          0.0.0.0:*          users:(("sshd",pid=1234,fd=3))
LISTEN 0       511     0.0.0.0:80          0.0.0.0:*          users:(("nginx",pid=5678,fd=6))
ESTAB  0       0       10.0.0.1:43210      10.0.0.2:443       users:(("curl",pid=9999,fd=5))
UNCONN 0       0       0.0.0.0:53          0.0.0.0:*          users:(("named",pid=2222,fd=512))
`

const linuxNetstatFallbackOutput = `Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      1234/sshd
tcp6       0      0 :::80                   :::*                    LISTEN      5678/nginx
udp        0      0 0.0.0.0:53              0.0.0.0:*                           2222/named
`

const windowsTasklistOutput = `"System Idle Process","0","Services","0","8 K"
"System","4","Services","0","7,136 K"
"svchost.exe","1720","Services","0","18,652 K"
"csrss.exe","1280","Services","0","3,404 K"
"explorer.exe","12456","Console","1","120,800 K"
`

const linuxPsOutput = `    1     0 root     systemd         /sbin/init
   22     1 root     sshd            /usr/sbin/sshd -D
 1234    22 root     sshd            sshd: user@pts/0
 5678     1 www-data nginx           nginx: worker process
 9999  1234 john     vim             vim /etc/hosts
`

const windowsDirOutput = `Volume in drive D is Data
 Volume Serial Number is 36FA-B728

 D:\Programing\AI\CLIProxyAPI directory

2026/03/12  22:46    <DIR>          .
2026/03/11  15:11    <DIR>          ..
2026/03/11  15:11               487 .dockerignore
2026/03/11  15:11             1,706 .env.example
2026/03/12  23:02    <DIR>          .git
2026/03/11  15:11    <DIR>          .github
2026/03/11  15:11               525 .gitignore
2026/03/12  11:18        66,036,736 cliproxy.exe
2026/03/11  22:49    <DIR>          cmd
              3 File(s)         66,039,454 bytes
              5 Dir(s)  100,000,000,000 bytes free
`

const linuxLsOutput = `total 128
drwxr-xr-x 12 john john  4096 Mar 12 22:46 .
drwxr-xr-x  5 john john  4096 Mar 11 15:11 ..
-rw-r--r--  1 john john   487 Mar 11 15:11 .dockerignore
-rw-r--r--  1 john john  1706 Mar 11 15:11 .env.example
drwxr-xr-x  8 john john  4096 Mar 12 23:02 .git
drwxr-xr-x  3 john john  4096 Mar 11 15:11 .github
-rw-r--r--  1 john john   525 Mar 11 15:11 .gitignore
-rwxr-xr-x  1 john john 66036736 Mar 12 11:18 cliproxy.exe
drwxr-xr-x  3 john john  4096 Mar 11 22:49 cmd
lrwxrwxrwx  1 john john    11 Mar 12 22:00 link -> target
`

const windowsEnvOutput = `ALLUSERSPROFILE=C:\ProgramData
APPDATA=C:\Users\John\AppData\Roaming
COMPUTERNAME=DESKTOP-ABC
HOMEDRIVE=C:
HOMEPATH=\Users\John
NUMBER_OF_PROCESSORS=16
OS=Windows_NT
PROCESSOR_ARCHITECTURE=AMD64
USERNAME=John
`

const linuxEnvOutput = `HOME=/home/john
USER=john
SHELL=/bin/bash
PATH=/usr/local/bin:/usr/bin:/bin
LANG=en_US.UTF-8
TERM=xterm-256color
`

// Simulated tool output with metadata wrapper (as returned by LLM agent shell tools).
func wrapToolOutput(output string) string {
	return fmt.Sprintf("Exit code: 0\nWall time: 1.5 seconds\n\nOutput:\n%s", output)
}

// ===================================================================
// Parser unit tests — parseNetstatOutput
// ===================================================================

func TestParseNetstatOutput_Windows(t *testing.T) {
	t.Parallel()
	resp := parseNetstatOutput(windowsNetstatOutput, "Windows")

	if len(resp.Socks) == 0 {
		t.Fatal("expected netstat entries, got 0")
	}

	// Count TCP and UDP entries.
	var tcpCount, udpCount int
	for _, s := range resp.Socks {
		switch s.Protocol {
		case "TCP":
			tcpCount++
		case "UDP":
			udpCount++
		}
	}

	if tcpCount == 0 {
		t.Error("expected TCP entries")
	}
	if udpCount == 0 {
		t.Error("expected UDP entries")
	}

	// Verify a specific TCP LISTENING entry.
	found := false
	for _, s := range resp.Socks {
		if s.LocalAddr == "0.0.0.0:135" && s.SkState == "LISTENING" && s.Pid == "1720" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected TCP entry 0.0.0.0:135 LISTENING PID=1720")
	}

	// Verify a TCP ESTABLISHED entry.
	found = false
	for _, s := range resp.Socks {
		if s.LocalAddr == "10.235.171.228:49440" && s.RemoteAddr == "4.145.79.82:443" && s.SkState == "ESTABLISHED" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected TCP ESTABLISHED entry 10.235.171.228:49440 -> 4.145.79.82:443")
	}

	// Verify a UDP entry (no state column).
	found = false
	for _, s := range resp.Socks {
		if s.Protocol == "UDP" && s.LocalAddr == "0.0.0.0:53" && s.Pid == "13936" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected UDP entry 0.0.0.0:53 PID=13936")
	}

	// Verify IPv6 TCP entry.
	found = false
	for _, s := range resp.Socks {
		if s.LocalAddr == "[::]:80" && s.SkState == "LISTENING" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IPv6 TCP entry [::]:80 LISTENING")
	}
}

func TestParseNetstatOutput_LinuxSS(t *testing.T) {
	t.Parallel()
	resp := parseNetstatOutput(linuxSSOutput, "Linux")

	if len(resp.Socks) == 0 {
		t.Fatal("expected netstat entries, got 0")
	}

	// Verify LISTEN on port 22 with PID from ss process info.
	found := false
	for _, s := range resp.Socks {
		if s.LocalAddr == "0.0.0.0:22" && s.SkState == "LISTEN" && s.Pid == "1234" && s.Protocol == "TCP" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected LISTEN 0.0.0.0:22 PID=1234, got entries: %+v", resp.Socks)
	}

	// Verify ESTAB entry.
	found = false
	for _, s := range resp.Socks {
		if s.SkState == "ESTAB" && s.LocalAddr == "10.0.0.1:43210" && s.Pid == "9999" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ESTAB 10.0.0.1:43210 PID=9999")
	}

	// Verify UNCONN = UDP.
	found = false
	for _, s := range resp.Socks {
		if s.Protocol == "UDP" && s.LocalAddr == "0.0.0.0:53" && s.Pid == "2222" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected UDP (UNCONN) 0.0.0.0:53 PID=2222")
	}
}

func TestParseNetstatOutput_LinuxNetstatFallback(t *testing.T) {
	t.Parallel()
	resp := parseNetstatOutput(linuxNetstatFallbackOutput, "Linux")

	if len(resp.Socks) == 0 {
		t.Fatal("expected netstat entries, got 0")
	}

	// tcp 0.0.0.0:22 LISTEN 1234/sshd
	found := false
	for _, s := range resp.Socks {
		if s.Protocol == "TCP" && s.LocalAddr == "0.0.0.0:22" && s.SkState == "LISTEN" && s.Pid == "1234" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected TCP LISTEN 0.0.0.0:22 PID=1234, got: %+v", resp.Socks)
	}
}

// ===================================================================
// Parser unit tests — parsePsOutput
// ===================================================================

func TestParsePsOutput_Windows(t *testing.T) {
	t.Parallel()
	resp := parsePsOutput(windowsTasklistOutput, "Windows")

	if len(resp.Processes) != 5 {
		t.Fatalf("expected 5 processes, got %d", len(resp.Processes))
	}

	// Verify System process.
	p := resp.Processes[1]
	if p.Name != "System" || p.Pid != 4 {
		t.Errorf("expected System PID=4, got name=%q pid=%d", p.Name, p.Pid)
	}
	if p.Owner != "Services" {
		t.Errorf("expected owner=Services, got %q", p.Owner)
	}

	// Verify explorer.exe.
	p = resp.Processes[4]
	if p.Name != "explorer.exe" || p.Pid != 12456 {
		t.Errorf("expected explorer.exe PID=12456, got name=%q pid=%d", p.Name, p.Pid)
	}
}

func TestParsePsOutput_Linux(t *testing.T) {
	t.Parallel()
	resp := parsePsOutput(linuxPsOutput, "Linux")

	if len(resp.Processes) != 5 {
		t.Fatalf("expected 5 processes, got %d", len(resp.Processes))
	}

	// Verify systemd PID=1 PPID=0.
	p := resp.Processes[0]
	if p.Pid != 1 || p.Ppid != 0 || p.Name != "systemd" {
		t.Errorf("expected systemd PID=1 PPID=0, got name=%q pid=%d ppid=%d", p.Name, p.Pid, p.Ppid)
	}
	if p.Path != "/sbin/init" {
		t.Errorf("expected path=/sbin/init, got %q", p.Path)
	}

	// Verify vim process with args.
	p = resp.Processes[4]
	if p.Name != "vim" || p.Owner != "john" {
		t.Errorf("expected vim owner=john, got name=%q owner=%q", p.Name, p.Owner)
	}
	if !strings.Contains(p.Args, "/etc/hosts") {
		t.Errorf("expected args containing /etc/hosts, got %q", p.Args)
	}
}

// ===================================================================
// Parser unit tests — parseLsOutput
// ===================================================================

func TestParseLsOutput_Windows(t *testing.T) {
	t.Parallel()
	resp := parseLsOutput(windowsDirOutput, "Windows", "D:\\Programing\\AI\\CLIProxyAPI")

	if !resp.Exists {
		t.Error("expected Exists=true")
	}

	if len(resp.Files) == 0 {
		t.Fatal("expected files, got 0")
	}

	// Verify .dockerignore (file).
	found := false
	for _, f := range resp.Files {
		if f.Name == ".dockerignore" && !f.IsDir && f.Size == 487 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected .dockerignore file with size 487")
	}

	// Verify .git (directory).
	found = false
	for _, f := range resp.Files {
		if f.Name == ".git" && f.IsDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected .git directory")
	}

	// Verify cliproxy.exe (large file).
	found = false
	for _, f := range resp.Files {
		if f.Name == "cliproxy.exe" && f.Size == 66036736 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected cliproxy.exe with size 66036736, files: %+v", resp.Files)
	}

	// Ensure "." and ".." are excluded.
	for _, f := range resp.Files {
		if f.Name == "." || f.Name == ".." {
			t.Errorf("should not include %q", f.Name)
		}
	}
}

func TestParseLsOutput_Linux(t *testing.T) {
	t.Parallel()
	resp := parseLsOutput(linuxLsOutput, "Linux", "/home/john/project")

	if !resp.Exists {
		t.Error("expected Exists=true")
	}

	if len(resp.Files) == 0 {
		t.Fatal("expected files, got 0")
	}

	// Verify .dockerignore (regular file).
	found := false
	for _, f := range resp.Files {
		if f.Name == ".dockerignore" && !f.IsDir && f.Size == 487 {
			found = true
			if f.Mode == 0 {
				t.Error("expected non-zero mode for .dockerignore")
			}
			break
		}
	}
	if !found {
		t.Error("expected .dockerignore file with size 487")
	}

	// Verify .git (directory).
	found = false
	for _, f := range resp.Files {
		if f.Name == ".git" && f.IsDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected .git directory")
	}

	// Verify symlink: link -> target.
	found = false
	for _, f := range resp.Files {
		if f.Name == "link" && f.Link == "target" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected symlink 'link' -> 'target'")
	}

	// Verify cliproxy.exe is an executable.
	for _, f := range resp.Files {
		if f.Name == "cliproxy.exe" {
			if f.Mode&0100 == 0 {
				t.Error("expected execute permission on cliproxy.exe")
			}
			break
		}
	}

	// Ensure "." and ".." are excluded.
	for _, f := range resp.Files {
		if f.Name == "." || f.Name == ".." {
			t.Errorf("should not include %q", f.Name)
		}
	}
}

func TestParseLsOutput_NotFound(t *testing.T) {
	t.Parallel()
	resp := parseLsOutput("ls: cannot access '/nonexistent': No such file or directory", "Linux", "/nonexistent")
	if resp.Exists {
		t.Error("expected Exists=false for not-found path")
	}
}

// ===================================================================
// Parser unit tests — parseEnvOutput
// ===================================================================

func TestParseEnvOutput_Windows(t *testing.T) {
	t.Parallel()
	resp := parseEnvOutput(windowsEnvOutput)

	if len(resp.Kv) == 0 {
		t.Fatal("expected KV pairs, got 0")
	}

	if v := resp.Kv["COMPUTERNAME"]; v != "DESKTOP-ABC" {
		t.Errorf("expected COMPUTERNAME=DESKTOP-ABC, got %q", v)
	}
	if v := resp.Kv["USERNAME"]; v != "John" {
		t.Errorf("expected USERNAME=John, got %q", v)
	}
	if v := resp.Kv["HOMEDRIVE"]; v != "C:" {
		t.Errorf("expected HOMEDRIVE=C:, got %q", v)
	}
	// Values containing = should be handled (only split on first =).
	if v := resp.Kv["HOMEPATH"]; v != `\Users\John` {
		t.Errorf("expected HOMEPATH=\\Users\\John, got %q", v)
	}
}

func TestParseEnvOutput_Linux(t *testing.T) {
	t.Parallel()
	resp := parseEnvOutput(linuxEnvOutput)

	if v := resp.Kv["HOME"]; v != "/home/john" {
		t.Errorf("expected HOME=/home/john, got %q", v)
	}
	if v := resp.Kv["SHELL"]; v != "/bin/bash" {
		t.Errorf("expected SHELL=/bin/bash, got %q", v)
	}
	// PATH contains colons, should not break on them.
	if v := resp.Kv["PATH"]; v != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("expected PATH=/usr/local/bin:/usr/bin:/bin, got %q", v)
	}
}

// ===================================================================
// Parser unit tests — parseLsMode
// ===================================================================

func TestParseLsMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode string
		want uint32
	}{
		{"drwxr-xr-x", 0755},
		{"-rw-r--r--", 0644},
		{"-rwxr-xr-x", 0755},
		{"lrwxrwxrwx", 0777},
		{"-rw-------", 0600},
		{"-rwxrwxrwx", 0777},
		{"drwx------", 0700},
		{"-r--r--r--", 0444},
	}
	for _, tt := range tests {
		if got := parseLsMode(tt.mode); got != tt.want {
			t.Errorf("parseLsMode(%q) = %04o, want %04o", tt.mode, got, tt.want)
		}
	}
}

// ===================================================================
// Parser unit tests — extractPlainOutput
// ===================================================================

func TestExtractPlainOutput_WithMetadata(t *testing.T) {
	t.Parallel()
	raw := "Exit code: 0\nWall time: 1.5 seconds\n\nOutput:\ncodemonkey\\john\n"
	got := extractPlainOutput(raw)
	if !strings.Contains(got, "codemonkey\\john") {
		t.Errorf("expected output to contain 'codemonkey\\john', got %q", got)
	}
}

func TestExtractPlainOutput_PlainText(t *testing.T) {
	t.Parallel()
	raw := "codemonkey\\john\n"
	got := extractPlainOutput(raw)
	if got != raw {
		t.Errorf("expected raw passthrough, got %q", got)
	}
}

func TestExtractPlainOutput_NestedNetstat(t *testing.T) {
	t.Parallel()
	wrapped := wrapToolOutput(windowsNetstatOutput)
	got := extractPlainOutput(wrapped)

	// The extracted output should contain actual netstat data.
	if !strings.Contains(got, "TCP") {
		t.Error("expected extracted output to contain TCP entries")
	}
	if !strings.Contains(got, "0.0.0.0:135") {
		t.Error("expected extracted output to contain 0.0.0.0:135")
	}
}

// ===================================================================
// Helper function tests
// ===================================================================

func TestFirstArg(t *testing.T) {
	t.Parallel()
	if got := firstArg(nil); got != "" {
		t.Errorf("firstArg(nil) = %q, want empty", got)
	}
	if got := firstArg(&implantpb.Request{}); got != "" {
		t.Errorf("firstArg(empty) = %q, want empty", got)
	}
	if got := firstArg(&implantpb.Request{Args: []string{"hello"}}); got != "hello" {
		t.Errorf("firstArg = %q, want hello", got)
	}
	// Falls back to Input when Args is empty.
	if got := firstArg(&implantpb.Request{Input: "fallback"}); got != "fallback" {
		t.Errorf("firstArg(input fallback) = %q, want fallback", got)
	}
}

func TestSecondArg(t *testing.T) {
	t.Parallel()
	if got := secondArg(nil); got != "" {
		t.Errorf("secondArg(nil) = %q, want empty", got)
	}
	if got := secondArg(&implantpb.Request{Args: []string{"one"}}); got != "" {
		t.Errorf("secondArg(one arg) = %q, want empty", got)
	}
	if got := secondArg(&implantpb.Request{Args: []string{"one", "two"}}); got != "two" {
		t.Errorf("secondArg = %q, want two", got)
	}
}

func TestIsWindows(t *testing.T) {
	t.Parallel()
	if !isWindows("Windows") {
		t.Error("expected true for Windows")
	}
	if !isWindows("windows") {
		t.Error("expected true for windows (case insensitive)")
	}
	if isWindows("Linux") {
		t.Error("expected false for Linux")
	}
}

// ===================================================================
// E2E: Module command flow via Registry.Dispatch
// ===================================================================

func TestE2E_ShellModule_Netstat_Windows(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64) WindowsTerminal", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModuleNetstat,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 10, wrapToolOutput(windowsNetstatOutput), 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 10, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected at least 1 SpiteResponse")
	}

	last := sent[len(sent)-1]
	if last.Spite.Name != consts.ModuleNetstat {
		t.Errorf("expected spite name %q, got %q", consts.ModuleNetstat, last.Spite.Name)
	}
	nr := last.Spite.GetNetstatResponse()
	if nr == nil {
		t.Fatal("expected NetstatResponse body")
	}
	if len(nr.Socks) == 0 {
		t.Error("expected non-empty socks list")
	}
	if last.TaskId != 10 {
		t.Errorf("expected taskID=10, got %d", last.TaskId)
	}
}

func TestE2E_ShellModule_Ps_Linux(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "claude-code/1.0.33 (Linux 6.1.0; x86_64)", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModulePs,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 20, wrapToolOutput(linuxPsOutput), 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 20, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected SpiteResponse")
	}

	last := sent[len(sent)-1]
	pr := last.Spite.GetPsResponse()
	if pr == nil {
		t.Fatal("expected PsResponse body")
	}
	if len(pr.Processes) != 5 {
		t.Errorf("expected 5 processes, got %d", len(pr.Processes))
	}
	if last.TaskId != 20 {
		t.Errorf("expected taskID=20, got %d", last.TaskId)
	}
}

func TestE2E_ShellModule_Whoami(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64)", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModuleWhoami,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 40, "Exit code: 0\nOutput:\ncodemonkey\\john", 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 40, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected SpiteResponse")
	}

	last := sent[len(sent)-1]
	r := last.Spite.GetResponse()
	if r == nil {
		t.Fatal("expected Response body")
	}
	if !strings.Contains(r.Output, "codemonkey") {
		t.Errorf("expected output containing 'codemonkey', got %q", r.Output)
	}
}

func TestE2E_ShellModule_Env(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64)", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModuleEnv,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 50, wrapToolOutput(windowsEnvOutput), 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 50, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected SpiteResponse")
	}

	last := sent[len(sent)-1]
	r := last.Spite.GetResponse()
	if r == nil {
		t.Fatal("expected Response body")
	}
	if len(r.Kv) == 0 {
		t.Error("expected non-empty KV map")
	}
	if v := r.Kv["USERNAME"]; v != "John" {
		t.Errorf("expected USERNAME=John, got %q", v)
	}
}

// Test that modules with nil buildResponse fall back to ExecResponse in E2E.
func TestE2E_ShellModule_Kill_FallbackExecResponse(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "claude-code/1.0.33 (Linux 6.1.0; x86_64)", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModuleKill,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{Args: []string{"1234"}}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 60, "Exit code: 0\nOutput:\n", 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 60, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected SpiteResponse")
	}

	last := sent[len(sent)-1]
	// Should fall back to ExecResponse since kill has nil buildResponse.
	er := last.Spite.GetExecResponse()
	if er == nil {
		t.Fatal("expected ExecResponse body (fallback)")
	}
	if !er.End {
		t.Error("expected End=true")
	}
}

func TestE2E_ShellModule_Ls_Windows(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)
	defer swapGlobalManager(origGlobal)

	b, stream, sess := setupTestBridge(t, mgr, "codex_cli_rs/0.112.0 (Windows 10.0.26200; x86_64)", testClaudeTools)

	spite := &implantpb.Spite{
		Name: consts.ModuleLs,
		Body: &implantpb.Spite_Request{Request: &implantpb.Request{
			Args: []string{`D:\Programing\AI\CLIProxyAPI`},
		}},
	}

	b.taskManager.StartSessionListener(sess.ID)
	simulateToolResult(mgr, sess.ID, 30, wrapToolOutput(windowsDirOutput), 50*time.Millisecond)
	dispatchWithTimeout(t, b, sess.ID, 30, spite, 3*time.Second)

	sent := stream.getSent()
	if len(sent) == 0 {
		t.Fatal("expected SpiteResponse")
	}

	last := sent[len(sent)-1]
	lr := last.Spite.GetLsResponse()
	if lr == nil {
		t.Fatal("expected LsResponse body")
	}
	if !lr.Exists {
		t.Error("expected Exists=true")
	}
	if len(lr.Files) == 0 {
		t.Error("expected non-empty file list")
	}
}
