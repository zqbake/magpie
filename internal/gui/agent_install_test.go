package gui

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

func TestAgentInstallOnlyRunsCatalogCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script npm fixture")
	}
	sandboxHome(t)
	dir := t.TempDir()
	npm := "#!/bin/sh\nif [ \"$1\" = prefix ]; then printf '%s\\n' '" + dir + "'; exit; fi\nprintf '%s\\n' \"$*\" > '" + filepath.Join(dir, "ran") + "'\nprintf '#!/bin/sh\\nprintf \"1.2.3\\\\n\"\\n' > '" + filepath.Join(dir, "pi") + "'\n/bin/chmod +x '" + filepath.Join(dir, "pi") + "'\n"
	for name, script := range map[string]string{"npm": npm, "node": "#!/bin/sh\nprintf 'v24.0.0\\n'\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	agent.Rescan()
	t.Cleanup(agent.Rescan)
	h := Handler(nil, nil)
	post := func(id, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("POST", "/api/agents/install/"+id, strings.NewReader(body)))
		return r
	}
	if r := post("pi", `{"via":"`+strings.Repeat("n", 4096)+`"}`); r.Code < 400 || !strings.Contains(r.Body.String(), "request body too large") {
		t.Fatalf("oversized body not bounded: %d %s", r.Code, r.Body)
	}
	for _, tc := range [][2]string{
		{"pi", `{"via":"npm","command":"touch injected"}`},
		{"pi", `{"via":"npm;sh"}`},
		{"unknown", `{"via":"npm"}`},
		{"claude", `{"via":"script"}`},
		{"pi", `{`},
	} {
		r := post(tc[0], tc[1])
		if r.Code < 400 {
			t.Fatalf("accepted %v: %d %s", tc, r.Code, r.Body)
		}
		if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
			t.Fatalf("invalid selection ran npm: %v", err)
		}
	}
	r := post("pi", `{"via":"npm"}`)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"id":"pi"`) {
		t.Fatalf("install: %d %s", r.Code, r.Body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ran")); err != nil || string(b) != "install -g @earendil-works/pi-coding-agent\n" {
		t.Fatalf("npm ran %q, %v", b, err)
	}
	if r := post("pi", `{"via":"npm"}`); r.Code < 400 {
		t.Fatalf("already installed agent accepted: %d %s", r.Code, r.Body)
	}
	// Exit zero from npm isn't proof: its CLI must exist and answer --version.
	for _, script := range []string{
		strings.Replace(npm, "1.2.3", "not-a-version", 1),
		"#!/bin/sh\nif [ \"$1\" = prefix ]; then printf '%s\\n' '" + dir + "'; fi\n",
	} {
		if err := os.Remove(filepath.Join(dir, "pi")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		agent.Rescan()
		r := post("pi", `{"via":"npm"}`)
		if r.Code < 400 || !strings.Contains(r.Body.String(), "CLI was not found") && !strings.Contains(r.Body.String(), "could not be verified") {
			t.Fatalf("unverified installation: %d %s", r.Code, r.Body)
		}
	}
}
