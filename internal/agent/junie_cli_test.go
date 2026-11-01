package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// An opt-in check against real Junie in batch, TUI and ACP: magpie wires
// it through the gateway to a fixture vendor, which drives its tool
// loop (create a file, then submit), and Disconnect takes it off magpie
// again. Everything it reads and writes is in the test's folders; no
// JetBrains or vendor account is used. MAGPIE_TEST_JUNIE_BIN is the junie
// launcher, e.g. ~/.local/share/junie/versions/<v>/junie-app/bin/junie.
func TestJunieRealCLI(t *testing.T) {
	modes := []string{"batch", "responses", "batch-effort", "responses-effort", "acp", "retry"}
	if junieInteractive != nil {
		modes = append(modes, "tui")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) { testJunieRealCLI(t, mode) })
	}
}

// The PTY check is available on Linux; the other modes run on every OS.
var junieInteractive func(*testing.T, *exec.Cmd) (string, error)

func testJunieRealCLI(t *testing.T, mode string) {
	bin := os.Getenv("MAGPIE_TEST_JUNIE_BIN")
	if bin == "" {
		t.Skip("set MAGPIE_TEST_JUNIE_BIN to a junie binary")
	}
	home, conf, _ := junieSandbox(t)
	jh := filepath.Join(home, ".junie")
	t.Setenv("JUNIE_HOME", jh)
	work := t.TempDir()
	pong := filepath.Join(work, "pong.txt")
	// the user is on a BYOK model, as Junie writes it: the provider has
	// to go for the custom model to be used at all
	writeFile(t, conf, `{"ecoModePerModel":"{}","subagentsMode":"Auto","modelForLaunch":"sonnet","providerForLaunch":"anthropic"}`)
	writeFile(t, filepath.Join(jh, "AGENTS.md"), "HOME_INSTRUCTIONS_SENTINEL\n")
	writeFile(t, filepath.Join(work, "AGENTS.md"), "PROJECT_INSTRUCTIONS_SENTINEL\n")

	var mu sync.Mutex
	var calls, toolCalls int
	var mcpCalls int
	var submitted bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid MCP request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			mcpCalls++
			mu.Unlock()
			if r.Header.Get("X-Dollar") != "${MAGPIE_JUNIE_TOKEN}" || r.Header.Get("X-Cursor") != "${env:MAGPIE_JUNIE_TOKEN}" {
				t.Error("Junie changed MCP interpolation behavior; review its Library target")
			}
			if len(req.ID) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			var result any = map[string]any{}
			if req.Method == "initialize" {
				result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
			} else if req.Method == "tools/list" {
				result = map[string]any{"tools": []any{}}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			return
		}
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		if (req.Model != "pro" && !(mode == "tui" && req.Model == "flash" && len(req.Tools) == 0)) || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("wire contract: model=%q", req.Model)
		}
		msg := map[string]any{"role": "assistant", "content": "Say pong"}
		finish := "stop"
		call := func(name string, args map[string]string) {
			b, _ := json.Marshal(args)
			msg["tool_calls"] = []any{map[string]any{"index": 0, "id": "call_" + name, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(b)}}}
			finish = "tool_calls"
		}
		// a call with no tools is a helper's (the task's title, a step's
		// summary): text. With tools, create the file, then submit once
		// its result came back.
		if len(req.Tools) > 0 {
			toolCalls++
			back := false
			for _, m := range req.Messages {
				back = back || m.Role == "tool"
			}
			if !back {
				call("create", map[string]string{"filename": pong, "content": "pong\n"})
			} else {
				msg["content"] = nil
				call("submit", map[string]string{"solution_summary": "Wrote pong.txt.\n\n### Changes\n- Created `pong.txt`\n\n### Verification\n- It is there"})
				submitted = true
			}
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, choice := range []any{
				map[string]any{"index": 0, "delta": msg, "finish_reason": nil},
				map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish},
			} {
				b, _ := json.Marshal(map[string]any{"id": "junie-contract", "object": "chat.completion.chunk", "model": "pro", "choices": []any{choice}})
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "junie-contract", "object": "chat.completion", "model": "pro",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer upstream.Close()
	if mode == "batch" {
		t.Setenv("MAGPIE_JUNIE_TOKEN", "fixture-token")
		b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"url": upstream.URL + "/mcp", "headers": map[string]string{"X-Dollar": "${MAGPIE_JUNIE_TOKEN}", "X-Cursor": "${env:MAGPIE_JUNIE_TOKEN}"}}}})
		writeFile(t, filepath.Join(jh, "mcp", "mcp.json"), string(b))
	}
	if err := provider.Save(provider.Provider{ID: "a", Chat: upstream.URL + "/v1", Key: "upstream-secret", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	handler := s.Handler()
	var requests []map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		var req map[string]any
		if err := json.Unmarshal(b, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ua := "junie-cli-batch:"
		if mode == "acp" {
			ua = "junie-cli-acp:"
		} else if mode == "tui" {
			ua = "junie-cli:"
		}
		if !strings.HasPrefix(r.UserAgent(), ua) {
			t.Errorf("User-Agent: %q", r.UserAgent())
		}
		mu.Lock()
		requests = append(requests, req)
		retry := mode == "retry" && len(requests) == 1
		mu.Unlock()
		if retry {
			http.Error(w, "fixture endpoint unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))

	a := junie(home)
	if err := a.Apply("model", "magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("small", "magpie/a/flash"); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(mode, "responses") {
		if err := edit.SetJSON(filepath.Join(jh, "models", "magpie.json"),
			edit.KV{Path: "apiType", Value: "OpenAIResponses"}, edit.KV{Path: "baseUrl", Value: gw.URL + "/v1/responses"}); err != nil {
			t.Fatal(err)
		}
	}
	run := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		args := []string{"--skip-update-check", "--share-anonymous-statistics=false", "--max-steps=6"}
		if mode == "acp" {
			args = append(args, "--acp=true")
		} else if mode == "tui" {
			args = append(args, "--brave")
		} else {
			args = append(args, "--output-format=json", "--task", "Create pong.txt containing pong")
		}
		if strings.HasSuffix(mode, "-effort") {
			args = append(args, "--model", junieCustom, "--effort", "high")
		}
		cmd := proc.CommandContext(ctx, bin, args...)
		cmd.Dir = work
		// Junie is Java: it takes its home from user.home, not HOME, and
		// writes instances/ there even with JUNIE_HOME set
		cmd.Env = append(os.Environ(), "JUNIE_HOME="+jh,
			"JAVA_TOOL_OPTIONS="+strings.TrimSpace(os.Getenv("JAVA_TOOL_OPTIONS")+` -Duser.home="`+home+`"`))
		// A developer's environment must not authenticate the disconnected
		// BYOK selection or override the model selected by magpie.
		for _, name := range []string{"JUNIE_API_KEY", "JUNIE_AUTH_TOKEN", "JUNIE_MODEL", "JUNIE_PROVIDER", "JUNIE_EFFORT",
			"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "GROK_API_KEY", "XAI_API_KEY", "META_API_KEY", "OPENROUTER_API_KEY", "LITELLM_API_KEY"} {
			cmd.Env = slices.DeleteFunc(cmd.Env, func(v string) bool { return strings.HasPrefix(v, name+"=") })
		}
		if mode == "acp" {
			return runJunieACP(t, cmd)
		}
		if mode == "tui" {
			return junieInteractive(t, cmd)
		}
		var out bytes.Buffer
		cmd.Stdout = &out
		tree, err := proc.StartTree(cmd)
		if err != nil {
			return "", err
		}
		err = tree.Wait()
		return out.String(), err
	}
	out, err := run()
	if err != nil {
		t.Fatalf("junie: %v\n%s", err, out)
	}
	var res struct {
		Result     string   `json:"result"`
		Errors     []string `json:"errors"`
		StopReason string   `json:"stopReason"`
	}
	i := strings.Index(out, "{")
	if i < 0 && mode != "tui" {
		t.Fatalf("Junie returned no JSON: %s", out)
	}
	if mode != "tui" {
		if err := json.Unmarshal([]byte(out[i:]), &res); err != nil || len(res.Errors) > 0 || (mode == "acp" && res.StopReason != "end_turn") || (mode != "acp" && !strings.Contains(res.Result, "pong.txt")) {
			t.Fatalf("junie's result: %v\n%s", err, out)
		}
	}
	if b, err := os.ReadFile(pong); err != nil || string(b) != "pong\n" {
		t.Fatalf("the tool call wasn't run: %q %v", b, err)
	}
	mu.Lock()
	if toolCalls < 2 || !submitted {
		t.Errorf("tool round trip: calls=%d with tools=%d submitted=%v", calls, toolCalls, submitted)
	}
	before := calls
	if mode == "batch" && mcpCalls == 0 {
		t.Error("Junie did not read its home MCP config")
	}
	var instructions bool
	var small bool
	for _, req := range requests {
		if (req["model"] != "a/pro" && !(mode == "tui" && req["model"] == "a/flash")) || req["stream"] != false || req["reasoning"] != nil || req["reasoning_effort"] != nil {
			t.Errorf("custom model request: model=%v stream=%v reasoning=%v effort=%v", req["model"], req["stream"], req["reasoning"], req["reasoning_effort"])
		}
		small = small || req["model"] == "a/flash"
		b, _ := json.Marshal(req)
		instructions = instructions || bytes.Contains(b, []byte("HOME_INSTRUCTIONS_SENTINEL")) && bytes.Contains(b, []byte("PROJECT_INSTRUCTIONS_SENTINEL"))
	}
	if mode == "tui" && !small {
		t.Error("TUI never used fasterModel")
	}
	if mode == "retry" && len(requests) <= calls {
		t.Error("the startup failure was not retried")
	}
	mu.Unlock()
	if !instructions {
		t.Error("home and project AGENTS.md did not reach the model")
	}
	if m := junieJSON(t, conf); m[junieLaunch] != junieCustom || m[junieProvider] != nil {
		t.Errorf("Junie overwrote the selected profile: %v", m)
	}
	recent := s.Recent()
	if len(recent) == 0 {
		t.Fatal("the gateway saw no request")
	}
	for _, c := range recent {
		if c.Agent != "junie" || c.Provider != "a" || c.Status != http.StatusOK {
			t.Errorf("gateway attribution: agent=%q provider=%q status=%d error=%q", c.Agent, c.Provider, c.Status, c.Error)
		}
	}

	// off magpie: back on the user's BYOK model, which has no key here, so
	// junie doesn't start, and nothing reaches the gateway
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m := junieJSON(t, conf); m["modelForLaunch"] != "sonnet" || m["providerForLaunch"] != "anthropic" {
		t.Fatalf("settings after Disconnect: %v", m)
	}
	if mode != "tui" {
		out, _ = run()
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != before {
		t.Errorf("junie still reached magpie after Disconnect (%d more calls):\n%s", calls-before, out)
	}
}

// ACP uses newline-delimited JSON-RPC. The fixture grants only the file edit
// in this sandbox; closing the process also ends its background helpers.
func runJunieACP(t *testing.T, cmd *exec.Cmd) (result string, runErr error) {
	t.Helper()
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	tree, err := proc.StartTree(cmd)
	if err != nil {
		return "", err
	}
	defer func() {
		in.Close()
		tree.Kill()
		tree.Wait()
		if runErr != nil {
			runErr = fmt.Errorf("%w\n%s", runErr, stderr.String())
		}
	}()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	send := func(v any) error { return json.NewEncoder(in).Encode(v) }
	id := 0
	rpc := func(method string, params any) (json.RawMessage, error) {
		id++
		if err := send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for scanner.Scan() {
			var msg struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
				Params struct {
					Options []struct {
						ID string `json:"optionId"`
					} `json:"options"`
				} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				return nil, err
			}
			if msg.Method == "session/request_permission" {
				if len(msg.Params.Options) == 0 {
					return nil, fmt.Errorf("ACP permission has no options")
				}
				if err := send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": msg.Params.Options[0].ID}}}); err != nil {
					return nil, err
				}
			} else if msg.ID == id && msg.Method == "" {
				if len(msg.Error) > 0 {
					return nil, fmt.Errorf("ACP %s: %s", method, msg.Error)
				}
				return msg.Result, nil
			}
		}
		return nil, fmt.Errorf("ACP %s ended: %v", method, scanner.Err())
	}
	if _, err := rpc("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "fixture", "version": "1"}}); err != nil {
		return "", err
	}
	b, err := rpc("session/new", map[string]any{"cwd": cmd.Dir, "mcpServers": []any{}})
	if err != nil {
		return "", err
	}
	var session struct {
		ID string `json:"sessionId"`
	}
	if err := json.Unmarshal(b, &session); err != nil || session.ID == "" {
		return string(b), fmt.Errorf("ACP session: %v", err)
	}
	b, err = rpc("session/prompt", map[string]any{"sessionId": session.ID, "prompt": []any{map[string]string{"type": "text", "text": "Create pong.txt containing pong"}}})
	return string(b), err
}
