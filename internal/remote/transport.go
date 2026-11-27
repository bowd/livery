package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bowd/livery/internal/model"
)

var SSHOpts = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=10",
	"-o", "ServerAliveCountMax=3",
}

type HostProbe struct {
	OK               bool     `json:"ok"`
	Error            string   `json:"error,omitempty"`
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	IsNixOS          bool     `json:"is_nixos"`
	Home             string   `json:"home"`
	Installed        bool     `json:"installed"`          // skillshare installed
	Bin              string   `json:"bin"`                // skillshare path
	Version          string   `json:"version"`            // skillshare version
	LivInstalled     bool     `json:"liv_installed"`      // liv installed
	LivBin           string   `json:"liv_bin"`            // liv path
	LivVersion       string   `json:"liv_version"`        // liv version
	ConfigPath       string   `json:"config_path"`
	ConfigExists     bool     `json:"config_exists"`
	HasBrew          bool     `json:"has_brew"`
	HasNix           bool     `json:"has_nix"`
	Harnesses        []string `json:"harnesses"`
	ExistingSkills   []string `json:"existing_skills"`
	ProbedAt         int64    `json:"probed_at"`
}

// ErrRemoteLivMissing is included when a reachable SSH host does not have `liv` installed yet.
const ErrRemoteLivMissing = "liv not installed on remote host"

// sshShCmd wraps a POSIX sh script as a single safely-quoted `sh -c '...'` command string
// compatible with bash, zsh, dash, AND fish login shells on the remote host!
func sshShCmd(script string) string {
	return "sh -c '" + strings.ReplaceAll(script, "'", `'"'"'`) + "'"
}

func cleanSSHStderr(s string) string {
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		tr := strings.TrimSpace(ln)
		if tr == "" || strings.Contains(tr, "setlocale") {
			continue
		}
		kept = append(kept, tr)
	}
	return strings.Join(kept, "; ")
}

func sshEnv() []string {
	return append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
}

// RunAgent invokes `liv _remote` in-process for local targets or over SSH for remote targets.
func RunAgent(t *model.Target, req AgentRequest, timeout time.Duration) (AgentResponse, error) {
	if t.Home != "" {
		req.Home = t.Home
	}
	if b := os.Getenv("LIVERY_SECRET_BACKEND"); b != "" {
		req.SecretBackend = b
	} else if b := os.Getenv("AGENT_LAUNCH_SECRET_BACKEND"); b != "" {
		req.SecretBackend = b
	}
	if t.IsLocal() {
		rawResp := HandleAgentRequest(req)
		// Normalize via JSON round-trip so local and remote AgentResponse.Result shapes are 100% identical.
		if b, err := json.Marshal(rawResp); err == nil {
			var norm AgentResponse
			if json.Unmarshal(b, &norm) == nil {
				return norm, nil
			}
		}
		return rawResp, nil
	}
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Locate `liv` on remote host across standard PATH + user bin + Nix profiles.
	// Uses NO single quotes inside the script and wraps cleanly via sshShCmd so fish & bash both parse identically!
	remoteBoot := `for b in liv "$HOME/.local/bin/liv" /opt/homebrew/bin/liv /usr/local/bin/liv "$HOME/.nix-profile/bin/liv" "/etc/profiles/per-user/$USER/bin/liv" /run/current-system/sw/bin/liv; do if [ -x "$b" ] || command -v "$b" >/dev/null 2>&1; then exec "$b" _remote; fi; done; echo "` + ErrRemoteLivMissing + `" >&2; exit 127`
	args := append(append([]string(nil), SSHOpts...), t.SSH, sshShCmd(remoteBoot))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = sshEnv()
	blob, _ := json.Marshal(req)
	cmd.Stdin = bytes.NewReader(blob)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	lines := strings.Split(stdout.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], AgentMark) {
			var resp AgentResponse
			if jerr := json.Unmarshal([]byte(lines[i][len(AgentMark):]), &resp); jerr != nil {
				return AgentResponse{}, fmt.Errorf("%s: invalid agent response: %w", t.ID, jerr)
			}
			return resp, nil
		}
	}
	errMsg := cleanSSHStderr(stderr.String())
	if strings.Contains(errMsg, ErrRemoteLivMissing) {
		return AgentResponse{}, fmt.Errorf("%s (run `liv env bootstrap %s`)", ErrRemoteLivMissing, t.ID)
	}
	if errMsg == "" && err != nil {
		errMsg = err.Error()
	}
	return AgentResponse{}, fmt.Errorf("%s: remote liv agent failed: %s", t.ID, errMsg)
}

// RsyncTree mirrors a local skill directory to a local or remote target path.
func RsyncTree(t *model.Target, srcDir, dstAbs string) error {
	src := strings.TrimRight(srcDir, "/") + "/"
	args := []string{"-a", "--delete", "--exclude", ".git/", "--exclude", "__pycache__/", "--exclude", "/skill.toml"}
	if t.IsLocal() {
		if err := os.MkdirAll(dstAbs, 0o755); err != nil {
			return err
		}
		args = append(args, src, dstAbs+"/")
	} else {
		args = append(args, "-e", "ssh "+strings.Join(SSHOpts, " "), src, t.SSH+":"+dstAbs+"/")
	}
	cmd := exec.Command("rsync", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: rsync %s -> %s: %s", t.ID, srcDir, dstAbs, strings.TrimSpace(string(out)))
	}
	return nil
}

// PullHostSkillshareConfig reads a target's live HOME and ~/.local/share/agent-config/skillshare/config.yaml
// (local or over SSH) so `liv pull` can sync host edits back into the central git repo.
func PullHostSkillshareConfig(t *model.Target, cfgRel string) (home string, rawYAML []byte, exists bool, err error) {
	if t.IsLocal() {
		home = t.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		abs := cfgRel
		if strings.HasPrefix(abs, "~/") {
			abs = filepath.Join(home, abs[2:])
		}
		b, rerr := os.ReadFile(abs)
		if os.IsNotExist(rerr) {
			return home, nil, false, nil
		}
		if rerr != nil {
			return home, nil, false, rerr
		}
		return home, b, true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relTrim := strings.TrimPrefix(cfgRel, "~/")
	script := fmt.Sprintf(`printf '__HOME__=%%s\n' "$HOME"; P="$HOME/%s"; if [ -f "$P" ]; then printf '__EXISTS__=1\n'; cat "$P"; else printf '__EXISTS__=0\n'; fi`, relTrim)
	args := append(append([]string(nil), SSHOpts...), t.SSH, sshShCmd(script))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = sshEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if rerr := cmd.Run(); rerr != nil {
		msg := cleanSSHStderr(stderr.String())
		if msg == "" {
			msg = rerr.Error()
		}
		return "", nil, false, fmt.Errorf("%s: pull config: %s", t.ID, msg)
	}
	text := stdout.String()
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "__HOME__=") || !strings.HasPrefix(lines[1], "__EXISTS__=") {
		return "", nil, false, fmt.Errorf("%s: unexpected pull output", t.ID)
	}
	home = strings.TrimPrefix(lines[0], "__HOME__=")
	exists = strings.TrimPrefix(lines[1], "__EXISTS__=") == "1"
	if exists && len(lines) == 3 {
		rawYAML = []byte(lines[2])
	}
	return home, rawYAML, exists, nil
}

// RsyncPullDir pulls a remote (or local) skills directory back into a local repo directory WITHOUT deleting
// unrelated skills from the shared repo library (unless deleteExtra is true).
func RsyncPullDir(t *model.Target, srcAbs, dstLocal string, dryRun, deleteExtra bool) ([]string, error) {
	if err := os.MkdirAll(dstLocal, 0o755); err != nil {
		return nil, err
	}
	args := []string{
		"-rlpgoD", "--checksum", "--omit-dir-times", "--itemize-changes",
		"--exclude", ".git/",
		"--exclude", "__pycache__/",
		"--exclude", ".DS_Store",
		"--exclude", ".trash/",
		"--exclude", ".skillshare-manifest.json",
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if deleteExtra {
		args = append(args, "--delete")
	}
	src := strings.TrimRight(srcAbs, "/") + "/"
	dst := strings.TrimRight(dstLocal, "/") + "/"
	if t.IsLocal() {
		if _, err := os.Stat(srcAbs); os.IsNotExist(err) {
			return nil, nil
		}
		args = append(args, src, dst)
	} else {
		args = append(args, "-e", "ssh "+strings.Join(SSHOpts, " "), t.SSH+":"+src, dst)
	}
	cmd := exec.Command("rsync", args...)
	cmd.Env = sshEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit 23/24 when remote skills dir doesn't exist yet is harmless
		if strings.Contains(string(out), "No such file or directory") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: rsync pull %s -> %s: %s", t.ID, srcAbs, dstLocal, strings.TrimSpace(string(out)))
	}
	var changed []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, ".d") {
			continue
		}
		changed = append(changed, ln)
	}
	return changed, nil
}

// CommitEnvSkillshareBase initializes (if needed) a standalone git repo inside the target's skillshare
// directory, commits the outward-synced state, and advances the `livery-base` tag to HEAD.
func CommitEnvSkillshareBase(t *model.Target, sroot, commitRef string) (string, error) {
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	rel := strings.TrimPrefix(strings.TrimRight(sroot, "/"), "~/")
	if commitRef == "" {
		commitRef = "sync"
	}
	script := fmt.Sprintf(`
set -eu
DIR="$HOME/%s"
mkdir -p "$DIR/skills"
if ! command -v git >/dev/null 2>&1; then
  printf '__BASE__=\n'
  exit 0
fi
cd "$DIR"
if [ ! -d .git ]; then
  git init -q
fi
git config user.name "livery"
git config user.email "livery@local"
if [ ! -f .gitignore ]; then
  printf '.DS_Store\n__pycache__/\n' > .gitignore
fi
git add -A
if ! git diff --cached --quiet; then
  git commit -q -m "livery outward sync (%s)"
elif ! git rev-parse --verify HEAD >/dev/null 2>&1; then
  git commit -q --allow-empty -m "livery outward sync (%s)"
fi
git tag -f livery-base HEAD >/dev/null
printf '__BASE__=%%s\n' "$(git rev-parse --short HEAD)"
`, rel, commitRef, commitRef)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.IsLocal() {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), sshEnv()...)
		if t.Home != "" {
			cmd.Env = append(cmd.Env, "HOME="+t.Home)
		} else if h, err := os.UserHomeDir(); err == nil && h != "" {
			cmd.Env = append(cmd.Env, "HOME="+h)
		}
	} else {
		args := append(append([]string(nil), SSHOpts...), t.SSH, "sh -s")
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
		cmd.Stdin = strings.NewReader(script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := cleanSSHStderr(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: git base snapshot failed: %s", t.ID, msg)
	}
	for _, ln := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(ln, "__BASE__=") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "__BASE__=")), nil
		}
	}
	return "", nil
}

type EnvGitSnapshot struct {
	Home        string   `json:"home"`
	HasGit      bool     `json:"has_git"`
	HasBase     bool     `json:"has_base"`
	BaseSHA     string   `json:"base_sha"`
	HeadSHA     string   `json:"head_sha"`
	DiffLines   []string `json:"diff_lines"`   // name-status vs livery-base
	Untracked   []string `json:"untracked"`    // untracked files
	BaseConfig  string   `json:"-"`            // git show livery-base:config.yaml
	LiveConfig  string   `json:"-"`            // live config.yaml on disk
}

// InspectEnvSkillshareGit inspects the target environment's standalone skillshare git repo against `livery-base`.
func RunHostShell(t *model.Target, script string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if t.IsLocal() {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), sshEnv()...)
		if t.Home != "" {
			cmd.Env = append(cmd.Env, "HOME="+t.Home)
		} else if h, err := os.UserHomeDir(); err == nil && h != "" {
			cmd.Env = append(cmd.Env, "HOME="+h)
		}
	} else {
		args := append(append([]string(nil), SSHOpts...), t.SSH, "sh -s")
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
		cmd.Stdin = strings.NewReader(script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := cleanSSHStderr(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", t.ID, msg)
	}
	return stdout.String(), nil
}

func InspectEnvSkillshareGit(t *model.Target, sroot string) (*EnvGitSnapshot, error) {
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	rel := strings.TrimPrefix(strings.TrimRight(sroot, "/"), "~/")
	script := fmt.Sprintf(`
set -u
DIR="$HOME/%s"
printf '__HOME__=%%s\n' "$HOME"
if [ ! -d "$DIR" ] || ! command -v git >/dev/null 2>&1 || [ ! -d "$DIR/.git" ]; then
  printf '__HAS_GIT__=0\n'
  exit 0
fi
cd "$DIR"
printf '__HAS_GIT__=1\n'
HEAD_SHA=$(git rev-parse --short HEAD 2>/dev/null || true)
BASE_SHA=$(git rev-parse --short livery-base 2>/dev/null || true)
printf '__HEAD__=%%s\n' "$HEAD_SHA"
printf '__BASE__=%%s\n' "$BASE_SHA"
printf '__DIFF_START__\n'
if [ -n "$BASE_SHA" ]; then
  git diff --name-status livery-base 2>/dev/null || true
fi
printf '__DIFF_END__\n'
printf '__UNTRACKED_START__\n'
git ls-files --others --exclude-standard 2>/dev/null || true
printf '__UNTRACKED_END__\n'
printf '__BASE_CFG_START__\n'
if [ -n "$BASE_SHA" ]; then
  git show livery-base:config.yaml 2>/dev/null || true
fi
printf '\n__BASE_CFG_END__\n'
printf '__LIVE_CFG_START__\n'
if [ -f config.yaml ]; then
  cat config.yaml
fi
printf '\n__LIVE_CFG_END__\n'
`, rel)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.IsLocal() {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), sshEnv()...)
		if t.Home != "" {
			cmd.Env = append(cmd.Env, "HOME="+t.Home)
		} else if h, err := os.UserHomeDir(); err == nil && h != "" {
			cmd.Env = append(cmd.Env, "HOME="+h)
		}
	} else {
		args := append(append([]string(nil), SSHOpts...), t.SSH, "sh -s")
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
		cmd.Stdin = strings.NewReader(script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := cleanSSHStderr(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: inspect env git: %s", t.ID, msg)
	}
	raw := stdout.String()
	snap := &EnvGitSnapshot{DiffLines: []string{}, Untracked: []string{}}
	extractBlock := func(startMark, endMark string) string {
		s := strings.Index(raw, startMark+"\n")
		if s == -1 {
			return ""
		}
		s += len(startMark) + 1
		e := strings.Index(raw[s:], endMark)
		if e == -1 {
			return ""
		}
		return strings.TrimSpace(raw[s : s+e])
	}
	for _, ln := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(ln, "__HOME__="):
			snap.Home = strings.TrimSpace(strings.TrimPrefix(ln, "__HOME__="))
		case strings.HasPrefix(ln, "__HAS_GIT__="):
			snap.HasGit = strings.TrimSpace(strings.TrimPrefix(ln, "__HAS_GIT__=")) == "1"
		case strings.HasPrefix(ln, "__HEAD__="):
			snap.HeadSHA = strings.TrimSpace(strings.TrimPrefix(ln, "__HEAD__="))
		case strings.HasPrefix(ln, "__BASE__="):
			snap.BaseSHA = strings.TrimSpace(strings.TrimPrefix(ln, "__BASE__="))
			snap.HasBase = snap.BaseSHA != ""
		}
	}
	if blk := extractBlock("__DIFF_START__", "__DIFF_END__"); blk != "" {
		for _, ln := range strings.Split(blk, "\n") {
			if tr := strings.TrimSpace(ln); tr != "" {
				snap.DiffLines = append(snap.DiffLines, tr)
			}
		}
	}
	if blk := extractBlock("__UNTRACKED_START__", "__UNTRACKED_END__"); blk != "" {
		for _, ln := range strings.Split(blk, "\n") {
			if tr := strings.TrimSpace(ln); tr != "" {
				snap.Untracked = append(snap.Untracked, tr)
			}
		}
	}
	snap.BaseConfig = extractBlock("__BASE_CFG_START__", "__BASE_CFG_END__")
	snap.LiveConfig = extractBlock("__LIVE_CFG_START__", "__LIVE_CFG_END__")
	return snap, nil
}

// Zero-prereq POSIX sh probe script — works on ANY macOS or NixOS/Linux host before `liv` or `skillshare` is installed!
const posixProbeScript = `
set -u
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
MACH=$(uname -m | tr '[:upper:]' '[:lower:]')
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) ARCH="$MACH" ;;
esac
NIXOS=0; [ -e /etc/NIXOS ] || [ -d /run/current-system ] && NIXOS=1
find_cmd() {
  name="$1"
  if command -v "$name" >/dev/null 2>&1; then command -v "$name"; return; fi
  for d in /opt/homebrew/bin /usr/local/bin "$HOME/.local/bin" "$HOME/.nix-profile/bin" "/etc/profiles/per-user/$USER/bin" /run/current-system/sw/bin; do
    if [ -x "$d/$name" ]; then printf '%s\n' "$d/$name"; return; fi
  done
}
SS_BIN=$(find_cmd skillshare || true)
SS_VER=""; [ -n "$SS_BIN" ] && SS_VER=$("$SS_BIN" --version 2>&1 | head -n 1 || true)
LIV_BIN=$(find_cmd liv || true)
LIV_VER=""; [ -n "$LIV_BIN" ] && LIV_VER=$("$LIV_BIN" version 2>&1 | head -n 1 || true)
BREW=0; [ -n "$(find_cmd brew || true)" ] && BREW=1
NIX=0; [ -n "$(find_cmd nix || true)" ] && NIX=1
CFG="$HOME/.local/share/agent-config/skillshare/config.yaml"
CFG_OK=0; [ -f "$CFG" ] && CFG_OK=1
HARNESSES=""
for h in claude codex opencode agy cursor pi gemini; do
  if [ -n "$(find_cmd "$h" || true)" ]; then
    HARNESSES="${HARNESSES}${HARNESSES:+,}$h"
  fi
done
SKILLS=""
for sd in "$HOME/.claude/skills" "$HOME/.codex/skills"; do
  if [ -d "$sd" ]; then
    for s in "$sd"/*; do
      [ -e "$s" ] || continue
      bn=$(basename "$s")
      case ",$SKILLS," in
        *",$bn,"*) ;;
        *) SKILLS="${SKILLS}${SKILLS:+,}$bn" ;;
      esac
    done
  fi
done
printf 'OS=%s\nARCH=%s\nNIXOS=%s\nHOME=%s\nSS_BIN=%s\nSS_VER=%s\nLIV_BIN=%s\nLIV_VER=%s\nBREW=%s\nNIX=%s\nCFG=%s\nCFG_OK=%s\nHARNESSES=%s\nSKILLS=%s\n' \
  "$OS" "$ARCH" "$NIXOS" "$HOME" "$SS_BIN" "$SS_VER" "$LIV_BIN" "$LIV_VER" "$BREW" "$NIX" "$CFG" "$CFG_OK" "$HARNESSES" "$SKILLS"
`

// ProbeSSHHost probes a local ("local") or SSH host ("user@host") using pure POSIX sh (zero remote prereqs!).
func ProbeSSHHost(sshTarget, homeOverride string) HostProbe {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if sshTarget == "local" {
		cmd = exec.CommandContext(ctx, "sh", "-c", posixProbeScript)
		if homeOverride != "" {
			cmd.Env = append(sshEnv(), "HOME="+homeOverride)
		} else {
			cmd.Env = sshEnv()
		}
	} else {
		args := append(append([]string(nil), SSHOpts...), sshTarget, "sh -s")
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
		cmd.Stdin = strings.NewReader(posixProbeScript)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := cleanSSHStderr(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return HostProbe{OK: false, Error: msg, ProbedAt: time.Now().Unix()}
	}
	kv := map[string]string{}
	for _, ln := range strings.Split(stdout.String(), "\n") {
		if k, v, ok := strings.Cut(ln, "="); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	splitCSV := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return strings.Split(s, ",")
	}
	return HostProbe{
		OK:             true,
		OS:             kv["OS"],
		Arch:           kv["ARCH"],
		IsNixOS:        kv["NIXOS"] == "1",
		Home:           kv["HOME"],
		Installed:      kv["SS_BIN"] != "",
		Bin:            kv["SS_BIN"],
		Version:        kv["SS_VER"],
		LivInstalled:   kv["LIV_BIN"] != "",
		LivBin:         kv["LIV_BIN"],
		LivVersion:     kv["LIV_VER"],
		ConfigPath:     kv["CFG"],
		ConfigExists:   kv["CFG_OK"] == "1",
		HasBrew:        kv["BREW"] == "1",
		HasNix:         kv["NIX"] == "1",
		Harnesses:      splitCSV(kv["HARNESSES"]),
		ExistingSkills: splitCSV(kv["SKILLS"]),
		ProbedAt:       time.Now().Unix(),
	}
}

// InstallSkillshare installs the stock upstream black-box `skillshare` binary on a local or SSH target.
func InstallSkillshare(t *model.Target, method string) map[string]any {
	if method == "" {
		method = "auto"
	}
	// Pure POSIX sh installer using Homebrew on macOS (when requested/auto) or SHA-256 verified GitHub static tarball
	script := fmt.Sprintf(`
set -eu
METHOD=%q
INSTALL_DIR="$HOME/.local/bin"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
MACH=$(uname -m | tr '[:upper:]' '[:lower:]')
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $MACH" >&2; exit 1 ;;
esac
BREW=""
if command -v brew >/dev/null 2>&1; then BREW=$(command -v brew); elif [ -x /opt/homebrew/bin/brew ]; then BREW=/opt/homebrew/bin/brew; fi
if [ "$OS" = "darwin" ] && [ -n "$BREW" ] && { [ "$METHOD" = "auto" ] || [ "$METHOD" = "brew" ]; }; then
  "$BREW" install skillshare >&2
  BIN=$(command -v skillshare || echo /opt/homebrew/bin/skillshare)
  VER=$("$BIN" --version 2>&1 | head -n 1 || true)
  printf 'OK=1\nMETHOD=brew\nBIN=%%s\nVER=%%s\n' "$BIN" "$VER"
  exit 0
fi
LATEST=$(curl -fsSI "https://github.com/runkids/skillshare/releases/latest" | grep -i '^location:' | sed 's/.*tag\/\([^[:space:]]*\).*/\1/' | tr -d '\r')
[ -n "$LATEST" ] || { echo "could not resolve latest release tag" >&2; exit 1; }
VER_NUM=${LATEST#v}
ASSET="skillshare_${VER_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/runkids/skillshare/releases/download/${LATEST}/${ASSET}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "$URL" -o "$TMP/$ASSET"
if curl -fsSL "https://github.com/runkids/skillshare/releases/download/${LATEST}/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null; then
  EXP=$(grep "  ${ASSET}$" "$TMP/checksums.txt" | awk '{print $1}' | head -n 1 || true)
  if [ -n "$EXP" ]; then
    ACT=$( (sha256sum "$TMP/$ASSET" 2>/dev/null || shasum -a 256 "$TMP/$ASSET") | awk '{print $1}' )
    [ "$EXP" = "$ACT" ] || { echo "SHA-256 mismatch for $ASSET" >&2; exit 1; }
  fi
fi
tar -xzf "$TMP/$ASSET" -C "$TMP"
mkdir -p "$INSTALL_DIR"
chmod 755 "$TMP/skillshare"
mv "$TMP/skillshare" "$INSTALL_DIR/skillshare"
VER=$("$INSTALL_DIR/skillshare" --version 2>&1 | head -n 1 || true)
printf 'OK=1\nMETHOD=release\nBIN=%%s\nVER=%%s\n' "$INSTALL_DIR/skillshare" "$VER"
`, method)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.IsLocal() {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = sshEnv()
	} else {
		args := append(append([]string(nil), SSHOpts...), t.SSH, "sh -s")
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
		cmd.Stdin = strings.NewReader(script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return map[string]any{"ok": false, "installed": false, "error": strings.TrimSpace(cleanSSHStderr(stderr.String()) + " " + err.Error())}
	}
	kv := map[string]string{}
	for _, ln := range strings.Split(stdout.String(), "\n") {
		if k, v, ok := strings.Cut(ln, "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	return map[string]any{
		"ok":        kv["OK"] == "1",
		"installed": kv["OK"] == "1",
		"method":    kv["METHOD"],
		"bin":       kv["BIN"],
		"version":   kv["VER"],
	}
}

func findLiverySourceDir(hint string) string {
	var candidates []string
	if hint != "" {
		candidates = append(candidates, hint)
	}
	if env := strings.TrimSpace(os.Getenv("LIVERY_SRC")); env != "" {
		candidates = append(candidates, model.ExpandHome(env))
	}
	if cwd, err := os.Getwd(); err == nil {
		cur := cwd
		for {
			candidates = append(candidates, cur)
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	home, _ := os.UserHomeDir()
	if b, err := os.ReadFile(filepath.Join(home, ".config", "livery", "src")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			candidates = append(candidates, model.ExpandHome(s))
		}
	}
	for _, dir := range candidates {
		gm := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gm); err == nil && strings.Contains(string(b), "module github.com/bowd/livery") {
			return dir
		}
	}
	return ""
}

// BootstrapLiv installs the static `liv` binary onto a local or remote target (~/.local/bin/liv).
// On a Go dev machine with repo source, it cross-compiles CGO_ENABLED=0 for the target's OS/arch
// and streams it atomically over SSH stdin!
func BootstrapLiv(repoRoot string, t *model.Target) (map[string]any, error) {
	pr := ProbeSSHHost(t.SSH, t.Home)
	if !pr.OK {
		return nil, fmt.Errorf("probe %s failed: %s", t.ID, pr.Error)
	}
	targetOS := pr.OS
	targetArch := pr.Arch
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	if targetArch == "" {
		targetArch = runtime.GOARCH
	}

	var binBytes []byte
	var err error
	srcDir := findLiverySourceDir(repoRoot)
	if srcDir != "" {
		tmpDir, err := os.MkdirTemp("", "liv-xbuild-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)
		outBin := filepath.Join(tmpDir, "liv")
		build := exec.Command("go", "build", "-trimpath", "-o", outBin, "./cmd/liv")
		build.Dir = srcDir
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+targetOS, "GOARCH="+targetArch)
		if out, err := build.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("cross-compile liv (%s/%s): %s", targetOS, targetArch, strings.TrimSpace(string(out)))
		}
		binBytes, err = os.ReadFile(outBin)
		if err != nil {
			return nil, err
		}
	} else if targetOS == runtime.GOOS && targetArch == runtime.GOARCH {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		binBytes, err = os.ReadFile(exe)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("cannot cross-build %s/%s without go.mod source tree", targetOS, targetArch)
	}

	installScript := `mkdir -p "$HOME/.local/bin" && tmp="$HOME/.local/bin/.liv.tmp.$$" && cat > "$tmp" && chmod 755 "$tmp" && mv "$tmp" "$HOME/.local/bin/liv" && "$HOME/.local/bin/liv" version`
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.IsLocal() {
		cmd = exec.CommandContext(ctx, "sh", "-c", installScript)
		cmd.Env = sshEnv()
	} else {
		args := append(append([]string(nil), SSHOpts...), t.SSH, sshShCmd(installScript))
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = sshEnv()
	}
	cmd.Stdin = bytes.NewReader(binBytes)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("install liv on %s: %s", t.ID, cleanSSHStderr(string(out)))
	}
	verLine := cleanSSHStderr(string(out))
	return map[string]any{
		"ok":      true,
		"target":  t.ID,
		"os":      targetOS,
		"arch":    targetArch,
		"bin":     "~/.local/bin/liv",
		"version": verLine,
	}, nil
}
