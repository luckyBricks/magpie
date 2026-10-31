package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func junieSandbox(t *testing.T) (home, conf, prof string) {
	home, _ = museHome(t)
	t.Setenv("JUNIE_HOME", "")
	conf = filepath.Join(home, ".junie", "config.json")
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
	orig := `{
  "model": "sonnet",
  "provider": "anthropic",
  "byok": {"anthropic": "sk-ant-x"},
  "hooks": {"SessionStart": []}
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
	m := junieJSON(t, conf)
	if m["model"] != "custom:magpie" || m["provider"] != "anthropic" || !reflect.DeepEqual(m["byok"], want["byok"]) {
		t.Fatalf("config: %v", m)
	}
	p := junieJSON(t, prof)
	if p["id"] != "deepseek/pro" || p["baseUrl"] != gatewayV1()+"/chat/completions" || p["apiType"] != "OpenAICompletion" ||
		p["displayName"] != "magpie" || p["apiKey"] == "" || !reflect.DeepEqual(p["primaryModel"], map[string]any{"id": "deepseek/pro"}) {
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
	os.WriteFile(prof, []byte(strings.Replace(string(b), `"apiType"`, `"temperature": 0.3, "apiType"`, 1)), 0o644)
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	p = junieJSON(t, prof)
	if p["temperature"] != 0.3 || p["id"] != "deepseek/flash" ||
		!reflect.DeepEqual(p["fasterModel"], map[string]any{"id": "deepseek/flash"}) {
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
	if got := junieJSON(t, conf); got["model"] != "opus" || a.Wired() {
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
	if a.Dir != moved || a.Path != filepath.Join(moved, "config.json") {
		t.Fatalf("home: %s %s", a.Dir, a.Path)
	}
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if junieJSON(t, a.Path)["model"] != "custom:magpie" {
		t.Fatal("not set")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("made file left")
	}
}
