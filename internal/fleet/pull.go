package fleet

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/skillshare"
)

type PullResult struct {
	Target        string   `json:"target"`
	OK            bool     `json:"ok"`
	DryRun        bool     `json:"dry_run"`
	ConfigChanged bool     `json:"config_changed"`
	SkillsChanged []string `json:"skills_changed"`
	Message       string   `json:"message"`
	Error         string   `json:"error,omitempty"`
}

// PullTarget pulls live skillshare config.yaml and any new/updated skills from a host back into the git repo:
//   - <host>:~/.local/share/agent-config/skillshare/config.yaml -> <repo>/skillshare/hosts/<id>/config.yaml
//   - <host>:~/.local/share/agent-config/skillshare/skills/     -> <repo>/skillshare/skills/
func PullTarget(repo *model.Repo, t *model.Target, dryRun, deleteExtra bool) PullResult {
	res := PullResult{Target: t.ID, DryRun: dryRun, SkillsChanged: []string{}}
	sroot := strings.TrimRight(t.SkillshareRoot, "/")
	if sroot == "" {
		sroot = "~/.local/share/agent-config/skillshare"
	}
	cfgRel := sroot + "/config.yaml"

	home, rawYAML, exists, err := remote.PullHostSkillshareConfig(t, cfgRel)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if exists && len(bytes.TrimSpace(rawYAML)) > 0 {
		norm, err := skillshare.NormalizeHostConfigYAML(rawYAML, home, sroot)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		dest := skillshare.HostConfigPath(repo.Root, t.ID)
		prev, _ := os.ReadFile(dest)
		if !bytes.Equal(prev, norm) {
			res.ConfigChanged = true
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					res.Error = err.Error()
					return res
				}
				if err := os.WriteFile(dest, norm, 0o644); err != nil {
					res.Error = err.Error()
					return res
				}
			}
		}
	}

	skillsAbs := sroot + "/skills"
	if strings.HasPrefix(skillsAbs, "~/") && home != "" {
		skillsAbs = filepath.Join(home, skillsAbs[2:])
	}
	changed, err := remote.RsyncPullDir(t, skillsAbs, skillshare.RepoSkillsDir(repo.Root), dryRun, deleteExtra)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if changed != nil {
		res.SkillsChanged = changed
	}

	res.OK = true
	verb := "pulled"
	if dryRun {
		verb = "would pull"
	}
	var parts []string
	if res.ConfigChanged {
		parts = append(parts, "config.yaml")
	}
	if len(res.SkillsChanged) > 0 {
		parts = append(parts, fmt.Sprintf("%d skill change(s)", len(res.SkillsChanged)))
	}
	if len(parts) == 0 {
		res.Message = fmt.Sprintf("%s: already up to date with repo", t.ID)
	} else {
		res.Message = fmt.Sprintf("%s: %s %s", t.ID, verb, strings.Join(parts, " + "))
	}
	return res
}
