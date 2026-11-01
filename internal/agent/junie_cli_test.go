package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// An opt-in check against a real junie: magpie wires it, it runs a task
// headless through the gateway to a fixture vendor, which drives its tool
// loop (create a file, then submit), and Disconnect takes it off magpie
// again. Everything it reads and writes is in the test's folders; no
// JetBrains or vendor account is used. MAGPIE_TEST_JUNIE_BIN is the junie
// launcher, e.g. ~/.local/share/junie/versions/<v>/junie-app/bin/junie.
func TestJunieRealCLI(t *testing.T) {
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

	var mu sync.Mutex
	var calls, toolCalls int
	var submitted bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
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
		if req.Model != "pro" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("wire contract: model=%q", req.Model)
		}
		msg := map[string]any{"role": "assistant", "content": "Say pong"}
		finish := "stop"
		call := func(name string, args map[string]string) {
			b, _ := json.Marshal(args)
			msg["tool_calls"] = []any{map[string]any{"id": "call_" + name, "type": "function",
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "junie-contract", "object": "chat.completion", "model": "pro",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer upstream.Close()
	if err := provider.Save(provider.Provider{ID: "a", Chat: upstream.URL + "/v1", Key: "upstream-secret", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))

	a := junie(home)
	if err := a.Apply("model", "magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		cmd := proc.CommandContext(ctx, bin, "--skip-update-check", "--share-anonymous-statistics=false",
			"--output-format=json", "--max-steps=6", "--task", "Create pong.txt containing pong")
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "JUNIE_HOME="+jh)
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := run()
	if err != nil {
		t.Fatalf("junie: %v\n%s", err, out)
	}
	var res struct {
		Result string   `json:"result"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &res); err != nil || len(res.Errors) > 0 || !strings.Contains(res.Result, "pong.txt") {
		t.Fatalf("junie's result: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(pong); err != nil || string(b) != "pong\n" {
		t.Fatalf("the tool call wasn't run: %q %v", b, err)
	}
	mu.Lock()
	if toolCalls < 2 || !submitted {
		t.Errorf("tool round trip: calls=%d with tools=%d submitted=%v", calls, toolCalls, submitted)
	}
	before := calls
	mu.Unlock()
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
	out, _ = run()
	mu.Lock()
	defer mu.Unlock()
	if calls != before {
		t.Errorf("junie still reached magpie after Disconnect (%d more calls):\n%s", calls-before, out)
	}
}
