// Package pair detects whether the user is currently running a recognised
// AI-assisted development tool. The result drives VibeNet's --mode=auto
// behaviour: when paired, the miner runs; when not, it sleeps.
//
// Detection enumerates running process names via platform-native commands
// (tasklist on Windows, ps elsewhere) and matches them against a curated
// allowlist. The allowlist is overridable by a user file at
// ~/.vibenet/pairs.txt (one name per line, "#" comments allowed).
package pair

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultProcesses is the curated list of "vibe-coding" processes shipped
// with the binary. The match is case-insensitive on Windows and case-
// sensitive everywhere else (which mirrors the underlying filesystems).
//
// Windows names retain their .exe suffix because tasklist reports them that
// way; the matcher strips it before comparing.
func DefaultProcesses() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			"Cursor.exe",
			"Claude.exe", "claude.exe", // Claude desktop / CLI
			"Code.exe", // VS Code (heuristic — captures the entire IDE)
			"Windsurf.exe",
			"idea64.exe", "idea.exe", // JetBrains IDEA
			"pycharm64.exe", "pycharm.exe",
			"webstorm64.exe", "webstorm.exe",
			"goland64.exe", "goland.exe",
			"rustrover64.exe", "rustrover.exe",
			"aider.exe",
		}
	default:
		return []string{
			"cursor",
			"claude",
			"code",
			"windsurf",
			"idea",
			"pycharm",
			"webstorm",
			"goland",
			"rustrover",
			"aider",
		}
	}
}

// Detector polls the OS for running processes and reports whether any of
// its targets are present.
type Detector struct {
	targets []string // normalised: lower-cased on Windows, no .exe
}

// New constructs a Detector against the given list of process names.
// Names are normalised once at construction time so IsPaired is allocation-
// free in the steady state.
func New(processes []string) *Detector {
	d := &Detector{targets: make([]string, 0, len(processes))}
	seen := make(map[string]struct{}, len(processes))
	for _, p := range processes {
		n := normaliseName(p)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		d.targets = append(d.targets, n)
	}
	return d
}

// LoadUserOverrides returns the contents of ~/.vibenet/pairs.txt as a slice
// of process names, falling back to DefaultProcesses if the file does not
// exist. Lines starting with "#" are comments.
func LoadUserOverrides() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return DefaultProcesses()
	}
	path := filepath.Join(home, ".vibenet", "pairs.txt")
	f, err := os.Open(path)
	if err != nil {
		return DefaultProcesses()
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return DefaultProcesses()
	}
	return out
}

// IsPaired enumerates running processes and reports whether any target is
// present. The second return value is the subset of targets currently
// matched (for status display).
func (d *Detector) IsPaired() (bool, []string, error) {
	running, err := listProcesses()
	if err != nil {
		return false, nil, err
	}
	var matched []string
	for _, target := range d.targets {
		if _, ok := running[target]; ok {
			matched = append(matched, target)
		}
	}
	return len(matched) > 0, matched, nil
}

// listProcesses returns the set of currently running process names
// (normalised) on the host system.
func listProcesses() (map[string]struct{}, error) {
	switch runtime.GOOS {
	case "windows":
		return listWindowsProcesses()
	default:
		return listUnixProcesses()
	}
}

func listWindowsProcesses() (map[string]struct{}, error) {
	// tasklist /FO CSV /NH outputs one line per process:
	//   "Cursor.exe","12345","Console","1","45,000 K"
	cmd := exec.Command("tasklist.exe", "/FO", "CSV", "/NH")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tasklist: %w", err)
	}
	set := make(map[string]struct{}, 256)
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '"' {
			continue
		}
		// First CSV field, between the leading quote and the next ",".
		end := bytes.Index(line[1:], []byte(`","`))
		if end < 0 {
			continue
		}
		name := string(line[1 : 1+end])
		set[normaliseName(name)] = struct{}{}
	}
	return set, nil
}

func listUnixProcesses() (map[string]struct{}, error) {
	// `ps -e -o comm=` prints one process command per line.
	cmd := exec.Command("ps", "-e", "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	set := make(map[string]struct{}, 256)
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		name := string(bytes.TrimSpace(line))
		if name == "" {
			continue
		}
		set[normaliseName(name)] = struct{}{}
	}
	return set, nil
}

// normaliseName makes process names comparable across platforms: trim the
// trailing .exe on Windows and lower-case everything for case-insensitive
// matching there.
func normaliseName(s string) string {
	s = strings.TrimSpace(s)
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
		s = strings.TrimSuffix(s, ".exe")
	}
	return s
}
