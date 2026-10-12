package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
)

// cmdMCP implements "stai mcp": a Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0). It exposes stai's review capability to
// MCP clients such as Claude Code or Cursor. Everything the protocol does
// not own (progress, diagnostics) goes to the log file or stderr — never to
// stdout, which is reserved for protocol messages.
func cmdMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	fs.Parse(args)
	cfg := loadConfig()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel in-flight reviews when the client disconnects (stdin EOF
	// handled by serveMCP) or the process is asked to terminate.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	if err := serveMCP(cfg, ctx); err != nil {
		fatal(err)
	}
}

// mcpRequest is one JSON-RPC request or notification from the client.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpResponse is one JSON-RPC response to the client.
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serveMCP reads requests from stdin and writes responses to stdout until
// stdin closes. A failed stdout write (client gone, pipe closed) aborts the
// loop — there is no one left to answer. ctx is cancelled on stdin close or
// termination signal, unblocking any review in flight.
func serveMCP(cfg config.Config, ctx context.Context) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 16*1024*1024) // diffs can be large
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for in.Scan() {
			select {
			case lines <- in.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErr <- in.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			// Graceful shutdown on signal; cancel in-flight work.
			return nil
		case line, ok := <-lines:
			if !ok {
				return <-scanErr
			}
			if err := handleMCPLine(cfg, ctx, out, line); err != nil {
				return err
			}
		}
	}
}

func handleMCPLine(cfg config.Config, ctx context.Context, out *bufio.Writer, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var req mcpRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		logf("mcp: malformed line: %v", err)
		resp := mcpResponse{JSONRPC: "2.0", ID: nil, Error: &mcpError{Code: -32700, Message: "Parse error"}}
		return writeMCPResponse(out, resp)
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil // notifications expect no response
	}
	resp := handleMCP(cfg, ctx, req)
	return writeMCPResponse(out, resp)
}

func writeMCPResponse(out *bufio.Writer, resp mcpResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(mcpResponse{JSONRPC: "2.0", ID: resp.ID,
			Error: &mcpError{Code: -32603, Message: err.Error()}})
	}
	if _, err := out.Write(b); err != nil {
		return err
	}
	if err := out.WriteByte('\n'); err != nil {
		return err
	}
	return out.Flush()
}

// handleMCP dispatches one request to its handler.
func handleMCP(cfg config.Config, ctx context.Context, req mcpRequest) mcpResponse {
	switch req.Method {
	case "initialize":
		return okResp(req, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "stai", "version": version},
		})
	case "ping":
		return okResp(req, map[string]any{})
	case "tools/list":
		return okResp(req, map[string]any{"tools": []any{reviewDiffTool()}})
	case "tools/call":
		return callTool(cfg, ctx, req)
	default:
		return mcpResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &mcpError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

func okResp(req mcpRequest, result any) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// reviewDiffTool describes the single tool the server exposes.
func reviewDiffTool() map[string]any {
	return map[string]any{
		"name": "review_diff",
		"description": "Review a git diff with stai's AI review. " +
			"Pass the diff text directly, or a repo_dir to review its staged changes. " +
			"Returns {complete, error, findings[]}.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"diff": map[string]any{
					"type":        "string",
					"description": "Unified diff text to review. Optional when repo_dir is given.",
				},
				"repo_dir": map[string]any{
					"type":        "string",
					"description": "Repository path whose staged changes should be reviewed. Optional when diff is given.",
				},
			},
		},
	}
}

// callTool executes a tools/call request. Unknown tools and invalid
// arguments are reported as tool errors (isError: true), not transport
// errors.
func callTool(cfg config.Config, ctx context.Context, req mcpRequest) mcpResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return toolErrResp(req, err.Error())
	}
	if params.Name != "review_diff" {
		return toolErrResp(req, "unknown tool: "+params.Name)
	}
	var args struct {
		Diff    string `json:"diff"`
		RepoDir string `json:"repo_dir"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return toolErrResp(req, err.Error())
		}
	}

	diff := []byte(args.Diff)
	if len(bytes.TrimSpace(diff)) == 0 {
		var err error
		if args.RepoDir != "" {
			diff, err = git.StagedDiffDir(args.RepoDir)
		} else {
			diff, err = git.StagedDiff()
		}
		if err != nil {
			return toolErrResp(req, err.Error())
		}
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		return toolErrResp(req, "no diff to review: pass diff text or a repo_dir with staged changes")
	}

	findings, err := generateReview(ctx, cfg, diff, false, false, "")
	out := buildReviewJSONOutput(findings, err)
	b, jerr := json.MarshalIndent(out, "", "  ")
	if jerr != nil {
		return toolErrResp(req, jerr.Error())
	}
	return okResp(req, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(b)}},
		"isError": false,
	})
}

func toolErrResp(req mcpRequest, msg string) mcpResponse {
	return okResp(req, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	})
}

// buildReviewJSONOutput mirrors presentReviewJSON's schema without I/O so
// both the CLI and the MCP server share one shape.
func buildReviewJSONOutput(findings []ai.Finding, err error) reviewJSONOutput {
	out := reviewJSONOutput{Complete: err == nil, Findings: make([]reviewJSONFinding, 0, len(findings))}
	if err != nil {
		out.Error = err.Error()
	}
	for _, f := range findings {
		out.Findings = append(out.Findings, reviewJSONFinding{
			Severity:     f.Severity,
			Location:     f.Location,
			Message:      f.Message,
			SuggestedFix: f.SuggestedFix,
		})
	}
	return out
}
