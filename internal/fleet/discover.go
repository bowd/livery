package fleet

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
)

type DiscoveredHost struct {
	SuggestedID string   `json:"suggested_id"`
	SSH         string   `json:"ssh"`
	Host        string   `json:"host"`
	User        string   `json:"user"`
	OS          string   `json:"os,omitempty"`
	Sources     []string `json:"sources"`
	Online      bool     `json:"online"`
	Configured  bool     `json:"configured"`
}

// DiscoverEnvironments aggregates host candidates from:
// 1. Local machine
// 2. Herdr connected machines (`hc machine ls --json`)
// 3. Tailscale peers (`tailscale status --json`)
// 4. `~/.ssh/config` named Host blocks
func DiscoverEnvironments(repo *model.Repo) []DiscoveredHost {
	existingSSH := map[string]bool{}
	existingID := map[string]bool{}
	if repo != nil {
		for _, t := range repo.Targets {
			existingSSH[t.SSH] = true
			existingID[t.ID] = true
		}
	}

	curUser := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		curUser = u.Username
	}
	localHost, _ := os.Hostname()
	localShort := strings.Split(localHost, ".")[0]

	byKey := map[string]*DiscoveredHost{}
	addSource := func(ssh, host, usr, osName, src string, online bool) {
		key := ssh
		if key == "" {
			return
		}
		if cur, ok := byKey[key]; ok {
			if !hasStr(cur.Sources, src) {
				cur.Sources = append(cur.Sources, src)
			}
			if osName != "" && cur.OS == "" {
				cur.OS = osName
			}
			if online {
				cur.Online = true
			}
			return
		}
		sid := ssh
		if ssh == "local" {
			sid = curUser + "@" + strings.ToLower(localShort)
		} else if !strings.Contains(ssh, "@") && usr != "" {
			sid = usr + "@" + ssh
		}
		byKey[key] = &DiscoveredHost{
			SuggestedID: sid,
			SSH:         ssh,
			Host:        host,
			User:        usr,
			OS:          osName,
			Sources:     []string{src},
			Online:      online,
			Configured:  existingSSH[ssh] || existingID[sid],
		}
	}

	// 1. Local machine
	addSource("local", localShort, curUser, "local", "local", true)

	// 2. Herdr connected machines (`hc machine ls --json`)
	if hcBin, err := exec.LookPath("hc"); err == nil {
		if out, err := exec.Command(hcBin, "machine", "ls", "--json").Output(); err == nil {
			var doc struct {
				Machines []struct {
					Name      string `json:"name"`
					State     string `json:"state"`
					Temporary bool   `json:"temporary"`
				} `json:"machines"`
			}
			if json.Unmarshal(out, &doc) == nil {
				for _, m := range doc.Machines {
					if m.Temporary || m.Name == "" {
						continue
					}
					addSource(curUser+"@"+m.Name, m.Name, curUser, "", "herdr", m.State == "connected")
				}
			}
		}
	}

	// 3. Tailscale peers (`tailscale status --json`)
	if tsBin, err := exec.LookPath("tailscale"); err == nil {
		if out, err := exec.Command(tsBin, "status", "--json").Output(); err == nil {
			var doc struct {
				Peer map[string]struct {
					HostName string `json:"HostName"`
					DNSName  string `json:"DNSName"`
					OS       string `json:"OS"`
					Online   bool   `json:"Online"`
				} `json:"Peer"`
			}
			if json.Unmarshal(out, &doc) == nil {
				for _, peer := range doc.Peer {
					osLow := strings.ToLower(peer.OS)
					if osLow != "linux" && osLow != "macos" && osLow != "darwin" {
						continue
					}
					name := strings.TrimSuffix(strings.Split(peer.DNSName, ".")[0], ".")
					if name == "" {
						name = peer.HostName
					}
					if name == "" {
						continue
					}
					addSource(curUser+"@"+name, name, curUser, osLow, "tailscale", peer.Online)
				}
			}
		}
	}

	// 4. ~/.ssh/config named Host entries (skip common git forges)
	skipForge := map[string]bool{
		"github.com": true, "ssh.github.com": true, "gitlab.com": true, "bitbucket.org": true, "codeberg.org": true,
	}
	if home, err := os.UserHomeDir(); err == nil {
		parseSSHConfig(filepath.Join(home, ".ssh", "config"), curUser, func(alias, usr string) {
			if skipForge[strings.ToLower(alias)] {
				return
			}
			ssh := alias
			if usr != "" && !strings.Contains(alias, "@") {
				ssh = usr + "@" + alias
			}
			addSource(ssh, alias, usr, "", "ssh-config", false)
		})
	}

	out := make([]DiscoveredHost, 0, len(byKey))
	for _, h := range byKey {
		sort.Strings(h.Sources)
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Configured != out[j].Configured {
			return !out[i].Configured
		}
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		return out[i].SuggestedID < out[j].SuggestedID
	})
	return out
}

func parseSSHConfig(path, defaultUser string, emit func(alias, usr string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	var aliases []string
	curUser := ""
	flush := func() {
		for _, a := range aliases {
			if a == "" || strings.ContainsAny(a, "*?!") {
				continue
			}
			u := curUser
			if u == "" {
				u = defaultUser
			}
			emit(a, u)
		}
		aliases = nil
		curUser = ""
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kw := strings.ToLower(fields[0])
		if kw == "host" {
			flush()
			aliases = fields[1:]
		} else if kw == "user" && len(aliases) > 0 {
			curUser = fields[1]
		}
	}
	flush()
}

// SuggestTargetFromProbe runs a live zero-prereq SSH probe and returns a suggested NewTargetSpec.
func SuggestTargetFromProbe(repo *model.Repo, sshTarget, suggestedID string) (model.NewTargetSpec, remote.HostProbe) {
	pr := remote.ProbeSSHHost(sshTarget, "")
	id := strings.TrimSpace(suggestedID)
	if id == "" {
		id = sshTarget
	}
	var profiles []string
	if _, ok := repo.Profiles["base"]; ok {
		profiles = append(profiles, "base")
	}
	if pr.IsNixOS {
		if _, ok := repo.Profiles["nixos-host"]; ok {
			profiles = append(profiles, "nixos-host")
		}
	}
	var ssTargets []string
	for _, h := range pr.Harnesses {
		if h == "agy" {
			ssTargets = append(ssTargets, "antigravity-cli")
		} else {
			ssTargets = append(ssTargets, h)
		}
	}
	if len(ssTargets) == 0 {
		ssTargets = []string{"claude", "codex"}
	}
	spec := model.NewTargetSpec{
		ID:                id,
		SSH:               sshTarget,
		Providers:         []string{"claude", "codex"},
		Profiles:          profiles,
		SkillshareTargets: ssTargets,
		External:          pr.ExistingSkills,
		Roles:             []string{},
	}
	return spec, pr
}

func hasStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
