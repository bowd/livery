package ui

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type gitDiffLine struct {
	Kind   string `json:"kind"` // "add" | "del" | "ctx"
	OldNum int    `json:"old_num,omitempty"`
	NewNum int    `json:"new_num,omitempty"`
	Text   string `json:"text"`
}

type gitDiffHunk struct {
	Header string        `json:"header"`
	Lines  []gitDiffLine `json:"lines"`
}

type gitFileChange struct {
	Path      string        `json:"path"`
	Status    string        `json:"status"` // "M" | "A" | "D" | "R" | "?"
	Additions int           `json:"additions"`
	Deletions int           `json:"deletions"`
	Binary    bool          `json:"binary,omitempty"`
	Hunks     []gitDiffHunk `json:"hunks"`
}

type gitCommitInfo struct {
	SHA     string `json:"sha"`
	When    string `json:"when"`
	Subject string `json:"subject"`
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$`)

func parseUnifiedDiff(raw string) map[string]*gitFileChange {
	files := map[string]*gitFileChange{}
	var cur *gitFileChange
	var curHunk *gitDiffHunk
	oldLine, newLine := 0, 0

	for _, ln := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if strings.HasPrefix(ln, "diff --git ") {
			curHunk = nil
			// Parse b/<path>
			parts := strings.SplitN(ln, " b/", 2)
			path := ""
			if len(parts) == 2 {
				path = strings.Trim(parts[1], "\"")
			}
			cur = &gitFileChange{Path: path, Status: "M", Hunks: []gitDiffHunk{}}
			if path != "" {
				files[path] = cur
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(ln, "new file mode") {
			cur.Status = "A"
			continue
		}
		if strings.HasPrefix(ln, "deleted file mode") {
			cur.Status = "D"
			continue
		}
		if strings.HasPrefix(ln, "rename to ") {
			cur.Status = "R"
			newPath := strings.TrimSpace(strings.TrimPrefix(ln, "rename to "))
			if newPath != "" && newPath != cur.Path {
				delete(files, cur.Path)
				cur.Path = newPath
				files[newPath] = cur
			}
			continue
		}
		if strings.HasPrefix(ln, "Binary files ") {
			cur.Binary = true
			continue
		}
		if strings.HasPrefix(ln, "index ") || strings.HasPrefix(ln, "--- ") || strings.HasPrefix(ln, "+++ ") || strings.HasPrefix(ln, "similarity index") || strings.HasPrefix(ln, "rename from") || strings.HasPrefix(ln, "old mode") || strings.HasPrefix(ln, "new mode") {
			continue
		}
		if m := hunkHeaderRe.FindStringSubmatch(ln); m != nil {
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
			ctx := strings.TrimSpace(m[3])
			hdr := fmt.Sprintf("Line %d", newLine)
			if newLine == 0 {
				hdr = fmt.Sprintf("Line %d", oldLine)
			}
			if ctx != "" {
				hdr += " · " + ctx
			}
			cur.Hunks = append(cur.Hunks, gitDiffHunk{Header: hdr, Lines: []gitDiffLine{}})
			curHunk = &cur.Hunks[len(cur.Hunks)-1]
			continue
		}
		if curHunk == nil {
			continue
		}
		if strings.HasPrefix(ln, `\ No newline at end of file`) {
			continue
		}
		if strings.HasPrefix(ln, "+") {
			cur.Additions++
			curHunk.Lines = append(curHunk.Lines, gitDiffLine{Kind: "add", NewNum: newLine, Text: ln[1:]})
			newLine++
		} else if strings.HasPrefix(ln, "-") {
			cur.Deletions++
			curHunk.Lines = append(curHunk.Lines, gitDiffLine{Kind: "del", OldNum: oldLine, Text: ln[1:]})
			oldLine++
		} else if strings.HasPrefix(ln, " ") {
			curHunk.Lines = append(curHunk.Lines, gitDiffLine{Kind: "ctx", OldNum: oldLine, NewNum: newLine, Text: ln[1:]})
			oldLine++
			newLine++
		}
	}
	return files
}

func (s *Server) recentGitCommits(n int) []gitCommitInfo {
	out, err := exec.Command("git", "-C", s.repoRoot, "log", fmt.Sprintf("-n%d", n), "--format=%h\t%cr\t%s").Output()
	if err != nil {
		return []gitCommitInfo{}
	}
	var list []gitCommitInfo
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) == 3 {
			list = append(list, gitCommitInfo{SHA: parts[0], When: parts[1], Subject: parts[2]})
		}
	}
	if list == nil {
		return []gitCommitInfo{}
	}
	return list
}

func (s *Server) buildUntrackedFileChange(relPath string) *gitFileChange {
	fc := &gitFileChange{Path: relPath, Status: "?", Hunks: []gitDiffHunk{}}
	abs := filepath.Join(s.repoRoot, relPath)
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return fc
	}
	const maxPreview = 64 * 1024
	b, err := os.ReadFile(abs)
	if err != nil {
		return fc
	}
	if len(b) > maxPreview {
		b = b[:maxPreview]
	}
	// Detect binary via NUL byte
	for _, c := range b {
		if c == 0 {
			fc.Binary = true
			return fc
		}
	}
	rawLines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(b) == 0 {
		return fc
	}
	h := gitDiffHunk{Header: "New file", Lines: make([]gitDiffLine, 0, len(rawLines))}
	for i, text := range rawLines {
		h.Lines = append(h.Lines, gitDiffLine{Kind: "add", NewNum: i + 1, Text: text})
		fc.Additions++
	}
	fc.Hunks = append(fc.Hunks, h)
	return fc
}

func parseStatusPath(line string) (code, path string) {
	if len(line) < 4 {
		return "", ""
	}
	xy := line[:2]
	rest := strings.TrimSpace(line[3:])
	if idx := strings.Index(rest, " -> "); idx >= 0 {
		rest = rest[idx+4:]
	}
	rest = strings.Trim(rest, "\"")
	switch {
	case xy == "??":
		return "?", rest
	case strings.Contains(xy, "D"):
		return "D", rest
	case strings.Contains(xy, "A"):
		return "A", rest
	case strings.Contains(xy, "R"):
		return "R", rest
	default:
		return "M", rest
	}
}

func (s *Server) apiGitDiff(w http.ResponseWriter) {
	status := s.gitStatusLines()
	// Use `git diff HEAD` so both staged and unstaged changes appear cleanly; fallback to `git diff` if unborn HEAD.
	diffOut, err := exec.Command("git", "-C", s.repoRoot, "diff", "HEAD").Output()
	if err != nil {
		diffOut, _ = exec.Command("git", "-C", s.repoRoot, "diff").Output()
	}
	parsed := parseUnifiedDiff(string(diffOut))

	files := make([]*gitFileChange, 0, len(status))
	seen := map[string]bool{}
	for _, ln := range status {
		code, p := parseStatusPath(ln)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if code == "?" {
			files = append(files, s.buildUntrackedFileChange(p))
			continue
		}
		if fc, ok := parsed[p]; ok {
			if code != "" && fc.Status == "M" {
				fc.Status = code
			}
			files = append(files, fc)
		} else {
			files = append(files, &gitFileChange{Path: p, Status: code, Hunks: []gitDiffHunk{}})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dirty":          len(status) > 0,
		"repo_root":      s.repoRoot,
		"status":         nonNilStrings(status),
		"diff":           string(diffOut),
		"files":          files,
		"recent_commits": s.recentGitCommits(6),
	})
}

func (s *Server) apiGitCommit(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	msg, _ := body["message"].(string)
	msg = strings.TrimSpace(msg)
	status := s.gitStatusLines()
	if len(status) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "nothing to commit"})
		return
	}
	if msg == "" {
		if len(status) == 1 {
			_, p := parseStatusPath(status[0])
			msg = "Update " + p
		} else {
			msg = fmt.Sprintf("Update %d control plane files", len(status))
		}
	}
	if out, err := exec.Command("git", "-C", s.repoRoot, "add", "-A").CombinedOutput(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.TrimSpace(string(out))})
		return
	}
	if out, err := exec.Command("git", "-C", s.repoRoot, "commit", "-m", msg).CombinedOutput(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.TrimSpace(string(out))})
		return
	}
	shaBytes, _ := exec.Command("git", "-C", s.repoRoot, "rev-parse", "--short", "HEAD").Output()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"sha":     strings.TrimSpace(string(shaBytes)),
		"message": msg,
	})
}

func (s *Server) apiGitDiscard(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	if conf, _ := body["confirm"].(bool); !conf {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "discard requires confirm: true"})
		return
	}
	rel, _ := body["path"].(string)
	rel = strings.TrimSpace(rel)

	if rel != "" {
		cleanRel := filepath.Clean(rel)
		if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") || cleanRel == "." {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid relative path"})
			return
		}
		// Check whether path is tracked in HEAD
		trackedErr := exec.Command("git", "-C", s.repoRoot, "cat-file", "-e", "HEAD:"+cleanRel).Run()
		if trackedErr == nil {
			if out, err := exec.Command("git", "-C", s.repoRoot, "checkout", "HEAD", "--", cleanRel).CombinedOutput(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.TrimSpace(string(out))})
				return
			}
		} else {
			_ = exec.Command("git", "-C", s.repoRoot, "rm", "-rf", "--cached", "--ignore-unmatch", "--", cleanRel).Run()
			abs := filepath.Join(s.repoRoot, cleanRel)
			if err := os.RemoveAll(abs); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
		}
	} else {
		if out, err := exec.Command("git", "-C", s.repoRoot, "reset", "--hard", "HEAD").CombinedOutput(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.TrimSpace(string(out))})
			return
		}
		if out, err := exec.Command("git", "-C", s.repoRoot, "clean", "-fd").CombinedOutput(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.TrimSpace(string(out))})
			return
		}
	}

	s.slots.InvalidatePreviewEnvs()
	s.invalidateSyncCache("")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
