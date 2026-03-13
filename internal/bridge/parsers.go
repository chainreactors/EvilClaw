package bridge

import (
	"encoding/csv"
	"strconv"
	"strings"
	"time"

	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
)

// parseNetstatOutput parses the text output of netstat/ss into a NetstatResponse.
func parseNetstatOutput(output, osName string) *implantpb.NetstatResponse {
	resp := &implantpb.NetstatResponse{}
	lines := strings.Split(output, "\n")

	if isWindows(osName) {
		// Windows: netstat -ano
		// Proto  Local Address          Foreign Address        State           PID
		// TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1234
		for _, line := range lines {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 4 {
				continue
			}
			proto := strings.ToLower(fields[0])
			if proto != "tcp" && proto != "udp" {
				continue
			}
			entry := &implantpb.SockTabEntry{
				Protocol:  strings.ToUpper(proto),
				LocalAddr: fields[1],
			}
			if proto == "tcp" && len(fields) >= 5 {
				entry.RemoteAddr = fields[2]
				entry.SkState = fields[3]
				entry.Pid = fields[4]
			} else if proto == "udp" {
				entry.RemoteAddr = fields[2]
				if len(fields) >= 5 {
					entry.SkState = fields[3]
					entry.Pid = fields[4]
				} else {
					entry.Pid = fields[len(fields)-1]
				}
			}
			resp.Socks = append(resp.Socks, entry)
		}
	} else {
		// Linux: ss -tulnp
		// State  Recv-Q  Send-Q  Local Address:Port  Peer Address:Port  Process
		// LISTEN 0       128     0.0.0.0:22          0.0.0.0:*          users:(("sshd",pid=1234,fd=3))
		for _, line := range lines {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 5 {
				continue
			}
			state := fields[0]
			if state == "State" || state == "Netid" {
				continue // skip header
			}

			// Determine protocol from first field or context
			proto := ""
			localAddr := ""
			peerAddr := ""
			pid := ""

			if state == "LISTEN" || state == "ESTAB" || state == "TIME-WAIT" ||
				state == "CLOSE-WAIT" || state == "SYN-SENT" || state == "SYN-RECV" ||
				state == "UNCONN" {
				// ss format: State Recv-Q Send-Q LocalAddr PeerAddr Process
				localAddr = fields[3]
				peerAddr = fields[4]
				// Try to detect protocol from the line
				proto = "TCP"
				if state == "UNCONN" {
					proto = "UDP"
				}
				if len(fields) >= 6 {
					pid = extractPidFromSS(fields[5])
				}
			} else if state == "tcp" || state == "tcp6" || state == "udp" || state == "udp6" {
				proto = strings.ToUpper(strings.TrimSuffix(state, "6"))
				// Distinguish ss-with-Netid from netstat-tulnp:
				//   ss:      tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:...
				//   netstat: tcp 0      0     0.0.0.0:22 0.0.0.0:* LISTEN 1234/sshd
				// In ss format, fields[1] is a state name; in netstat, it's a number.
				if isSSState(fields[1]) {
					// ss with Netid column.
					if len(fields) >= 6 {
						localAddr = fields[4]
						peerAddr = fields[5]
					}
					if len(fields) >= 7 {
						pid = extractPidFromSS(fields[6])
					}
					state = fields[1]
				} else {
					// netstat -tulnp: Proto Recv-Q Send-Q LocalAddr ForeignAddr State PID/Program
					if len(fields) >= 7 {
						localAddr = fields[3]
						peerAddr = fields[4]
						state = fields[5]
						pidProg := fields[6]
						if idx := strings.IndexByte(pidProg, '/'); idx > 0 {
							pid = pidProg[:idx]
						} else {
							pid = pidProg
						}
					} else if len(fields) >= 5 {
						// UDP lines in netstat may lack State column.
						localAddr = fields[3]
						peerAddr = fields[4]
						if len(fields) >= 6 {
							pidProg := fields[5]
							if idx := strings.IndexByte(pidProg, '/'); idx > 0 {
								pid = pidProg[:idx]
							} else {
								pid = pidProg
							}
						}
					}
				}
			} else {
				continue
			}

			resp.Socks = append(resp.Socks, &implantpb.SockTabEntry{
				Protocol:  proto,
				LocalAddr: localAddr,
				RemoteAddr: peerAddr,
				SkState:   state,
				Pid:       pid,
			})
		}
	}
	return resp
}

// extractPidFromSS extracts PID from ss process info like users:(("sshd",pid=1234,fd=3))
func extractPidFromSS(processInfo string) string {
	if idx := strings.Index(processInfo, "pid="); idx >= 0 {
		rest := processInfo[idx+4:]
		if end := strings.IndexAny(rest, ",)"); end > 0 {
			return rest[:end]
		}
		return rest
	}
	return ""
}

// isSSState returns true if s is a known ss State value (used to
// distinguish ss-with-Netid from netstat -tulnp when the first field
// is "tcp"/"udp").
func isSSState(s string) bool {
	switch s {
	case "LISTEN", "ESTAB", "TIME-WAIT", "CLOSE-WAIT",
		"SYN-SENT", "SYN-RECV", "FIN-WAIT-1", "FIN-WAIT-2",
		"CLOSING", "LAST-ACK", "UNCONN":
		return true
	}
	return false
}

// parsePsOutput parses the text output of tasklist/ps into a PsResponse.
func parsePsOutput(output, osName string) *implantpb.PsResponse {
	resp := &implantpb.PsResponse{}
	lines := strings.Split(output, "\n")

	if isWindows(osName) {
		// Windows: tasklist /FO CSV /NH
		// "System Idle Process","0","Services","0","8 K"
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "\"") {
				continue
			}
			r := csv.NewReader(strings.NewReader(line))
			record, err := r.Read()
			if err != nil || len(record) < 2 {
				continue
			}
			pid, _ := strconv.ParseUint(record[1], 10, 32)
			proc := &implantpb.Process{
				Name: record[0],
				Pid:  uint32(pid),
			}
			if len(record) >= 3 {
				proc.Owner = record[2]
			}
			resp.Processes = append(resp.Processes, proc)
		}
	} else {
		// Linux: ps -eo pid,ppid,user,comm,args --no-headers
		//     1     0 root     systemd         /sbin/init
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			pid, err := strconv.ParseUint(fields[0], 10, 32)
			if err != nil {
				continue // skip non-numeric (header)
			}
			ppid, _ := strconv.ParseUint(fields[1], 10, 32)
			proc := &implantpb.Process{
				Pid:   uint32(pid),
				Ppid:  uint32(ppid),
				Owner: fields[2],
				Name:  fields[3],
			}
			if len(fields) >= 5 {
				proc.Args = strings.Join(fields[4:], " ")
				proc.Path = fields[4]
			}
			resp.Processes = append(resp.Processes, proc)
		}
	}
	return resp
}

// parseLsOutput parses the text output of dir/ls into a LsResponse.
func parseLsOutput(output, osName, path string) *implantpb.LsResponse {
	resp := &implantpb.LsResponse{
		Path:   path,
		Exists: true,
	}
	lines := strings.Split(output, "\n")

	if isWindows(osName) {
		// Windows: dir /a "path"
		// 2026/03/12  22:00    <DIR>          subdir
		// 2026/03/12  21:59             1,234 file.txt
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Skip header/footer lines
			if strings.HasPrefix(line, "Volume ") || strings.HasPrefix(line, "Directory of ") ||
				strings.Contains(line, " File(s)") || strings.Contains(line, " Dir(s)") {
				continue
			}
			// Try to parse date-prefixed lines
			// Format: YYYY/MM/DD  HH:MM    <DIR>          name
			//         YYYY/MM/DD  HH:MM         1,234,567 name
			if len(line) < 20 {
				continue
			}
			// Check if line starts with a date pattern
			if !(line[4] == '/' || line[4] == '-' || line[2] == '/' || line[2] == '-') {
				continue
			}

			isDir := strings.Contains(line, "<DIR>")
			fi := &implantpb.FileInfo{
				IsDir: isDir,
			}

			if isDir {
				// Find <DIR> and extract name after it
				idx := strings.Index(line, "<DIR>")
				if idx >= 0 {
					fi.Name = strings.TrimSpace(line[idx+5:])
				}
			} else {
				// Find the size and name: everything after the date+time, before the name
				// Split by multiple spaces to find size and name
				parts := strings.Fields(line)
				if len(parts) >= 4 {
					// Last part is the filename, second-to-last is the size
					fi.Name = parts[len(parts)-1]
					sizeStr := strings.ReplaceAll(parts[len(parts)-2], ",", "")
					sizeStr = strings.ReplaceAll(sizeStr, ".", "")
					size, _ := strconv.ParseUint(sizeStr, 10, 64)
					fi.Size = size
				}
			}

			if fi.Name == "." || fi.Name == ".." || fi.Name == "" {
				continue
			}

			// Try to parse modification time from first two fields
			if parts := strings.Fields(line); len(parts) >= 2 {
				t, err := time.Parse("2006/01/02 15:04", parts[0]+" "+parts[1])
				if err == nil {
					fi.ModTime = t.Unix()
				}
			}

			resp.Files = append(resp.Files, fi)
		}
	} else {
		// Linux: ls -la "path"
		// drwxr-xr-x 2 user group 4096 Mar 12 22:00 subdir
		// -rw-r--r-- 1 user group 1234 Mar 12 21:59 file.txt
		// lrwxrwxrwx 1 user group   11 Mar 12 22:00 link -> target
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "total ") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 8 {
				continue
			}
			modeStr := fields[0]
			if len(modeStr) < 10 {
				continue
			}
			// Validate mode string starts with a valid file type character.
			if !strings.ContainsRune("dlcbps-", rune(modeStr[0])) {
				continue
			}

			fi := &implantpb.FileInfo{
				IsDir: modeStr[0] == 'd',
			}

			// Parse mode string to numeric
			fi.Mode = parseLsMode(modeStr)

			// Parse size
			size, _ := strconv.ParseUint(fields[4], 10, 64)
			fi.Size = size

			// Parse time (fields 5,6,7 = "Mar 12 22:00" or "Mar 12 2025")
			timeStr := fields[5] + " " + fields[6] + " " + fields[7]
			if t, err := time.Parse("Jan 2 15:04", timeStr); err == nil {
				t = t.AddDate(time.Now().Year(), 0, 0)
				fi.ModTime = t.Unix()
			} else if t, err := time.Parse("Jan 2 2006", timeStr); err == nil {
				fi.ModTime = t.Unix()
			}

			// Name is everything from field 8 onwards
			name := strings.Join(fields[8:], " ")

			// Handle symlinks: name -> target
			if modeStr[0] == 'l' {
				if idx := strings.Index(name, " -> "); idx >= 0 {
					fi.Link = name[idx+4:]
					name = name[:idx]
				}
			}

			if name == "." || name == ".." {
				continue
			}
			fi.Name = name
			resp.Files = append(resp.Files, fi)
		}
	}

	if len(resp.Files) == 0 {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "not found") || strings.Contains(lower, "no such file") ||
			strings.Contains(lower, "cannot access") || strings.Contains(lower, "cannot find") {
			resp.Exists = false
		}
	}

	return resp
}

// parseLsMode converts a Unix mode string like "drwxr-xr-x" to a numeric mode.
func parseLsMode(s string) uint32 {
	if len(s) < 10 {
		return 0
	}
	var mode uint32
	// Owner
	if s[1] == 'r' {
		mode |= 0400
	}
	if s[2] == 'w' {
		mode |= 0200
	}
	if s[3] == 'x' || s[3] == 's' {
		mode |= 0100
	}
	// Group
	if s[4] == 'r' {
		mode |= 0040
	}
	if s[5] == 'w' {
		mode |= 0020
	}
	if s[6] == 'x' || s[6] == 's' {
		mode |= 0010
	}
	// Other
	if s[7] == 'r' {
		mode |= 0004
	}
	if s[8] == 'w' {
		mode |= 0002
	}
	if s[9] == 'x' || s[9] == 't' {
		mode |= 0001
	}
	return mode
}

// parseEnvOutput parses environment variable output into a Response with Kv map.
func parseEnvOutput(output string) *implantpb.Response {
	resp := &implantpb.Response{
		Output: output,
		Kv:     make(map[string]string),
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx > 0 {
			key := line[:idx]
			value := line[idx+1:]
			resp.Kv[key] = value
		}
	}
	return resp
}
