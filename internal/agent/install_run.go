package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/proc"
)

var installPackage = regexp.MustCompile(`^(?:@[a-zA-Z0-9._-]+/)?[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var installing sync.Mutex

func installChoices(items []Install) {
	// This is only the page's hint. The server repeats the prerequisite check
	// when the user clicks, so a stale button fails safely if the environment
	// changed while the page was open.
	for i := range items {
		for j := range items[i].Commands {
			_, err := installUpdater(items[i].Commands[j])
			items[i].Commands[j].Runnable = err == nil
		}
	}
}

// installUpdater accepts only simple package-manager commands from the
// catalog. A missing prerequisite or an unknown npm prefix stays copy-only.
func installUpdater(c InstallCmd) (*updater, error) {
	if c.Node != "" || c.Admin != "" {
		return nil, fmt.Errorf("this command needs setup in a terminal first")
	}
	if strings.ContainsAny(c.Command, "\r\n\t") {
		return nil, fmt.Errorf("this install command contains invalid whitespace")
	}
	args := strings.Fields(c.Command)
	if len(args) < 3 || args[0] != c.Via || args[1] != "install" {
		return nil, fmt.Errorf("this install command is copy-only")
	}
	switch c.Via {
	case "npm":
		if len(args) != 4 || args[2] != "-g" || !installPackage.MatchString(args[3]) {
			return nil, fmt.Errorf("this npm command is copy-only")
		}
		if _, err := proc.Look("node"); err != nil {
			return nil, fmt.Errorf("Node.js is not available: %w", err)
		}
	case "brew":
		if !(len(args) == 3 || len(args) == 4 && args[2] == "--cask") || !installPackage.MatchString(args[len(args)-1]) {
			return nil, fmt.Errorf("this Homebrew command is copy-only")
		}
	default:
		return nil, fmt.Errorf("this install command is copy-only")
	}
	bin, err := proc.Look(c.Via)
	if err != nil {
		return nil, err
	}
	if c.Via == "npm" {
		prefix := npmGlobalPrefix()
		if prefix == "" || !canWrite(prefix) {
			return nil, fmt.Errorf("npm's global folder is unknown or needs administrator access; run the command in a terminal")
		}
		dirs := []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "lib", "node_modules")}
		if runtime.GOOS == "windows" {
			dirs = []string{filepath.Join(prefix, "node_modules")}
		}
		for _, dir := range dirs {
			if isDir(dir) && !canWrite(dir) {
				return nil, fmt.Errorf("npm's global folder %s needs administrator access; run the command in a terminal", dir)
			}
		}
	}
	return &updater{via: c.Via, path: filepath.Dir(bin), cmd: append([]string{bin}, args[1:]...)}, nil
}

func installFrom(ctx context.Context, items []Install, id, via string) error {
	if !installing.TryLock() {
		return fmt.Errorf("an agent installation is already running")
	}
	defer installing.Unlock()
	for _, item := range items {
		if item.ID != id {
			continue
		}
		for _, c := range item.Commands {
			if c.Via != via {
				continue
			}
			if c.Via == "npm" {
				if npm, err := proc.Look("npm"); err == nil {
					npmPrefixes.forget(npm) // npm config may have changed since the list was drawn
				}
			}
			u, err := installUpdater(c)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, updateTimeout)
			defer cancel()
			out, err := runUpdate(ctx, u)
			if ctx.Err() == nil {
				Rescan() // a failed package manager can still leave a CLI behind
			}
			if ctx.Err() != nil {
				return fmt.Errorf("%s: %s: %w", c.Command, lastLines(string(out), 3), ctx.Err())
			}
			if err != nil {
				return fmt.Errorf("%s: %s: %w", c.Command, lastLines(string(out), 3), err)
			}
			return nil
		}
	}
	return fmt.Errorf("this agent and installation method are not offered")
}

// InstallCLI installs an absent CLI using a method in the server's current
// catalog, then verifies the installed program rather than trusting exit 0.
func InstallCLI(ctx context.Context, id, via string) error {
	if err := installFrom(ctx, Installs(), id, via); err != nil {
		return err
	}
	a, err := Find(id)
	if err != nil {
		return err
	}
	bin, _ := a.cliBin()
	if bin == "" {
		return fmt.Errorf("the install command finished, but %s's CLI was not found; copy the command into a terminal", a.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, bin, "--version")
	cmd.Env = updateEnv(&updater{path: filepath.Dir(bin)})
	out, err := cmd.CombinedOutput()
	if err != nil || parseVersion(string(out)) == "" {
		return fmt.Errorf("%s was installed, but --version could not be verified: %s", a.Name, lastLines(string(out), 3))
	}
	return nil
}
