package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bowd/livery/internal/fleet"
	"github.com/bowd/livery/internal/herdr"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/remote"
	"github.com/bowd/livery/internal/render"
	"github.com/bowd/livery/internal/secrets"
	"github.com/bowd/livery/internal/skillshare"
	"github.com/bowd/livery/internal/ui"
	"github.com/bowd/livery/internal/version"
)

func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	cmd := args[0]
	rest := args[1:]

	// Direct harness launch: `liv <claude|codex|opencode|agy> <profile> [args...]`
	// Zero flag parsing after <profile>: every trailing argument is passed verbatim!
	if _, isHarness := launcher.Drivers[cmd]; isHarness {
		return cmdHarnessLaunch(cmd, rest)
	}

	switch cmd {
	case "-h", "--help", "help":
		printUsage()
		return 0
	case "version", "--version", "-v":
		fmt.Printf("liv %s (%s %s)\n", version.Version, version.Commit, version.Date)
		return 0
	case "_remote":
		return remote.RunAgentStdio()
	case "run":
		return cmdRun(rest)
	case "list", "ls":
		return cmdList(rest)
	case "show":
		return cmdShow(rest)
	case "dry-run":
		return cmdDryRun(rest)
	case "login":
		return cmdLogin(rest)
	case "status":
		return cmdStatus(rest)
	case "secret":
		return cmdSecret(rest)
	case "secret-helper":
		return cmdSecretHelper(rest)
	case "secrets":
		return cmdFleetSecrets(rest)
	case "ui":
		return cmdUI(rest)
	case "check":
		return cmdCheck(rest)
	case "plan":
		return cmdPlan(rest)
	case "sync":
		return cmdSync(rest)
	case "pull":
		return cmdPull(rest)
	case "drift":
		return cmdDrift(rest)
	case "migrate":
		return cmdMigrate(rest)
	case "audit":
		return cmdAudit(rest)
	case "doctor":
		return cmdDoctor(rest)
	case "matrix":
		return cmdMatrix(rest)
	case "env":
		return cmdEnv(rest)
	case "herdr":
		return cmdHerdr(rest)
	case "onboard":
		return cmdOnboard(rest)
	case "profile":
		return cmdProfile(rest)
	case "group":
		return cmdGroup(rest)
	case "apps":
		return cmdApps(rest)
	default:
		fmt.Fprintf(os.Stderr, "liv: unknown command %q (run `liv --help`)\n", cmd)
		return 2
	}
}

func printUsage() {
	fmt.Print(`livery (liv) — declarative multi-host agent configuration & profile launcher

Launch an agent profile (forwards all trailing arguments verbatim):
  liv <harness> <profile> [args...]   e.g. liv claude gateway --dangerously-skip-permissions
  liv <harness>                       list profiles (claude, codex, opencode, openclaw, agy)

Profiles, groups & onboarding:
  liv onboard [--apply]               guided setup & follow-up checklist
  liv profile <new|edit|rm|list|blueprints>
  liv group <list|set>                manage shared memory/session groups
  liv apps <status|t3|herdr|openclaw> edit/sync T3 Code, Herdr & OpenClaw configs
  liv list [--json] [--harness <h>]   list configured launcher profiles
  liv show <harness> <profile>        show profile metadata and secret refs
  liv dry-run <harness> <profile> ... print resolved argv & env without executing
  liv login <harness> <profile>       run interactive OAuth login for a profile
  liv status                          local liv, profile, secret, and Herdr mod checksum status
  liv secret <set|check|list|rm|import-t3>

Fleet & repo control plane:
  liv ui [--port 19400] [--no-open]   open the Livery Meta-UI (wraps stock skillshare ui)
  liv plan [--target <id>]            preview changes across targets
  liv sync [--target <id>]            outward sync: compile & push to environments + tag livery-base
  liv drift [--target <id>]           inspect local changes in environments vs livery-base
  liv pull [--target <id>] [--dry-run] backward sync: pull host edits into XDG catalog
  liv migrate [--from <dir>] [--to <dir>] migrate repo data into ~/.local/share/livery
  liv check                           validate inventory.toml, profiles, skills, hooks, launchers
  liv audit [--target <id>]           run stock skillshare security audit on skills
  liv doctor [--target <id>]          check config drift, skillshare, Herdr mod, and secret parity
  liv matrix                          regenerate provenance/matrix.md
  liv secrets <status|sync|import-t3> cross-host secret parity & stdin sync

Environments & Herdr mod:
  liv env <discover|probe|add|remove|bootstrap>
  liv herdr <status|install-mod|check-mod|checksum|export-mod|uninstall-mod>
  liv version
`)
}

func optionalRepoRoot() string {
	r, err := model.FindRepoRoot("")
	if err != nil {
		return ""
	}
	return r
}

func mustRepoRoot(explicit string) (string, error) {
	return model.FindRepoRoot(explicit)
}

func loadProfilesAndStore() (map[string]map[string]any, *secrets.Store, error) {
	store, err := secrets.NewStore()
	if err != nil {
		return nil, nil, err
	}
	profiles := launcher.LoadAllProfiles(optionalRepoRoot())
	return profiles, store, nil
}

// --- Direct Harness Launching -------------------------------------------------------------------

func cmdHarnessLaunch(harness string, args []string) int {
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		rows := launcher.ListProfileRows(profiles, store, harness)
		if len(rows) == 0 {
			fmt.Fprintf(os.Stderr, "No %s profiles configured.\n", harness)
			return 1
		}
		fmt.Printf("Available %s profiles (usage: liv %s <profile> [args...]):\n", harness, harness)
		for _, r := range rows {
			sec := ""
			if !r.SecretsOK {
				sec = "  [missing secret]"
			}
			desc := ""
			if r.Description != "" {
				desc = "  — " + r.Description
			}
			fmt.Printf("  %-14s %-16s %-9s%s%s\n", r.Profile, r.AuthMode, r.Mode, desc, sec)
		}
		return 0
	}
	profileName := args[0]
	passArgs := args[1:]
	spec, err := launcher.ResolveHarnessProfile(profiles, harness, profileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	if err := launcher.ExecProfile(spec, passArgs, store); err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	return 0
}

func cmdRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv run <id-or-harness/profile> [--] [args...]")
		return 2
	}
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	var spec map[string]any
	var pass []string
	if _, ok := launcher.Drivers[args[0]]; ok && len(args) >= 2 {
		spec, err = launcher.ResolveHarnessProfile(profiles, args[0], args[1])
		pass = args[2:]
	} else {
		spec, err = launcher.ResolveByID(profiles, args[0])
		pass = args[1:]
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	if len(pass) > 0 && pass[0] == "--" {
		pass = pass[1:]
	}
	if err := launcher.ExecProfile(spec, pass, store); err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	return 0
}

// --- Runtime Profile Inspection -----------------------------------------------------------------

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	harnessFilter := fs.String("harness", "", "filter by harness")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	rows := launcher.ListProfileRows(profiles, store, *harnessFilter)
	if *asJSON {
		if rows == nil {
			rows = []launcher.ProfileListRow{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Println("No launcher profiles found.")
		return 0
	}
	fmt.Printf("%-26s %-16s %-10s %-8s %s\n", "COMMAND", "AUTH", "MODE", "SECRETS", "DESCRIPTION")
	for _, r := range rows {
		sec := "ok"
		if !r.SecretsOK {
			sec = "MISSING"
		}
		fmt.Printf("%-26s %-16s %-10s %-8s %s\n", strings.Join(r.Command, " "), r.AuthMode, r.Mode, sec, r.Description)
	}
	return 0
}

func resolveProfileArgs(profiles map[string]map[string]any, args []string) (map[string]any, []string, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("profile name required (e.g. `claude gateway` or `claude-gateway`)")
	}
	if _, ok := launcher.Drivers[args[0]]; ok && len(args) >= 2 {
		sp, err := launcher.ResolveHarnessProfile(profiles, args[0], args[1])
		return sp, args[2:], err
	}
	sp, err := launcher.ResolveByID(profiles, args[0])
	return sp, args[1:], err
}

func cmdShow(args []string) int {
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	spec, _, err := resolveProfileArgs(profiles, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	h := fmt.Sprint(spec["harness"])
	drv := launcher.Drivers[h]
	req := map[string]string{}
	if drv != nil {
		req = drv.RequiredSecrets(spec)
	}
	secState := map[string]string{}
	for k, ref := range req {
		if store.Exists(ref, nil) {
			secState[k] = ref + " (present)"
		} else {
			secState[k] = ref + " (MISSING)"
		}
	}
	out := map[string]any{
		"id":          spec["id"],
		"harness":     h,
		"profile":     spec["profile"],
		"command":     []string{"liv", h, fmt.Sprint(spec["profile"])},
		"auth_mode":   spec["auth_mode"],
		"mode":        spec["mode"],
		"source":      spec["_source"],
		"path":        spec["_path"],
		"description": spec["description"],
		"secrets":     secState,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func cmdDryRun(args []string) int {
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	spec, rest, err := resolveProfileArgs(profiles, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	h := fmt.Sprint(spec["harness"])
	drv := launcher.Drivers[h]
	plan, err := drv.BuildPlan(spec, rest, launcher.EnvMap(), true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	secStatus := map[string]string{}
	for k, ref := range plan.EnvSecretRefs {
		if store.Exists(ref, nil) {
			secStatus[k] = ref + " (present)"
		} else {
			secStatus[k] = ref + " (MISSING)"
		}
	}
	out := map[string]any{
		"binary":      plan.Binary,
		"argv":        plan.Argv,
		"env_set":     plan.EnvSet,
		"env_unset":   plan.EnvUnset,
		"env_secrets": secStatus,
		"home":        plan.HomeInfo,
		"notes":       plan.Notes,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func cmdLogin(args []string) int {
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	spec, _, err := resolveProfileArgs(profiles, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	if err := launcher.RunLogin(spec, store); err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	return 0
}

func cmdStatus(_ []string) int {
	profiles, store, err := loadProfilesAndStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	rows := launcher.ListProfileRows(profiles, store, "")
	ssBin, ssVer := skillshare.LocalVersion("")
	hs := herdr.Status("")
	fmt.Printf("livery (liv) %s\n", version.Version)
	fmt.Printf("  secrets backend : %s\n", store.Backend)
	fmt.Printf("  profiles loaded : %d\n", len(rows))
	if ssBin != "" {
		fmt.Printf("  skillshare      : %s (%s)\n", ssVer, ssBin)
	} else {
		fmt.Printf("  skillshare      : not installed\n")
	}
	if hs.HerdrPresent {
		shortExp := hs.ExpectedSHA256
		if len(shortExp) > 12 {
			shortExp = shortExp[:12]
		}
		state := "outdated"
		if hs.Installed && hs.UpToDate {
			state = "up-to-date"
		} else if !hs.Installed {
			state = "not-installed"
		}
		fmt.Printf("  herdr mod       : %s (sha256:%s, key_bound=%v)\n", state, shortExp, hs.KeyBound)
	}
	if r := optionalRepoRoot(); r != "" {
		fmt.Printf("  repo root       : %s\n", r)
	}
	return 0
}

// --- Local Secrets & apiKeyHelper ---------------------------------------------------------------

func cmdSecretHelper(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: liv secret-helper <ref>")
		return 2
	}
	store, err := secrets.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	val, err := store.Resolve(args[0], nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = io.WriteString(os.Stdout, val)
	return 0
}

func readSecretHidden(prompt string) (string, error) {
	fi, err := os.Stdin.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// Fallback to stdin line read if no /dev/tty
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			return strings.TrimRight(sc.Text(), "\r\n"), nil
		}
		return "", fmt.Errorf("no input")
	}
	defer tty.Close()
	sttyOff := exec.Command("stty", "-echo")
	sttyOff.Stdin = tty
	_ = sttyOff.Run()
	defer func() {
		sttyOn := exec.Command("stty", "echo")
		sttyOn.Stdin = tty
		_ = sttyOn.Run()
		fmt.Fprintln(os.Stderr)
	}()
	r := bufio.NewReader(tty)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func cmdSecret(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv secret <set|check|list|rm|import-t3>")
		return 2
	}
	store, err := secrets.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "set":
		fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
		fromStdin := fs.Bool("stdin", false, "read value from stdin")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv secret set <name> [--stdin]")
			return 2
		}
		name := fs.Arg(0)
		var val string
		if *fromStdin {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			val = strings.TrimRight(string(b), "\r\n")
		} else {
			val, err = readSecretHidden(fmt.Sprintf("Enter secret value for store:%s: ", name))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if err := store.SetManaged(name, val); err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		fmt.Printf("Stored store:%s in %s backend.\n", name, store.Backend)
		return 0
	case "check":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv secret check <name-or-ref>")
			return 2
		}
		ref := rest[0]
		if !strings.Contains(ref, ":") {
			ref = "store:" + ref
		}
		if store.Exists(ref, nil) {
			fmt.Printf("%s: present (%s)\n", ref, store.Backend)
			return 0
		}
		fmt.Printf("%s: MISSING\n", ref)
		return 1
	case "list", "ls":
		recs := store.ListManaged()
		if len(recs) == 0 {
			fmt.Printf("No managed secrets recorded (%s backend).\n", store.Backend)
			return 0
		}
		for _, r := range recs {
			st := "present"
			if !r.Present {
				st = "MISSING"
			}
			fmt.Printf("  store:%-28s %-8s (%s)\n", r.Name, st, r.Backend)
		}
		return 0
	case "rm", "delete":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv secret rm <name>")
			return 2
		}
		ok, err := store.RemoveManaged(rest[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		if ok {
			fmt.Printf("Removed store:%s\n", rest[0])
		} else {
			fmt.Printf("store:%s was not present\n", rest[0])
		}
		return 0
	case "import-t3":
		return cmdImportT3(store, rest)
	default:
		fmt.Fprintf(os.Stderr, "liv secret: unknown subcommand %q\n", sub)
		return 2
	}
}

func cmdImportT3(store *secrets.Store, args []string) int {
	fs := flag.NewFlagSet("import-t3", flag.ContinueOnError)
	t3Dir := fs.String("t3-dir", "", "path to ~/.t3/userdata")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	res, err := secrets.ImportT3Secrets(store, *t3Dir, nil, *dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	if len(res) == 0 {
		fmt.Println("No T3 Code secrets found.")
		return 0
	}
	for _, r := range res {
		fmt.Printf("  store:%-28s %-14s (%s) <- %s\n", r.Name, r.Action, r.Backend, strings.Join(r.Sources, ", "))
	}
	return 0
}

// --- Fleet Control Plane ------------------------------------------------------------------------

func selectTargets(repo *model.Repo, targetID string) ([]*model.Target, error) {
	if targetID == "" {
		return repo.Targets, nil
	}
	t, err := repo.Target(targetID)
	if err != nil {
		return nil, err
	}
	return []*model.Target{t}, nil
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	errs := model.CheckRepo(root)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		return 1
	}
	fmt.Println("check: ok")
	return 0
}

func cmdPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	rc := 0
	for _, t := range targets {
		tp := fleet.PlanTarget(repo, t)
		fmt.Println(fleet.FormatPlan(tp))
		if tp.Error != "" || len(tp.By("!")) > 0 {
			rc = 1
		}
	}
	return rc
}

func cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	allowDirty := fs.Bool("allow-dirty", false, "sync even with uncommitted git changes")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	if errs := model.CheckRepo(root); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		return 1
	}
	if !*allowDirty {
		if out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintln(os.Stderr, "liv sync: git working tree is dirty (commit changes or pass --allow-dirty)")
			return 1
		}
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	commit := repo.HeadCommit()
	rc := 0
	for _, t := range targets {
		tp := fleet.PlanTarget(repo, t)
		fmt.Println(fleet.FormatPlan(tp))
		if tp.Error != "" {
			rc = 1
			continue
		}
		if err := fleet.SyncTarget(repo, tp, commit); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			rc = 1
			continue
		}
		fmt.Printf("  synced %s\n", t.ID)
	}
	return rc
}

func cmdPull(args []string) int {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	dryRun := fs.Bool("dry-run", false, "preview changes without writing to the repo")
	delExtra := fs.Bool("delete", false, "delete repo skills absent from the target (default: additive)")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	rc := 0
	for _, t := range targets {
		res := fleet.PullTarget(repo, t, *dryRun, *delExtra)
		if !res.OK {
			fmt.Fprintf(os.Stderr, "ERROR %s: %s\n", t.ID, res.Error)
			rc = 1
			continue
		}
		fmt.Println(res.Message)
		for _, ln := range res.SkillsChanged {
			fmt.Printf("  %s\n", ln)
		}
	}
	return rc
}

func cmdDrift(args []string) int {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	targetID := fs.String("target", "", "single environment id")
	asJSON := fs.Bool("json", false, "output JSON")
	repoFlag := fs.String("repo", "", "path to livery data home")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	var reps []fleet.EnvDriftReport
	hasAny := false
	for _, t := range targets {
		r := fleet.InspectEnvDrift(repo, t)
		reps = append(reps, r)
		if r.HasDrift || !r.OK {
			hasAny = true
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(reps)
	} else {
		for _, r := range reps {
			if !r.OK {
				fmt.Printf("== %s: ERROR %s\n", r.Target, r.Error)
				continue
			}
			if !r.HasBase {
				fmt.Printf("== %s: no livery-base tag yet (run `liv sync --target %s`)\n", r.Target, r.Target)
				continue
			}
			if !r.HasDrift {
				fmt.Printf("== %s: clean (livery-base %s)\n", r.Target, r.BaseSHA)
				continue
			}
			fmt.Printf("== %s: %d local change(s) since %s\n", r.Target, len(r.Items), r.BaseSHA)
			for _, it := range r.Items {
				fmt.Printf("  [%s/%s] %s — %s (default: %s)\n", it.Category, it.Kind, it.Name, it.Detail, it.DefaultAct)
			}
		}
	}
	if hasAny {
		return 1
	}
	return 0
}

func cmdMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fromDir := fs.String("from", ".", "source repo directory")
	toDir := fs.String("to", model.DefaultDataHome(), "destination XDG data home")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := fleet.MigrateToXDG(*fromDir, *toDir); err != nil {
		fmt.Fprintln(os.Stderr, "liv migrate:", err)
		return 1
	}
	fmt.Printf("Migrated livery control plane into %s\n", *toDir)
	fmt.Printf("  Skillshare catalog repo : %s\n", skillshare.CatalogDir(*toDir))
	fmt.Printf("  Env local overrides     : %s\n", filepath.Join(*toDir, "env-overrides"))
	return 0
}

func cmdAudit(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	threshold := fs.String("threshold", "", "severity threshold (critical|high|medium|low)")
	profile := fs.String("profile", "", "audit profile (default|strict|permissive)")
	asJSON := fs.Bool("json", false, "output JSON")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	rep := skillshare.RunAudit(repo, *targetID, *threshold, *profile, "")
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Printf("== skillshare audit (%s)\n", rep.Scope)
		if rep.Error != "" {
			fmt.Fprintln(os.Stderr, "  ERROR:", rep.Error)
		} else {
			for _, r := range rep.Results {
				name := fmt.Sprint(r["skillName"])
				fsList, _ := r["findings"].([]any)
				if len(fsList) == 0 {
					fmt.Printf("  [OK ] %s\n", name)
				} else {
					fmt.Printf("  [!! ] %s (%d finding(s))\n", name, len(fsList))
				}
			}
			fmt.Printf("  summary: %v\n", rep.Summary)
		}
	}
	if rep.OK {
		return 0
	}
	if rep.RC > 0 {
		return rep.RC
	}
	return 1
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	out, ok := fleet.RunDoctorReport(repo, targets)
	fmt.Println(out)
	if !ok {
		return 1
	}
	return 0
}

func cmdMatrix(args []string) int {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	out, err := render.BuildMatrix(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	fmt.Printf("wrote %s\n", out)
	return 0
}

func cmdFleetSecrets(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv secrets <status|sync|import-t3> [--target <id>]")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	if sub == "import-t3" {
		store, err := secrets.NewStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return cmdImportT3(store, rest)
	}
	fs := flag.NewFlagSet("secrets "+sub, flag.ContinueOnError)
	targetID := fs.String("target", "", "single target id")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	repo, err := model.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	targets, err := selectTargets(repo, *targetID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 2
	}
	switch sub {
	case "status":
		rows := fleet.CheckSecretsStatus(repo, targets)
		rc := 0
		for _, r := range rows {
			fmt.Printf("== %s (%s)\n", r.Target, r.Backend)
			if r.Error != "" {
				fmt.Printf("  [ERR] %s\n", r.Error)
				rc = 1
				continue
			}
			if len(r.Refs) == 0 {
				fmt.Println("  (no launcher secrets required)")
				continue
			}
			for _, ref := range r.Refs {
				tag := "OK "
				if !ref.OK {
					tag = "ERR"
					rc = 1
				}
				fmt.Printf("  [%s] %-34s %-16s %s\n", tag, ref.Ref, ref.Profile, ref.Label)
			}
		}
		return rc
	case "sync":
		store, err := secrets.NewStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		rc := 0
		for _, t := range targets {
			refs := fleet.TargetSecretRefs(repo, t)
			toPush := map[string]string{}
			for _, item := range refs {
				scheme, name, err := secrets.ParseRef(item.Ref)
				if err != nil || scheme != "store" {
					continue
				}
				if _, already := toPush[name]; already {
					continue
				}
				val, err := store.Resolve(item.Ref, nil)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: local secret %s missing (run `liv secret set %s`)\n", t.ID, item.Ref, name)
					rc = 1
					continue
				}
				toPush[name] = val
			}
			if len(toPush) == 0 {
				continue
			}
			resp, err := remote.RunAgent(t, remote.AgentRequest{
				Op:      "secrets_put",
				Secrets: toPush,
			}, 60*time.Second)
			if err != nil || !resp.OK {
				msg := ""
				if err != nil {
					msg = err.Error()
				} else {
					msg = resp.Error
				}
				fmt.Fprintf(os.Stderr, "%s: secrets sync failed: %s\n", t.ID, msg)
				rc = 1
				continue
			}
			names := make([]string, 0, len(toPush))
			for k := range toPush {
				names = append(names, "store:"+k)
			}
			sort.Strings(names)
			fmt.Printf("== %s: synced %d secret(s) (%s)\n", t.ID, len(names), strings.Join(names, ", "))
		}
		return rc
	default:
		fmt.Fprintf(os.Stderr, "liv secrets: unknown subcommand %q\n", sub)
		return 2
	}
}

// --- Meta-UI ------------------------------------------------------------------------------------

func cmdUI(args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "bind host")
	port := fs.Int("port", 19400, "bind port")
	noOpen := fs.Bool("no-open", false, "do not open browser")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}
	srv, err := ui.NewServer(root, *host, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv ui:", err)
		return 1
	}
	fmt.Printf("livery ui listening on %s (repo: %s)\n", srv.URL(), root)
	if !*noOpen {
		openBrowser(srv.URL())
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	reloadCh := make(chan string, 1)

	// Auto-reload when the installed `liv` binary on disk is atomically replaced (`make install`)
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		if origInfo, err := os.Stat(exePath); err == nil {
			go func() {
				tk := time.NewTicker(1500 * time.Millisecond)
				defer tk.Stop()
				for range tk.C {
					curInfo, err := os.Stat(exePath)
					if err != nil {
						continue
					}
					if !os.SameFile(origInfo, curInfo) || !curInfo.ModTime().Equal(origInfo.ModTime()) {
						select {
						case reloadCh <- exePath:
						default:
						}
						return
					}
				}
			}()
		}
	}

	var reexecPath string
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nStopping livery ui…")
			_ = srv.Close()
		case p := <-reloadCh:
			reexecPath = p
			fmt.Fprintln(os.Stderr, "\nDetected updated liv binary — reloading livery ui…")
			_ = srv.Close()
		}
	}()
	if err := srv.Serve(); err != nil && !strings.Contains(err.Error(), "Server closed") {
		fmt.Fprintln(os.Stderr, "liv ui:", err)
		return 1
	}
	if reexecPath != "" {
		nextArgs := append([]string{os.Args[0], "ui"}, args...)
		hasNoOpen := false
		for _, a := range args {
			if a == "--no-open" || a == "-no-open" {
				hasNoOpen = true
				break
			}
		}
		if !hasNoOpen {
			nextArgs = append(nextArgs, "--no-open")
		}
		_ = syscall.Exec(reexecPath, nextArgs, os.Environ())
	}
	return 0
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", u).Start()
	case "linux":
		_ = exec.Command("xdg-open", u).Start()
	}
}

// --- Environment Discovery & Management ---------------------------------------------------------

func cmdEnv(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv env <discover|probe|add|remove|bootstrap>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "discover":
		fs := flag.NewFlagSet("env discover", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output JSON")
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		var repo *model.Repo
		if root, err := mustRepoRoot(*repoFlag); err == nil {
			repo, _ = model.LoadRepo(root)
		}
		hosts := fleet.DiscoverEnvironments(repo)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(hosts)
			return 0
		}
		fmt.Printf("%-26s %-10s %-18s %s\n", "SSH", "STATE", "SOURCES", "STATUS")
		for _, h := range hosts {
			st := "offline"
			if h.Online {
				st = "online"
			}
			cfg := "unconfigured"
			if h.Configured {
				cfg = "configured"
			}
			fmt.Printf("%-26s %-10s %-18s %s\n", h.SSH, st, strings.Join(h.Sources, ","), cfg)
		}
		return 0

	case "probe":
		fs := flag.NewFlagSet("env probe", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output JSON")
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv env probe <ssh-or-local> [--json]")
			return 2
		}
		sshTarget := fs.Arg(0)
		var repo *model.Repo
		if root, err := mustRepoRoot(*repoFlag); err == nil {
			repo, _ = model.LoadRepo(root)
		}
		if repo == nil {
			repo = &model.Repo{Profiles: map[string]*model.Profile{}}
		}
		spec, pr := fleet.SuggestTargetFromProbe(repo, sshTarget, "")
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"probe": pr, "suggested": spec})
			return 0
		}
		if !pr.OK {
			fmt.Fprintf(os.Stderr, "probe %s failed: %s\n", sshTarget, pr.Error)
			return 1
		}
		fmt.Printf("Host:       %s (%s/%s, nixos=%v)\n", sshTarget, pr.OS, pr.Arch, pr.IsNixOS)
		fmt.Printf("liv:        installed=%v (%s)\n", pr.LivInstalled, pr.LivVersion)
		fmt.Printf("skillshare: installed=%v (%s)\n", pr.Installed, pr.Version)
		fmt.Printf("harnesses:  %s\n", strings.Join(pr.Harnesses, ", "))
		fmt.Printf("skills:     %s\n", strings.Join(pr.ExistingSkills, ", "))
		fmt.Printf("suggested:  id=%s profiles=%v targets=%v\n", spec.ID, spec.Profiles, spec.SkillshareTargets)
		return 0

	case "add":
		fs := flag.NewFlagSet("env add", flag.ContinueOnError)
		id := fs.String("id", "", "environment id (defaults to ssh target)")
		profilesFlag := fs.String("profiles", "", "comma-separated profiles")
		targetsFlag := fs.String("targets", "", "comma-separated skillshare targets")
		noProbe := fs.Bool("no-probe", false, "skip live SSH probe")
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv env add <ssh-or-local> [--id <id>] [--profiles base] [--targets claude,codex]")
			return 2
		}
		root, err := mustRepoRoot(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		repo, err := model.LoadRepo(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		sshTarget := fs.Arg(0)
		var spec model.NewTargetSpec
		if !*noProbe {
			var pr remote.HostProbe
			spec, pr = fleet.SuggestTargetFromProbe(repo, sshTarget, *id)
			if !pr.OK {
				fmt.Fprintf(os.Stderr, "warning: SSH probe failed (%s); using defaults\n", pr.Error)
			}
		} else {
			spec = model.NewTargetSpec{
				ID:                *id,
				SSH:               sshTarget,
				Providers:         []string{"claude", "codex"},
				Profiles:          []string{"base"},
				SkillshareTargets: []string{"claude", "codex"},
			}
		}
		if *id != "" {
			spec.ID = *id
		}
		if spec.ID == "" {
			spec.ID = sshTarget
		}
		if *profilesFlag != "" {
			spec.Profiles = splitCSV(*profilesFlag)
		}
		if *targetsFlag != "" {
			spec.SkillshareTargets = splitCSV(*targetsFlag)
		}
		if err := model.AddTargetToInventory(root, spec); err != nil {
			fmt.Fprintln(os.Stderr, "liv env add:", err)
			return 1
		}
		fmt.Printf("Added %s (%s) to %s\n", spec.ID, spec.SSH, filepath.Join(root, "inventory.toml"))
		fmt.Printf("Next: liv env bootstrap %s --skillshare && liv plan --target %s && liv sync --target %s\n", spec.ID, spec.ID, spec.ID)
		return 0

	case "remove", "rm":
		fs := flag.NewFlagSet("env remove", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv env remove <target-id>")
			return 2
		}
		root, err := mustRepoRoot(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		if err := model.RemoveTargetFromInventory(root, fs.Arg(0)); err != nil {
			fmt.Fprintln(os.Stderr, "liv env remove:", err)
			return 1
		}
		fmt.Printf("Removed %s from inventory.toml\n", fs.Arg(0))
		return 0

	case "bootstrap":
		fs := flag.NewFlagSet("env bootstrap", flag.ContinueOnError)
		withSS := fs.Bool("skillshare", false, "also install stock skillshare binary")
		allFlag := fs.Bool("all", false, "bootstrap all remote targets")
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil || (!*allFlag && fs.NArg() != 1) {
			fmt.Fprintln(os.Stderr, "usage: liv env bootstrap <target-id|--all> [--skillshare]")
			return 2
		}
		root, err := mustRepoRoot(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		repo, err := model.LoadRepo(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		var targets []*model.Target
		if *allFlag {
			for _, t := range repo.Targets {
				if !t.IsLocal() {
					targets = append(targets, t)
				}
			}
		} else {
			t, err := repo.Target(fs.Arg(0))
			if err != nil {
				fmt.Fprintln(os.Stderr, "liv:", err)
				return 2
			}
			targets = []*model.Target{t}
		}
		rc := 0
		for _, t := range targets {
			res, err := remote.BootstrapLiv(root, t)
			if err != nil {
				fmt.Fprintf(os.Stderr, "liv env bootstrap %s: %v\n", t.ID, err)
				rc = 1
				continue
			}
			fmt.Printf("Installed %v (%v/%v) on %s -> %v\n", res["version"], res["os"], res["arch"], t.ID, res["bin"])
			if *withSS {
				ssRes := remote.InstallSkillshare(t, "auto")
				if ok, _ := ssRes["ok"].(bool); ok {
					fmt.Printf("Installed skillshare %v on %s -> %v\n", ssRes["version"], t.ID, ssRes["bin"])
				} else {
					fmt.Fprintf(os.Stderr, "skillshare install failed on %s: %v\n", t.ID, ssRes["error"])
					rc = 1
				}
			}
		}
		return rc

	default:
		fmt.Fprintf(os.Stderr, "liv env: unknown subcommand %q\n", sub)
		return 2
	}
}

// --- Herdr Launcher Mod Management --------------------------------------------------------------

func cmdHerdr(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv herdr <status|install-mod|check-mod|checksum|export-mod|uninstall-mod>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "checksum":
		return cmdHerdrChecksum(rest)
	case "export-mod":
		return cmdHerdrExportMod(rest)
	case "status":
		fs := flag.NewFlagSet("herdr status", flag.ContinueOnError)
		herdrDir := fs.String("herdr-dir", "", "Herdr config dir (default ~/.config/herdr-app)")
		asJSON := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		st := herdr.Status(*herdrDir)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(st)
			return 0
		}
		fmt.Printf("Herdr config     : %s (present=%v)\n", st.HerdrDir, st.HerdrPresent)
		fmt.Printf("Mod dir          : %s (installed=%v)\n", st.ModDir, st.Installed)
		fmt.Printf("Bundled SHA-256  : %s\n", st.ExpectedSHA256)
		if st.InstalledSHA256 != "" {
			fmt.Printf("Installed SHA-256: %s (up_to_date=%v)\n", st.InstalledSHA256, st.UpToDate)
		}
		if len(st.DriftedFiles) > 0 {
			fmt.Printf("Drifted files    : %s\n", strings.Join(st.DriftedFiles, ", "))
		}
		if st.SymlinkTarget != "" {
			fmt.Printf("Symlink          : -> %s\n", st.SymlinkTarget)
		}
		fmt.Printf("Compat alias     : agent-launcher -> livery-launcher (%v)\n", st.CompatAlias)
		fmt.Printf("Keybinding       : cmd-shift-n = %q (bound=%v)\n", st.KeyValue, st.KeyBound)
		return 0

	case "install-mod":
		fs := flag.NewFlagSet("herdr install-mod", flag.ContinueOnError)
		herdrDir := fs.String("herdr-dir", "", "Herdr config dir")
		symlink := fs.Bool("symlink", false, "symlink to livery repo source for development")
		noCompat := fs.Bool("no-compat-alias", false, "do not maintain agent-launcher compatibility symlink")
		noKeys := fs.Bool("no-keys", false, "do not update keys.json")
		repoFlag := fs.String("repo", "", "path to livery repo (for --symlink)")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		repoRoot := *repoFlag
		if *symlink && repoRoot == "" {
			repoRoot = optionalRepoRoot()
		}
		st, err := herdr.InstallMod(herdr.InstallOptions{
			HerdrDir:    *herdrDir,
			RepoRoot:    repoRoot,
			Symlink:     *symlink,
			CompatAlias: !*noCompat,
			UpdateKeys:  !*noKeys,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv herdr install-mod:", err)
			return 1
		}
		fmt.Printf("Installed %s into %s (sha256:%s)\n", herdr.ModName, st.ModDir, st.ExpectedSHA256[:12])
		if st.CompatAlias {
			fmt.Println("  maintained compat symlink: agent-launcher -> livery-launcher")
		}
		if st.KeyBound {
			fmt.Printf("  bound cmd-shift-n -> mod.open:%s in keys.json\n", herdr.ModName)
		}
		if st.HerdrAppBin != "" {
			if out, err := herdr.CheckMod(st.ModDir); err == nil {
				fmt.Printf("  %s\n", out)
			} else {
				fmt.Fprintf(os.Stderr, "  herdr-app check warning: %s\n", out)
			}
		}
		return 0

	case "check-mod":
		fs := flag.NewFlagSet("herdr check-mod", flag.ContinueOnError)
		modDir := fs.String("mod-dir", "", "mod directory to check")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		out, err := herdr.CheckMod(*modDir)
		if out != "" {
			fmt.Println(out)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv herdr check-mod:", err)
			return 1
		}
		return 0

	case "uninstall-mod":
		fs := flag.NewFlagSet("herdr uninstall-mod", flag.ContinueOnError)
		herdrDir := fs.String("herdr-dir", "", "Herdr config dir")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if err := herdr.UninstallMod(*herdrDir); err != nil {
			fmt.Fprintln(os.Stderr, "liv herdr uninstall-mod:", err)
			return 1
		}
		fmt.Println("Uninstalled livery-launcher mod from Herdr.")
		return 0

	default:
		fmt.Fprintf(os.Stderr, "liv herdr: unknown subcommand %q\n", sub)
		return 2
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
