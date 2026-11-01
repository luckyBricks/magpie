package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Junie 3893.6 reads home AGENTS.md, but sends MCP references literally.
// A server needing expansion must be left out rather than receive a bad token.
func TestJunieLibrary(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "JUNIE_HOME"}[moved], func(t *testing.T) {
			h := sandbox(t)
			dir := filepath.Join(h, ".junie")
			if moved {
				dir = filepath.Join(h, "Junie Home")
				t.Setenv("JUNIE_HOME", dir)
			}
			write(t, filepath.Join(dir, "settings.json"), "{}\n")
			holdCLI(t, h, "junie")
			tg := targetByID("junie")
			if tg == nil || tg.Instructions != filepath.Join(dir, "AGENTS.md") || tg.Skills != filepath.Join(dir, "skills") || tg.MCP == nil || tg.MCP.Path != filepath.Join(dir, "mcp", "mcp.json") {
				t.Fatalf("Junie's Library target: %+v", tg)
			}
			shared := "Use the project's test command."
			ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"junie"}}))
			if text := read(t, tg.Instructions); !strings.Contains(text, shared) {
				t.Fatalf("home instructions: %s", text)
			}
			for _, s := range []Server{
				{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}},
				{Name: "fs", Transport: "stdio", Command: "server", Env: map[string]string{"TOKEN": "${TOKEN}"}},
			} {
				s.Agents = []string{"junie"}
				r, err := SaveServer("", s)
				if err != nil || len(r.Problems) != 1 || r.Problems[0].Agent != "junie" || r.Problems[0].Error != errNoEnvRef.Error() {
					t.Fatalf("%s references: %+v %v", s.Name, r, err)
				}
				ok(t)(RemoveServer(s.Name))
			}
			if b, err := os.ReadFile(tg.MCP.Path); err == nil && strings.Contains(string(b), "${") {
				t.Fatalf("Junie received an unexpanded reference: %s", b)
			}
		})
	}
}

// Upgrading removes a managed reference written using Cursor's syntax.
// The Library definition and a server written by the user are retained.
func TestJunieOldMCPReferences(t *testing.T) {
	h := sandbox(t)
	holdCLI(t, h, "junie")
	dir := filepath.Join(h, ".junie")
	write(t, filepath.Join(dir, "settings.json"), "{}\n")
	s := Server{Name: "managed", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}, Agents: []string{"junie"}}
	if _, err := SaveServer("", s); err != nil {
		t.Fatal(err)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.applied("junie").MCP = []string{"managed"}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "mcp", "mcp.json")
	write(t, p, `{"mcpServers":{"managed":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${env:TOKEN}"}},"mine":{"command":"server","env":{"TOKEN":"${TOKEN}"}}}}`)
	r, err := Sync()
	if err != nil || len(r.Problems) != 1 {
		t.Fatalf("sync: %+v %v", r, err)
	}
	text := read(t, p)
	if strings.Contains(text, "managed") || !strings.Contains(text, `"mine"`) || !strings.Contains(text, "${TOKEN}") {
		t.Fatalf("managed and user entries: %s", text)
	}
	l, err = load()
	if err != nil || l.server("managed") == nil || l.server("managed").Headers["Authorization"] != "Bearer ${TOKEN}" {
		t.Fatalf("Library definition changed: %+v %v", l, err)
	}
}
