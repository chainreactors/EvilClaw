// localrpc_client calls the malice-network client's LocalRPC to execute C2 commands.
//
// Usage: go run . [flags] <session_id_or_prefix> <command>
//
// Flags:
//
//	-s, --stream   Force streaming mode (server-streaming RPC).
//	                Automatically enabled for commands like "tapping".
//
// Session ID resolution:
//   - Exact match: used directly
//   - Short prefix: resolved via "use <prefix>" which returns the full session ID
//
// Examples:
//
//	go run . 6f97a09fdc5c "ls"
//	go run . 019d09d7 "whoami"
//	go run . "" "session"
//	go run . --stream 019d09d7 "tapping"
//	go run . 019d09d7 "tapping"          # auto-detects streaming
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/chainreactors/IoM-go/proto/services/localrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// fullSessionIDRe extracts a 12-char hex or UUID session ID from "use" output.
// Matches: "Active session xxx (6f97a09fdc5c)" or "(019d09d7-14a3-7b01-9887-05b51a111d8f)"
var fullSessionIDRe = regexp.MustCompile(`\(([0-9a-f-]{12,36})\)`)

// streamingCommands are commands that produce persistent events and should use StreamCommand.
var streamingCommands = []string{"tapping", "chat"}

func main() {
	addr := "127.0.0.1:15004"

	// Parse flags
	args := os.Args[1:]
	streamFlag := false
	var positional []string
	for _, a := range args {
		switch a {
		case "-s", "--stream":
			streamFlag = true
		default:
			positional = append(positional, a)
		}
	}

	if len(positional) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-s|--stream] <session_id_or_prefix> <command>\n", os.Args[0])
		os.Exit(1)
	}
	sessionID := positional[0]
	command := strings.Join(positional[1:], " ")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel on Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted, stopping stream...")
		cancel()
	}()

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := localrpc.NewCommandServiceClient(conn)

	// Resolve session ID: "use <prefix>" returns output containing the full ID.
	if sessionID != "" {
		resolved := resolveSessionID(ctx, client, sessionID)
		if resolved == "" {
			fmt.Fprintf(os.Stderr, "failed to resolve session %q\n", sessionID)
			os.Exit(1)
		}
		if resolved != sessionID {
			fmt.Fprintf(os.Stderr, "resolved %q → %s\n", sessionID, resolved)
		}
		sessionID = resolved
	}

	// Decide whether to use streaming mode.
	if streamFlag || isStreamingCommand(command) {
		if err := streamAndPrint(ctx, client, sessionID, command); err != nil {
			fmt.Fprintf(os.Stderr, "stream error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Unary mode (original behavior).
	unaryCtx, unaryCancel := context.WithTimeout(ctx, 60*time.Second)
	defer unaryCancel()

	resp, err := client.ExecuteCommand(unaryCtx, &localrpc.ExecuteCommandRequest{
		SessionId: sessionID,
		Command:   command,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rpc error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "command failed: %s\n", resp.Error)
		fmt.Print(resp.Output)
		os.Exit(1)
	}

	fmt.Print(resp.Output)
}

// streamAndPrint calls StreamCommand and prints each chunk as it arrives.
func streamAndPrint(ctx context.Context, client localrpc.CommandServiceClient, sessionID, command string) error {
	sc, err := client.StreamCommand(ctx, &localrpc.ExecuteCommandRequest{
		SessionId: sessionID,
		Command:   command,
	})
	if err != nil {
		return fmt.Errorf("StreamCommand: %w", err)
	}

	for {
		resp, err := sc.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// Context cancelled is normal (Ctrl+C).
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}
		fmt.Print(resp.Output)
	}
}

// isStreamingCommand returns true if the command name matches a known streaming command.
func isStreamingCommand(command string) bool {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return false
	}
	cmd := strings.ToLower(parts[0])
	for _, sc := range streamingCommands {
		if cmd == sc {
			return true
		}
	}
	return false
}

// resolveSessionID sends "use <id>" to the C2 client and extracts the full session ID
// from the output (e.g. "Active session codex_exec (019d09d7-14a3-7b01-...)").
func resolveSessionID(ctx context.Context, client localrpc.CommandServiceClient, idOrPrefix string) string {
	rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.ExecuteCommand(rpcCtx, &localrpc.ExecuteCommandRequest{
		Command: "use " + idOrPrefix,
	})
	if err != nil {
		return ""
	}
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "use %s: %s\n", idOrPrefix, resp.Error)
		return ""
	}

	// Extract full session ID from output like "Active session xxx (FULL_ID)"
	if m := fullSessionIDRe.FindStringSubmatch(resp.Output); len(m) > 1 {
		return m[1]
	}

	// If no parenthesized ID found, the input might already be exact.
	return idOrPrefix
}
