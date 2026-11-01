package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func junieSandbox(t *testing.T) (home, conf, prof string) {
	home, _ = museHome(t)
	t.Setenv("JUNIE_HOME", "")
	conf = filepath.Join(home, ".junie", "settings.json")
	prof = filepath.Join(home, ".junie", "models", "magpie.json")
	return
}

func junieJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return m
}

func TestJunie(t *testing.T) {
	home, conf, prof := junieSandbox(t)
	os.MkdirAll(filepath.Dir(conf), 0o755)
	// Junie's own settings.json, as 3893.6 writes it, on a BYOK model
	orig := `{
    "ecoModePerModel": "{}",
    "subagentsMode": "Auto",
    "modelForLaunch": "sonnet",
    "providerForLaunch": "anthropic"
}
`
	os.WriteFile(conf, []byte(orig), 0o644)
	var want map[string]any
	json.Unmarshal([]byte(orig), &want)

	a := junie(home)
	if a.Path != conf {
		t.Fatalf("path: %s", a.Path)
	}
	f, small := a.Field("model"), a.Field("small")
	if f.Get() != "sonnet" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "sonnet magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}
	// the helper model is part of magpie's profile: nothing to set before
	if err := small.Set("magpie/deepseek/flash"); err == nil {
		t.Fatal("small set while not connected")
	}

	for range 2 { // twice: the user's model is stashed once
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	// the provider goes: a BYOK one keeps Junie from a custom model
	m := junieJSON(t, conf)
	if _, ok := m["providerForLaunch"]; ok || m["modelForLaunch"] != "custom:magpie" || m["subagentsMode"] != "Auto" || m["ecoModePerModel"] != "{}" {
		t.Fatalf("settings: %v", m)
	}
	p := junieJSON(t, prof)
	if p["id"] != "deepseek/pro" || p["baseUrl"] != gatewayV1()+"/chat/completions" || p["apiType"] != "OpenAICompletion" ||
		p["displayName"] != "magpie" || p["apiKey"] == "" || !reflect.DeepEqual(p["primaryModel"], map[string]any{"id": "deepseek/pro", "baseUrl": gatewayV1() + "/chat/completions", "apiType": "OpenAICompletion"}) {
		t.Fatalf("profile: %v", p)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("on: %q %q", f.Get(), a.Check())
	}

	// the helper model, and what the user added to the profile
	if err := small.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := small.Set("openai/gpt-5"); err == nil {
		t.Fatal("small took a model that isn't magpie's")
	}
	if small.Get() != "magpie/deepseek/flash" {
		t.Fatalf("small: %q", small.Get())
	}
	b, _ := os.ReadFile(prof)
	os.WriteFile(prof, []byte(strings.Replace(string(b), `"displayName"`, `"temperature": 0.3, "displayName"`, 1)), 0o644)
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	p = junieJSON(t, prof)
	if p["temperature"] != 0.3 || p["id"] != "deepseek/flash" ||
		!reflect.DeepEqual(p["fasterModel"], map[string]any{"id": "deepseek/flash", "baseUrl": gatewayV1() + "/chat/completions", "apiType": "OpenAICompletion"}) {
		t.Fatalf("kept: %v", p)
	}
	if err := small.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := junieJSON(t, prof)["fasterModel"]; ok || small.Get() != "" {
		t.Fatalf("small not cleared")
	}

	// the profile pointed elsewhere, and gone
	b, _ = os.ReadFile(prof)
	os.WriteFile(prof, []byte(strings.Replace(string(b), gatewayV1(), "http://elsewhere/v1", 1)), 0o644)
	if !strings.Contains(a.Check(), "http://elsewhere/v1") {
		t.Fatalf("check: %q", a.Check())
	}
	os.Remove(prof)
	if a.Check() == "" {
		t.Fatal("a missing profile is not drift")
	}
	os.MkdirAll(filepath.Dir(prof), 0o755)
	os.WriteFile(prof, b, 0o644)

	// a BYOK provider picked in Junie afterwards: not on magpie, and said
	b, _ = os.ReadFile(conf)
	os.WriteFile(conf, []byte(strings.Replace(string(b), `"modelForLaunch"`, `"providerForLaunch": "openai", "modelForLaunch"`, 1)), 0o644)
	if f.Get() == "magpie/deepseek/flash" || !strings.Contains(a.Check(), "openai provider") {
		t.Fatalf("provider: %q %q", f.Get(), a.Check())
	}
	os.WriteFile(conf, b, 0o644)

	// stepping out puts back what the user had, and takes the profile away
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := junieJSON(t, conf); !reflect.DeepEqual(got, want) {
		t.Fatalf("restore: %v", got)
	}
	if _, err := os.Stat(prof); !os.IsNotExist(err) {
		t.Fatal("profile left")
	}

	// one of the user's own picked from magpie: back to theirs, nothing left
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("opus"); err != nil {
		t.Fatal(err)
	}
	if got := junieJSON(t, conf); got["modelForLaunch"] != "opus" || got["providerForLaunch"] != "anthropic" || a.Wired() {
		t.Fatalf("own: %v", got)
	}
	if _, err := os.Stat(prof); !os.IsNotExist(err) {
		t.Fatal("profile left")
	}
}

// A models/magpie.json the user wrote is theirs: refused, never overwritten.
func TestJunieForeignProfile(t *testing.T) {
	home, _, prof := junieSandbox(t)
	os.MkdirAll(filepath.Dir(prof), 0o755)
	own := `{"id":"qwen3","baseUrl":"http://localhost:11434/v1/chat/completions","apiType":"OpenAICompletion"}`
	os.WriteFile(prof, []byte(own), 0o644)
	a := junie(home)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("overwrote the user's profile")
	}
	if b, _ := os.ReadFile(prof); string(b) != own {
		t.Fatalf("profile changed: %s", b)
	}
	if a.Field("model").Set("") != nil {
		t.Fatal("disconnect")
	}
	if b, _ := os.ReadFile(prof); string(b) != own {
		t.Fatalf("disconnect took the user's profile: %s", b)
	}
}

// A file magpie made goes again, and $JUNIE_HOME is the folder itself.
func TestJunieHomeAndNewFile(t *testing.T) {
	home, _, _ := junieSandbox(t)
	moved := filepath.Join(home, "elsewhere")
	t.Setenv("JUNIE_HOME", moved)
	a := junie(home)
	if a.Dir != moved || a.Path != filepath.Join(moved, "settings.json") {
		t.Fatalf("home: %s %s", a.Dir, a.Path)
	}
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if junieJSON(t, a.Path)["modelForLaunch"] != "custom:magpie" {
		t.Fatal("not set")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("made file left")
	}
}

// Each role is asked on the API its model's provider serves it on natively,
// at the gateway path for that API: Responses to /v1/responses, Anthropic's
// Messages to /v1/messages, the rest on Chat. The two roles may differ, and
// each carries its own baseUrl and apiType.
func TestJunieAsksEachModelNatively(t *testing.T) {
	home, _, prof := junieSandbox(t)
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-sol"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	a := junie(home)
	f, small := a.Field("model"), a.Field("small")
	role := func(r map[string]any) [2]string {
		t.Helper()
		return [2]string{r["apiType"].(string), r["baseUrl"].(string)}
	}
	for ref, want := range map[string][2]string{
		"deepseek/pro":         {"OpenAICompletion", gateway.URL() + "/v1/chat/completions"},
		"resp/gpt-6-sol":       {"OpenAIResponses", gateway.URL() + "/v1/responses"},
		"anth/claude-sonnet-5": {"Anthropic", gateway.URL() + "/v1/messages"},
	} {
		if err := f.Set("magpie/" + ref); err != nil {
			t.Fatal(err)
		}
		p := junieJSON(t, prof)
		// the profile's own are the primary model's: Junie needs both
		if got := role(p); got != want || role(p["primaryModel"].(map[string]any)) != want || a.Check() != "" {
			t.Fatalf("%s: %v, want %v (%q)\n%v", ref, got, want, a.Check(), p)
		}
	}

	// a Claude as the primary model and a DeepSeek as the helper: each on its own
	if err := small.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	p := junieJSON(t, prof)
	if got := role(p["fasterModel"].(map[string]any)); got != [2]string{"OpenAICompletion", gateway.URL() + "/v1/chat/completions"} ||
		role(p["primaryModel"].(map[string]any))[0] != "Anthropic" || a.Check() != "" {
		t.Fatalf("roles: %v (%q)", p, a.Check())
	}
	// picking the primary model again leaves the helper on its own API
	if err := f.Set("magpie/resp/gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	p = junieJSON(t, prof)
	if role(p["primaryModel"].(map[string]any))[0] != "OpenAIResponses" || role(p["fasterModel"].(map[string]any))[0] != "OpenAICompletion" ||
		p["fasterModel"].(map[string]any)["id"] != "deepseek/flash" {
		t.Fatalf("repick: %v", p)
	}

	// the helper pointed elsewhere is drift, though the primary model is fine
	b, _ := os.ReadFile(prof)
	moved := strings.Replace(string(b), gateway.URL()+"/v1/chat/completions", "http://elsewhere/v1/chat/completions", 1)
	if moved == string(b) {
		t.Fatalf("no helper address to move:\n%s", b)
	}
	os.WriteFile(prof, []byte(moved), 0o644)
	if !strings.Contains(a.Check(), "http://elsewhere/v1/chat/completions") {
		t.Fatalf("helper drift: %q", a.Check())
	}
	os.WriteFile(prof, b, 0o644)

	// a profile an older magpie wrote, the address on the profile alone and the
	// roles naming ids only, still reaches the gateway: not drift
	key := junieJSON(t, prof)["apiKey"].(string)
	old := `{"id":"deepseek/pro","baseUrl":"` + gateway.URL() + `/v1/chat/completions","apiType":"OpenAICompletion","apiKey":"` + key + `","displayName":"magpie","primaryModel":{"id":"deepseek/pro"},"fasterModel":{"id":"deepseek/flash"}}`
	os.WriteFile(prof, []byte(old), 0o644)
	if msg := a.Check(); msg != "" {
		t.Fatalf("an older profile: %q", msg)
	}
}

// A ${NAME} the user put in apiKey is theirs: Junie reads it from its own
// environment, so it is kept when a model is picked, and is not drift.
func TestJunieKeepsAnEnvironmentKey(t *testing.T) {
	home, _, prof := junieSandbox(t)
	a := junie(home)
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(prof)
	var m map[string]any
	json.Unmarshal(b, &m)
	os.WriteFile(prof, []byte(strings.Replace(string(b), `"apiKey": "`+m["apiKey"].(string)+`"`, `"apiKey": "${MAGPIE_KEY}"`, 1)), 0o644)
	if junieJSON(t, prof)["apiKey"] != "${MAGPIE_KEY}" {
		t.Fatalf("the test didn't set the reference:\n%s", b)
	}
	if msg := a.Check(); msg != "" {
		t.Fatalf("a reference is not drift: %q", msg)
	}
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if got := junieJSON(t, prof); got["apiKey"] != "${MAGPIE_KEY}" || got["id"] != "deepseek/flash" {
		t.Fatalf("repick overwrote the reference: %v", got)
	}
	// a literal the user typed over magpie's is the wiring, as before: put back
	os.WriteFile(prof, []byte(strings.Replace(mustRead(t, prof), `${MAGPIE_KEY}`, `their-own-key`, 1)), 0o644)
	if msg := a.Check(); msg == "" {
		t.Fatal("a different literal key is drift")
	}
}
