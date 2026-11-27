package fleet

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/bowd/livery/internal/apps"
	"github.com/bowd/livery/internal/herdr"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/secrets"
	"github.com/bowd/livery/internal/skillshare"
)

type OnboardStep struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // "done" | "action" | "optional"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Command  string `json:"command,omitempty"`
	ActionID string `json:"action_id,omitempty"` // UI quick-fix action hook
}

type OnboardingReport struct {
	Ready         bool          `json:"ready"`
	PendingCount  int           `json:"pending_count"`
	OptionalCount int           `json:"optional_count"`
	Harnesses     []string      `json:"harnesses"`
	Steps         []OnboardStep `json:"steps"`
}

func detectLocalHarnesses() []string {
	var found []string
	for _, h := range []string{"claude", "codex", "opencode", "openclaw", "agy"} {
		if _, err := launcher.ResolveBinary(h, nil); err == nil {
			found = append(found, h)
		}
	}
	return found
}

// ComputeOnboarding inspects the local workstation, repo, secrets, Herdr mod checksum,
// T3/Herdr/OpenClaw configs, and discovered environments to produce a clean setup & follow-up checklist.
func ComputeOnboarding(repoRoot string) OnboardingReport {
	var steps []OnboardStep
	harnesses := detectLocalHarnesses()

	// 1. liv binary on PATH
	if p, err := exec.LookPath("liv"); err == nil {
		steps = append(steps, OnboardStep{
			ID: "liv-path", Status: "done", Title: "liv CLI on PATH", Detail: p,
		})
	} else {
		steps = append(steps, OnboardStep{
			ID: "liv-path", Status: "action", Title: "Install liv onto PATH",
			Detail:  "Run `make install` (or add ~/.local/bin to PATH) so Herdr and shells can invoke `liv` directly.",
			Command: "make install",
		})
	}

	// 2. Stock skillshare binary
	if bin, ver := skillshare.LocalVersion(""); bin != "" {
		steps = append(steps, OnboardStep{
			ID: "skillshare", Status: "done", Title: "Stock skillshare available", Detail: ver + " (" + bin + ")",
		})
	} else {
		steps = append(steps, OnboardStep{
			ID: "skillshare", Status: "action", Title: "Install stock skillshare binary",
			Detail:  "Powers multi-harness skill sync, security audit, and the embedded Skillshare UI.",
			Command: "brew install skillshare",
		})
	}

	// 3. Herdr launcher mod + bundled SHA-256 verification
	hs := herdr.Status("")
	if hs.HerdrPresent {
		shortExp := hs.ExpectedSHA256
		if len(shortExp) > 12 {
			shortExp = shortExp[:12]
		}
		if hs.Installed && hs.UpToDate && hs.KeyBound {
			steps = append(steps, OnboardStep{
				ID: "herdr-mod", Status: "done", Title: "Herdr launcher mod up to date",
				Detail: fmt.Sprintf("livery-launcher verified (sha256:%s, Cmd+Shift+N)", shortExp),
			})
		} else if hs.Installed && !hs.UpToDate {
			steps = append(steps, OnboardStep{
				ID: "herdr-mod", Status: "action", Title: "Update Herdr launcher mod",
				Detail:   fmt.Sprintf("Installed mod differs from bundled checksum sha256:%s (drifted: %s).", shortExp, strings.Join(hs.DriftedFiles, ", ")),
				Command:  "liv herdr install-mod",
				ActionID: "herdr-install-mod",
			})
		} else {
			steps = append(steps, OnboardStep{
				ID: "herdr-mod", Status: "action", Title: "Install Herdr launcher mod",
				Detail:   "Adds the flat searchable `liv <harness> <profile>` wizard on Cmd+Shift+N.",
				Command:  "liv herdr install-mod",
				ActionID: "herdr-install-mod",
			})
		}
	}

	// 4. Launcher profiles & missing secrets (distinguish active vs unassigned profiles, and match T3 secrets accurately)
	var repo *model.Repo
	activeLaunchers := map[string]bool{}
	if repoRoot != "" {
		if r, err := model.LoadRepo(repoRoot); err == nil {
			repo = r
			for _, t := range repo.Targets {
				if pids, err := repo.ExpandProfiles(t.Profiles); err == nil {
					if lids, err := repo.ResolveLaunchers(t, pids); err == nil {
						for _, lid := range lids {
							activeLaunchers[lid] = true
						}
					}
				}
			}
		}
	}

	store, _ := secrets.NewStore()
	profiles := launcher.LoadAllProfiles(repoRoot)
	var missingActive []string
	var missingUnused []string
	var unusedProfiles []string
	seenActive := map[string]bool{}
	seenUnused := map[string]bool{}
	seenUnusedProf := map[string]bool{}
	if store != nil {
		for _, r := range launcher.ListProfileRows(profiles, store, "") {
			isActive := len(activeLaunchers) == 0 || activeLaunchers[r.ID]
			for _, ref := range r.MissingSecrets {
				if isActive {
					if !seenActive[ref] {
						seenActive[ref] = true
						missingActive = append(missingActive, ref)
					}
				} else {
					if !seenUnused[ref] {
						seenUnused[ref] = true
						missingUnused = append(missingUnused, ref)
					}
					if !seenUnusedProf[r.ID] {
						seenUnusedProf[r.ID] = true
						unusedProfiles = append(unusedProfiles, r.ID)
					}
				}
			}
		}
	}
	// Only recommend `liv secret import-t3` if at least one actually-missing secret matches a discovered T3 secret!
	t3Disc := secrets.DiscoverT3Secrets("")
	t3MatchCount := 0
	for _, d := range t3Disc {
		if d.SuggestedName != "" && seenActive["store:"+d.SuggestedName] {
			t3MatchCount++
		}
	}

	if len(missingActive) == 0 {
		detail := fmt.Sprintf("All active launcher profiles have required secrets present (%s backend).", store.Backend)
		if len(missingUnused) > 0 {
			detail += fmt.Sprintf(" Unassigned profile(s) (%s) still need %s if you enable them.",
				strings.Join(unusedProfiles, ", "), strings.Join(missingUnused, ", "))
		}
		steps = append(steps, OnboardStep{
			ID: "secrets", Status: "done", Title: "Launcher secrets ready",
			Detail: detail,
		})
	} else {
		firstRef := missingActive[0]
		_, name, _ := secrets.ParseRef(firstRef)
		cmd := "liv secret set " + name
		detail := fmt.Sprintf("Missing %d secret ref(s) for active profiles: %s.", len(missingActive), strings.Join(missingActive, ", "))
		if t3MatchCount > 0 {
			cmd = "liv secret import-t3"
			detail += fmt.Sprintf(" (%d matching T3 Code secret(s) detected locally and ready for no-echo import.)", t3MatchCount)
		}
		steps = append(steps, OnboardStep{
			ID: "secrets", Status: "action", Title: "Populate missing launcher secrets",
			Detail:  detail,
			Command: cmd,
		})
	}

	// 5. Memory / session groups
	groups := launcher.SummarizeGroups(profiles)
	namedGroups := 0
	for _, g := range groups {
		if g.Kind == "group" {
			namedGroups++
		}
	}
	if namedGroups > 0 {
		steps = append(steps, OnboardStep{
			ID: "groups", Status: "done", Title: "Memory & session groups configured",
			Detail: fmt.Sprintf("%d named group(s) active across %d profiles.", namedGroups, len(profiles)),
		})
	} else {
		steps = append(steps, OnboardStep{
			ID: "groups", Status: "optional", Title: "Organize memory / session groups",
			Detail:  "Group profiles (e.g. `work` vs `personal`) to share conversation memory cleanly without mixing auth.",
			Command: "liv group set claude personal personal",
		})
	}

	// 6. Connected apps (T3 Code, Herdr programs, OpenClaw)
	t3Ov := apps.LoadT3Overview("")
	herdrOv := apps.LoadHerdrOverview("", "")
	ocOv := apps.LoadOpenClawOverview("")
	var detectedApps []string
	if t3Ov.Present {
		detectedApps = append(detectedApps, fmt.Sprintf("T3 Code (%d providers)", len(t3Ov.Providers)))
	}
	if herdrOv.Present {
		detectedApps = append(detectedApps, fmt.Sprintf("Herdr (%d programs)", len(herdrOv.Programs)))
	}
	if ocOv.Present {
		detectedApps = append(detectedApps, fmt.Sprintf("OpenClaw (%d profiles)", len(ocOv.Profiles)))
	}
	if len(detectedApps) > 0 {
		steps = append(steps, OnboardStep{
			ID: "apps", Status: "done", Title: "Detected companion apps",
			Detail:  strings.Join(detectedApps, " · "),
			Command: "liv apps status",
		})
	}

	// 7. Discovered online environments not yet added to inventory.toml
	if repo != nil {
		{
			disc := DiscoverEnvironments(repo)
			var uncfgOnline []string
			for _, h := range disc {
				if !h.Configured && h.Online {
					uncfgOnline = append(uncfgOnline, h.SSH)
				}
			}
			if len(uncfgOnline) > 0 {
				steps = append(steps, OnboardStep{
					ID: "discover", Status: "optional", Title: fmt.Sprintf("%d online host(s) discovered", len(uncfgOnline)),
					Detail:   strings.Join(uncfgOnline, ", "),
					Command:  "liv env discover",
					ActionID: "open-add-host",
				})
			} else {
				steps = append(steps, OnboardStep{
					ID: "discover", Status: "done", Title: "Environments configured",
					Detail: fmt.Sprintf("%d target(s) managed in inventory.toml.", len(repo.Targets)),
				})
			}

			// 8. Repo validation
			if errs := model.CheckRepo(repoRoot); len(errs) == 0 {
				steps = append(steps, OnboardStep{
					ID: "repo-check", Status: "done", Title: "Repo configuration valid",
					Detail: "inventory.toml, profiles, prompts, skills, hooks, and launchers pass lint.",
				})
			} else {
				steps = append(steps, OnboardStep{
					ID: "repo-check", Status: "action", Title: "Fix repo validation errors",
					Detail:  fmt.Sprintf("%d issue(s): %s", len(errs), errs[0]),
					Command: "liv check",
				})
			}
		}
	}

	pending := 0
	opt := 0
	for _, s := range steps {
		if s.Status == "action" {
			pending++
		} else if s.Status == "optional" {
			opt++
		}
	}
	return OnboardingReport{
		Ready:         pending == 0,
		PendingCount:  pending,
		OptionalCount: opt,
		Harnesses:     harnesses,
		Steps:         steps,
	}
}
