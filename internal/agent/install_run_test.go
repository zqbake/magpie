package agent

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installTools(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script package managers")
	}
	dir := t.TempDir()
	npmPrefixes = memo{}
	t.Cleanup(func() { npmPrefixes = memo{} })
	file(t, filepath.Join(dir, "npm"), "#!/bin/sh\nif [ \"$1\" = prefix ]; then printf '%s\\n' '"+dir+"'; else printf '%s\\n' \"$*\"; fi\n")
	file(t, filepath.Join(dir, "brew"), "#!/bin/sh\nprintf '%s\\n' \"$*\"\n")
	file(t, filepath.Join(dir, "node"), "#!/bin/sh\nprintf 'v24.0.0\\n'\n")
	t.Setenv("PATH", dir)
	return dir
}

func TestInstallChoicesMarkOnlyExecutableCommands(t *testing.T) {
	installTools(t)
	items := []Install{{ID: "gemini", Commands: []InstallCmd{
		{Via: "npm", Command: "npm install -g @google/gemini-cli"},
		{Via: "script", Command: "curl https://example.com/install | bash"},
	}}}
	installChoices(items)
	if !items[0].Commands[0].Runnable || items[0].Commands[1].Runnable {
		t.Fatalf("choices = %+v", items)
	}
}

func TestInstallCommandRunsOnlyAvailablePackageManagers(t *testing.T) {
	dir := installTools(t)
	for _, c := range []InstallCmd{
		{Via: "npm", Command: "npm install -g @google/gemini-cli"},
		{Via: "brew", Command: "brew install --cask codex"},
	} {
		u, err := installUpdater(c)
		if err != nil {
			t.Fatal(err)
		}
		if u.cmd[0] != filepath.Join(dir, c.Via) || strings.Join(u.cmd[1:], " ") != strings.TrimPrefix(c.Command, c.Via+" ") {
			t.Fatalf("argv = %q for %s", u.cmd, c.Command)
		}
		out, err := runUpdate(t.Context(), u)
		if err != nil || strings.TrimSpace(string(out)) != strings.Join(u.cmd[1:], " ") {
			t.Fatalf("package manager: %q, %v", out, err)
		}
	}
}

func TestInstallCommandRejectsUnsafeOrUnavailableCommands(t *testing.T) {
	installTools(t)
	for _, c := range []InstallCmd{
		{Via: "script", Command: "curl https://example.com/install | bash"},
		{Via: "powershell", Command: "irm https://example.com/install | iex"},
		{Via: "npm", Command: "npm install -g pi", Node: "nvm"},
		{Via: "npm", Command: "npm install -g pi", Admin: "/admin"},
		{Via: "npm", Command: "npm install -g pi; touch /tmp/injected"},
		{Via: "npm", Command: "npm install -g\npi"},
		{Via: "brew", Command: "brew uninstall codex"},
	} {
		if _, err := installUpdater(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := installUpdater(InstallCmd{Via: "brew", Command: "brew install gemini-cli"}); err == nil {
		t.Fatal("accepted unavailable brew")
	}
}

func TestInstallCommandRequiresKnownWritablePrefix(t *testing.T) {
	dir := installTools(t)
	oldWrite := canWrite
	t.Cleanup(func() { canWrite = oldWrite })
	canWrite = func(string) bool { return false }
	c := InstallCmd{Via: "npm", Command: "npm install -g @google/gemini-cli"}
	if _, err := installUpdater(c); err == nil {
		t.Fatal("accepted administrator-only prefix")
	}
	file(t, filepath.Join(dir, "npm"), "#!/bin/sh\nexit 1\n")
	npmPrefixes = memo{}
	if _, err := installUpdater(c); err == nil {
		t.Fatal("accepted unknown npm prefix")
	}
}

func TestInstallCommandReturnsDiagnosticsAndRejectsUnknownSelection(t *testing.T) {
	installTools(t)
	oldRun := runUpdate
	t.Cleanup(func() { runUpdate = oldRun })
	runUpdate = func(context.Context, *updater) ([]byte, error) {
		return []byte("npm error code ETARGET"), errors.New("exit status 1")
	}
	items := []Install{{ID: "gemini", Commands: []InstallCmd{{Via: "npm", Command: "npm install -g @google/gemini-cli"}}}}
	if err := installFrom(t.Context(), items, "gemini", "npm"); err == nil || !strings.Contains(err.Error(), "ETARGET") {
		t.Fatalf("error = %v, want npm diagnostic", err)
	}
	for _, choice := range [][2]string{{"unknown", "npm"}, {"gemini", "script"}, {"gemini", "npm;sh"}} {
		if err := installFrom(t.Context(), items, choice[0], choice[1]); err == nil || strings.Contains(err.Error(), "ETARGET") {
			t.Fatalf("unknown selection %v: %v", choice, err)
		}
	}
}

func TestInstallCommandRejectsConcurrentInstallAndStopsOnCancellation(t *testing.T) {
	installTools(t)
	oldRun := runUpdate
	t.Cleanup(func() { runUpdate = oldRun })
	entered, done := make(chan struct{}), make(chan error, 1)
	runUpdate = func(ctx context.Context, _ *updater) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	items := []Install{{ID: "gemini", Commands: []InstallCmd{{Via: "brew", Command: "brew install gemini-cli"}}}}
	go func() { done <- installFrom(ctx, items, "gemini", "brew") }()
	<-entered
	if err := installFrom(t.Context(), items, "gemini", "brew"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second install: %v", err)
	}
	cancel()
	if err := <-done; err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install: %v", err)
	}
	runUpdate = oldRun
	if err := installFrom(t.Context(), items, "gemini", "brew"); err != nil {
		t.Fatalf("cancellation left installation locked: %v", err)
	}
}

func TestInstallCommandTimeout(t *testing.T) {
	installTools(t)
	oldRun, oldTimeout := runUpdate, updateTimeout
	t.Cleanup(func() { runUpdate, updateTimeout = oldRun, oldTimeout })
	updateTimeout = 0
	runUpdate = func(ctx context.Context, _ *updater) ([]byte, error) {
		<-ctx.Done()
		return []byte("download incomplete"), ctx.Err()
	}
	items := []Install{{ID: "gemini", Commands: []InstallCmd{{Via: "brew", Command: "brew install gemini-cli"}}}}
	if err := installFrom(t.Context(), items, "gemini", "brew"); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "download incomplete") {
		t.Fatalf("timeout: %v", err)
	}
}
