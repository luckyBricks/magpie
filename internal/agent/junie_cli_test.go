package agent

import (
	"context"
	"encoding/json"
	"io"
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
// again. It is run once per API junie can be asked on (junieAPI): the
// vendor speaks that API natively, so the model's profile is written with
// that apiType and gateway path, and junie's request is the one the vendor
// reads. Everything it reads and writes is in the test's folders; no
// JetBrains or vendor account is used. MAGPIE_TEST_JUNIE_BIN is the junie
// launcher, e.g. ~/.local/share/junie/versions/<v>/junie-app/bin/junie.
func TestJunieRealCLI(t *testing.T) {
	bin := os.Getenv("MAGPIE_TEST_JUNIE_BIN")
	if bin == "" {
		t.Skip("set MAGPIE_TEST_JUNIE_BIN to a junie binary")
	}
	for _, kind := range []string{"chat", "responses", "anthropic"} {
		t.Run(kind, func(t *testing.T) { junieRealRun(t, bin, kind) })
	}
}

// junieFixture is a vendor on one API: how it reads a request, and how it
// answers one with text or a tool call.
type junieFixture struct {
	provider  func(url string) provider.Provider
	ref       string // the catalog ref junie is wired to
	model     string // what the vendor is asked for
	apiType   string // what the profile says
	path      string // the gateway path it is sent to
	read      func(body []byte) (model string, tools, back bool, err error)
	reply     func(text, tool string, args map[string]string) any
	authorize func(r *http.Request) bool
}

func junieFixtures() map[string]junieFixture {
	secret := "upstream-secret"
	args := func(a map[string]string) string { b, _ := json.Marshal(a); return string(b) }
	return map[string]junieFixture{
		"chat": {
			provider: func(u string) provider.Provider {
				return provider.Provider{ID: "a", Chat: u + "/v1", Key: secret, Models: []string{"pro", "flash"}}
			},
			ref: "magpie/a/pro", model: "pro", apiType: "OpenAICompletion", path: "/v1/chat/completions",
			authorize: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+secret },
			read: func(b []byte) (string, bool, bool, error) {
				var req struct {
					Model    string `json:"model"`
					Messages []struct {
						Role string `json:"role"`
					} `json:"messages"`
					Tools []json.RawMessage `json:"tools"`
				}
				err := json.Unmarshal(b, &req)
				back := false
				for _, m := range req.Messages {
					back = back || m.Role == "tool"
				}
				return req.Model, len(req.Tools) > 0, back, err
			},
			reply: func(text, tool string, a map[string]string) any {
				msg := map[string]any{"role": "assistant", "content": text}
				finish := "stop"
				if tool != "" {
					msg["content"] = nil
					msg["tool_calls"] = []any{map[string]any{"id": "call_" + tool, "type": "function",
						"function": map[string]any{"name": tool, "arguments": args(a)}}}
					finish = "tool_calls"
				}
				return map[string]any{"id": "junie-contract", "object": "chat.completion", "model": "pro",
					"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
					"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
			},
		},
		"responses": {
			provider: func(u string) provider.Provider {
				return provider.Provider{ID: "resp", Responses: u + "/v1", Key: secret, Models: []string{"gpt-6-sol"}}
			},
			ref: "magpie/resp/gpt-6-sol", model: "gpt-6-sol", apiType: "OpenAIResponses", path: "/v1/responses",
			authorize: func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+secret },
			read: func(b []byte) (string, bool, bool, error) {
				var req struct {
					Model string            `json:"model"`
					Input json.RawMessage   `json:"input"`
					Tools []json.RawMessage `json:"tools"`
				}
				err := json.Unmarshal(b, &req)
				return req.Model, len(req.Tools) > 0, strings.Contains(string(req.Input), `"function_call_output"`), err
			},
			reply: func(text, tool string, a map[string]string) any {
				out := []any{map[string]any{"type": "message", "id": "msg_1", "status": "completed", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}
				if tool != "" {
					out = []any{map[string]any{"type": "function_call", "id": "fc_" + tool, "call_id": "call_" + tool,
						"name": tool, "arguments": args(a), "status": "completed"}}
				}
				// the fields a real reply always has: junie's reader fails
				// on a missing one ('tools' first)
				return map[string]any{"id": "resp_contract", "object": "response", "created_at": 1, "status": "completed", "model": "gpt-6-sol",
					"output": out, "error": nil, "incomplete_details": nil, "instructions": nil, "metadata": map[string]any{},
					"parallel_tool_calls": false, "temperature": 1, "tool_choice": "auto", "tools": []any{}, "top_p": 1,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
			},
		},
		"anthropic": {
			provider: func(u string) provider.Provider {
				return provider.Provider{ID: "anth", Anthropic: u, Key: secret, Models: []string{"claude-sonnet-5"}}
			},
			ref: "magpie/anth/claude-sonnet-5", model: "claude-sonnet-5", apiType: "Anthropic", path: "/v1/messages",
			authorize: func(r *http.Request) bool {
				return r.Header.Get("x-api-key") == secret || r.Header.Get("Authorization") == "Bearer "+secret
			},
			read: func(b []byte) (string, bool, bool, error) {
				var req struct {
					Model    string `json:"model"`
					Messages []struct {
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
					Tools []json.RawMessage `json:"tools"`
				}
				err := json.Unmarshal(b, &req)
				back := false
				for _, m := range req.Messages {
					back = back || strings.Contains(string(m.Content), `"tool_result"`)
				}
				return req.Model, len(req.Tools) > 0, back, err
			},
			reply: func(text, tool string, a map[string]string) any {
				content := []any{map[string]any{"type": "text", "text": text}}
				stop := "end_turn"
				if tool != "" {
					var in map[string]any
					json.Unmarshal([]byte(args(a)), &in)
					content = []any{map[string]any{"type": "tool_use", "id": "toolu_" + tool, "name": tool, "input": in}}
					stop = "tool_use"
				}
				return map[string]any{"id": "msg_contract", "type": "message", "role": "assistant", "model": "claude-sonnet-5",
					"content": content, "stop_reason": stop, "stop_sequence": nil,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
			},
		},
	}
}

func junieRealRun(t *testing.T, bin, kind string) {
	fx := junieFixtures()[kind]
	home, conf, prof := junieSandbox(t)
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
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		model, tools, back, err := fx.read(b)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		paths = append(paths, r.URL.Path)
		if model != fx.model || !fx.authorize(r) {
			t.Errorf("wire contract: model=%q", model)
		}
		text, tool := "Say pong", ""
		args := map[string]string{}
		// a call with no tools is a helper's (the task's title, a step's
		// summary): text. With tools, create the file, then submit once
		// its result came back.
		if tools {
			toolCalls++
			if !back {
				tool, args = "create", map[string]string{"filename": pong, "content": "pong\n"}
			} else {
				text, tool = "", "submit"
				args = map[string]string{"solution_summary": "Wrote pong.txt.\n\n### Changes\n- Created `pong.txt`\n\n### Verification\n- It is there"}
				submitted = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fx.reply(text, tool, args))
	}))
	defer upstream.Close()
	if err := provider.Save(fx.provider(upstream.URL)); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))

	a := junie(home)
	if err := a.Apply("model", fx.ref); err != nil {
		t.Fatal(err)
	}
	// the profile names the API the vendor speaks, at its whole URL
	if p := junieJSON(t, prof); p["apiType"] != fx.apiType || p["baseUrl"] != gateway.URL()+fx.path {
		t.Fatalf("profile: %v", p)
	}
	run := func(env ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		cmd := proc.CommandContext(ctx, bin, "--skip-update-check", "--share-anonymous-statistics=false",
			"--output-format=json", "--max-steps=6", "--task", "Create pong.txt containing pong")
		cmd.Dir = work
		// Junie is Java: it takes its home from user.home, not HOME, and
		// writes instances/ there even with JUNIE_HOME set
		cmd.Env = append(append(os.Environ(), env...), "JUNIE_HOME="+jh,
			"JAVA_TOOL_OPTIONS="+strings.TrimSpace(os.Getenv("JAVA_TOOL_OPTIONS")+" -Duser.home="+home))
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
	// the vendor was asked on its own API: the gateway relayed it, and
	// didn't translate it to another
	for _, p := range paths {
		if p != fx.path {
			t.Errorf("the vendor was asked at %s, want %s", p, fx.path)
		}
	}
	mu.Unlock()
	if kind == "chat" {
		// a key the user moved to the environment is theirs: picking a model
		// leaves it, and junie, which reads it from its own, still gets in
		key := junieJSON(t, prof)["apiKey"].(string)
		b, _ := os.ReadFile(prof)
		writeFile(t, prof, strings.Replace(string(b), `"apiKey": "`+key+`"`, `"apiKey": "${MAGPIE_JUNIE_KEY}"`, 1))
		if err := a.Apply("model", fx.ref); err != nil || junieJSON(t, prof)["apiKey"] != "${MAGPIE_JUNIE_KEY}" || a.Check() != "" {
			t.Fatalf("the user's key reference: %v %q %v", err, a.Check(), junieJSON(t, prof)["apiKey"])
		}
		os.Remove(pong)
		mu.Lock()
		submitted, toolCalls = false, 0
		mu.Unlock()
		// the gateway wants no key on this machine, so only the variable's
		// absence shows that junie reads it: the profile doesn't load, and
		// junie says so and exits 0
		mu.Lock()
		unset := calls
		mu.Unlock()
		os.Unsetenv("MAGPIE_JUNIE_KEY")
		if out, _ := run(); strings.Contains(out, "pong.txt") || !strings.Contains(out, "Cannot find authorization") {
			t.Fatalf("junie with its key's variable unset:\n%s", out)
		}
		mu.Lock()
		if calls != unset {
			t.Errorf("junie asked the vendor with its key's variable unset (%d calls)", calls-unset)
		}
		mu.Unlock()
		if out, err := run("MAGPIE_JUNIE_KEY=" + key); err != nil || !strings.Contains(out, "pong.txt") {
			t.Fatalf("junie with its key in the environment: %v\n%s", err, out)
		}
		if b, err := os.ReadFile(pong); err != nil || string(b) != "pong\n" {
			t.Fatalf("the environment key's task wasn't run: %q %v", b, err)
		}
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	recent := s.Recent()
	if len(recent) == 0 {
		t.Fatal("the gateway saw no request")
	}
	for _, c := range recent {
		if c.Agent != "junie" || c.Provider != fx.provider("").ID || c.Status != http.StatusOK {
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
