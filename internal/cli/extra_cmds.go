package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bowd/livery/internal/apps"
	"github.com/bowd/livery/internal/fleet"
	"github.com/bowd/livery/internal/herdr"
	"github.com/bowd/livery/internal/launcher"
	"github.com/bowd/livery/internal/model"
	"github.com/bowd/livery/internal/secrets"
)

func promptLine(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	ln, _ := r.ReadString('\n')
	ln = strings.TrimSpace(ln)
	if ln == "" {
		return def
	}
	return ln
}

func promptYesNo(r *bufio.Reader, label string, defYes bool) bool {
	hint := "[Y/n]"
	if !defYes {
		hint = "[y/N]"
	}
	fmt.Printf("%s %s: ", label, hint)
	ln, _ := r.ReadString('\n')
	ln = strings.ToLower(strings.TrimSpace(ln))
	if ln == "" {
		return defYes
	}
	return ln == "y" || ln == "yes"
}

// --- Onboarding & Follow-up Flow (`liv onboard`) ------------------------------------------------

func cmdOnboard(args []string) int {
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON report")
	apply := fs.Bool("apply", false, "interactively apply pending fixes")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root := *repoFlag
	if root == "" {
		root = optionalRepoRoot()
	}
	rep := fleet.ComputeOnboarding(root)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return 0
	}

	fmt.Println("Livery onboarding & health checklist")
	fmt.Printf("  detected harnesses : %s\n\n", strings.Join(rep.Harnesses, ", "))
	for _, s := range rep.Steps {
		icon := "[ok  ]"
		if s.Status == "action" {
			icon = "[todo]"
		} else if s.Status == "optional" {
			icon = "[tip ]"
		}
		fmt.Printf("  %s %s\n", icon, s.Title)
		if s.Detail != "" {
			fmt.Printf("         %s\n", s.Detail)
		}
		if s.Status != "done" && s.Command != "" {
			fmt.Printf("         -> %s\n", s.Command)
		}
	}
	fmt.Println()

	if *apply {
		r := bufio.NewReader(os.Stdin)
		for _, s := range rep.Steps {
			if s.Status != "action" {
				continue
			}
			switch s.ID {
			case "herdr-mod":
				if promptYesNo(r, "Install/update Herdr livery-launcher mod now?", true) {
					_ = cmdHerdr([]string{"install-mod"})
				}
			case "secrets":
				if strings.Contains(s.Command, "import-t3") {
					if promptYesNo(r, "Import detected T3 Code secrets into local store (no-echo)?", true) {
						_ = cmdSecret([]string{"import-t3"})
					}
				}
			}
		}
		rep = fleet.ComputeOnboarding(root)
	}

	if rep.Ready {
		fmt.Println("All required setup steps are complete!")
		if rep.OptionalCount > 0 {
			fmt.Println("Optional next steps: run `liv ui`, `liv profile new`, or `liv env discover`.")
		}
	} else {
		fmt.Printf("%d action item(s) remaining. Run `liv onboard --apply` or follow the commands above.\n", rep.PendingCount)
	}
	return 0
}

// --- Easy Profile Onboarding & Editing (`liv profile`) ------------------------------------------

func cmdProfile(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv profile <list|blueprints|new|edit|rm>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return cmdList(rest)
	case "blueprints":
		bps := model.LauncherBlueprints()
		for _, bp := range bps {
			fmt.Printf("  %-18s %-10s %-16s %s\n", bp.ID, bp.Harness, bp.AuthMode, bp.Description)
		}
		return 0
	case "new", "add", "edit":
		return cmdProfileSave(sub == "edit", rest)
	case "rm", "delete":
		fs := flag.NewFlagSet("profile rm", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv profile rm <launcher-id>")
			return 2
		}
		root, err := mustRepoRoot(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		if err := model.DeleteLauncherProfile(root, fs.Arg(0)); err != nil {
			fmt.Fprintln(os.Stderr, "liv profile rm:", err)
			return 1
		}
		fmt.Printf("Removed launcher profile %s\n", fs.Arg(0))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "liv profile: unknown subcommand %q\n", sub)
		return 2
	}
}

func cmdProfileSave(isEdit bool, args []string) int {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
	idFlag := fs.String("id", "", "full launcher id (e.g. claude-work)")
	harness := fs.String("harness", "", "harness (claude|codex|opencode|openclaw|agy)")
	name := fs.String("name", "", "short profile name (e.g. gateway, personal, work)")
	authMode := fs.String("auth", "", "auth mode")
	mode := fs.String("mode", "", "isolation mode (shadow|isolated|direct)")
	group := fs.String("group", "", "memory/session group name (e.g. work, personal)")
	desc := fs.String("description", "", "human description")
	baseURL := fs.String("base-url", "", "gateway or provider base URL")
	secretRef := fs.String("secret-ref", "", "secret reference (e.g. store:my-key)")
	modelName := fs.String("model", "", "default model")
	providerID := fs.String("provider-id", "", "Codex provider_id")
	providerName := fs.String("provider-name", "", "Codex provider_name")
	envKey := fs.String("env-key", "", "env var name for API key (e.g. ZAI_API_KEY)")
	openclawProf := fs.String("openclaw-profile", "", "OpenClaw --profile name")
	herdrName := fs.String("herdr-name", "", "Herdr display name")
	t3Name := fs.String("t3-name", "", "T3 Code display name")
	t3Models := fs.String("t3-models", "", "comma-separated T3 custom models")
	attach := fs.String("attach", "", "attach launcher to repo profile (e.g. owner-workstation)")
	repoFlag := fs.String("repo", "", "path to livery repo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := mustRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv:", err)
		return 1
	}

	in := model.LauncherProfileInput{
		ID:              *idFlag,
		Harness:         *harness,
		Profile:         *name,
		AuthMode:        *authMode,
		Mode:            *mode,
		SessionGroup:    *group,
		Description:     *desc,
		BaseURL:         *baseURL,
		TokenSecret:     *secretRef,
		Model:           *modelName,
		ProviderID:      *providerID,
		ProviderName:    *providerName,
		EnvKey:          *envKey,
		OpenClawProfile: *openclawProf,
		HerdrName:       *herdrName,
		T3DisplayName:   *t3Name,
		AttachProfile:   *attach,
	}
	if *t3Models != "" {
		in.T3CustomModels = splitCSV(*t3Models)
	}

	// Allow positional shorthand: `liv profile new claude work` or `liv profile edit claude-gateway`
	pos := fs.Args()
	if isEdit && len(pos) >= 1 && in.ID == "" {
		in.ID = pos[0]
		if repo, err := model.LoadRepo(root); err == nil {
			if sp, ok := repo.Launchers[in.ID]; ok {
				if in.Harness == "" {
					in.Harness = sp.Harness
				}
				if in.Profile == "" {
					in.Profile = sp.Profile
				}
				if in.Mode == "" {
					in.Mode = fmt.Sprint(sp.Raw["mode"])
				}
				if in.Description == "" {
					in.Description = fmt.Sprint(sp.Raw["description"])
				}
			}
		}
	} else if !isEdit && len(pos) >= 2 && in.Harness == "" && in.Profile == "" {
		in.Harness = pos[0]
		in.Profile = pos[1]
	}

	// Interactive wizard when called without harness/profile flags!
	if !isEdit && in.Harness == "" {
		r := bufio.NewReader(os.Stdin)
		bps := model.LauncherBlueprints()
		fmt.Println("Choose a launch profile blueprint:")
		for i, bp := range bps {
			fmt.Printf("  %d) %-26s %s\n", i+1, bp.Title, bp.Description)
		}
		pick := promptLine(r, "Select blueprint (1-8)", "1")
		idx := 0
		fmt.Sscanf(pick, "%d", &idx)
		if idx < 1 || idx > len(bps) {
			idx = 1
		}
		bp := bps[idx-1]
		in.Harness = bp.Harness
		in.AuthMode = bp.AuthMode
		in.Mode = bp.Mode
		in.Profile = promptLine(r, fmt.Sprintf("Short profile name (for `liv %s <name>`)", in.Harness), "")
		if in.Profile == "" {
			fmt.Fprintln(os.Stderr, "aborted: profile name required")
			return 2
		}
		in.Description = promptLine(r, "Description", fmt.Sprintf("%s (%s)", strings.Title(in.Harness), in.Profile))
		in.SessionGroup = promptLine(r, "Memory/session group (blank = default shared home, e.g. work / personal)", "")
		if bp.BaseURLHint != "" {
			in.BaseURL = promptLine(r, "Base URL", bp.BaseURLHint)
		}
		if bp.EnvKeyHint != "" {
			in.EnvKey = promptLine(r, "API key env var name", bp.EnvKeyHint)
		}
		if bp.SecretHint != "" {
			in.TokenSecret = promptLine(r, "Secret ref (store:<name>, never raw key)", bp.SecretHint)
		}
		if in.Harness == "codex" && in.AuthMode == "custom_provider" {
			in.ProviderID = promptLine(r, "Codex provider_id", in.Profile)
			in.ProviderName = promptLine(r, "Codex provider display name", in.Description)
		}
		in.AttachProfile = promptLine(r, "Attach to repo profile (e.g. owner-workstation, base, or blank)", "owner-workstation")
	}

	id, err := model.SaveLauncherProfile(root, in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv profile:", err)
		return 1
	}
	prof := model.DeriveProfileName(id, in.Harness, in.Profile)
	fmt.Printf("Saved launchers/%s.toml (command: `liv %s %s`)\n", id, in.Harness, prof)

	// Actionable follow-ups!
	var next []string
	if in.TokenSecret != "" {
		if st, err := secrets.NewStore(); err == nil && !st.Exists(in.TokenSecret, nil) {
			_, sname, _ := secrets.ParseRef(in.TokenSecret)
			if sname != "" {
				next = append(next, "liv secret set "+sname)
			}
		}
	}
	next = append(next, fmt.Sprintf("liv dry-run %s %s", in.Harness, prof))
	next = append(next, "liv apps herdr sync")
	fmt.Printf("Next: %s\n", strings.Join(next, " && "))
	return 0
}

// --- Memory / Session Groups (`liv group`) ------------------------------------------------------

func cmdGroup(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv group <list|set>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("group list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		profiles := launcher.LoadAllProfiles(optionalRepoRoot())
		groups := launcher.SummarizeGroups(profiles)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(groups)
			return 0
		}
		fmt.Printf("%-22s %-10s %-10s %s\n", "GROUP", "KIND", "SIZE", "PROFILES")
		for _, g := range groups {
			sz := "-"
			if g.Path != "" {
				sz = fmt.Sprintf("%.1f KB", float64(g.Bytes)/1024.0)
			}
			fmt.Printf("%-22s %-10s %-10s %s\n", g.Name, g.Kind, sz, strings.Join(g.Profiles, ", "))
		}
		return 0
	case "set":
		fs := flag.NewFlagSet("group set", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		pos := fs.Args()
		var id, grp string
		if len(pos) == 2 {
			id, grp = pos[0], pos[1]
		} else if len(pos) == 3 {
			id, grp = pos[0]+"-"+pos[1], pos[2]
		} else {
			fmt.Fprintln(os.Stderr, "usage: liv group set <harness> <profile> <group|none>  OR  liv group set <id> <group|none>")
			return 2
		}
		root, err := mustRepoRoot(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv:", err)
			return 1
		}
		if err := model.UpdateLauncherGroup(root, id, grp); err != nil {
			fmt.Fprintln(os.Stderr, "liv group set:", err)
			return 1
		}
		if grp == "" || grp == "none" || grp == "default" {
			fmt.Printf("Cleared session_group on %s (back to default shared/isolated home)\n", id)
		} else {
			fmt.Printf("Assigned %s -> memory/session group %q\n", id, grp)
		}
		fmt.Printf("Next: liv dry-run %s && liv sync\n", id)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "liv group: unknown subcommand %q\n", sub)
		return 2
	}
}

// --- Companion App Profile Editors (`liv apps <status|t3|herdr|openclaw>`) ----------------------

func cmdApps(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: liv apps <status|t3|herdr|openclaw>")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "status":
		fs := flag.NewFlagSet("apps status", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		h := apps.LoadHerdrOverview("", "")
		t3 := apps.LoadT3Overview("")
		oc := apps.LoadOpenClawOverview("")
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"herdr": h, "t3": t3, "openclaw": oc})
			return 0
		}
		fmt.Printf("Herdr    : present=%v (%d programs, %d templates)\n", h.Present, len(h.Programs), len(h.TemplateNames))
		fmt.Printf("T3 Code  : present=%v (%d provider instances)\n", t3.Present, len(t3.Providers))
		fmt.Printf("OpenClaw : present=%v (%d profiles)\n", oc.Present, len(oc.Profiles))
		return 0
	case "herdr":
		return cmdAppsHerdr(rest)
	case "t3":
		return cmdAppsT3(rest)
	case "openclaw":
		return cmdAppsOpenClaw(rest)
	default:
		fmt.Fprintf(os.Stderr, "liv apps: unknown subcommand %q\n", sub)
		return 2
	}
}

func cmdAppsHerdr(args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		ov := apps.LoadHerdrOverview("", "")
		for _, p := range ov.Programs {
			fmt.Printf("  %-20s %-24s %s\n", p.Key, p.Name, strings.Join(p.Run, " "))
		}
		return 0
	case "sync":
		fs := flag.NewFlagSet("apps herdr sync", flag.ContinueOnError)
		repoFlag := fs.String("repo", "", "path to livery repo")
		if err := fs.Parse(args[1:]); err != nil {
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
		n, err := apps.SyncHerdrFromRepo(repo, "", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "liv apps herdr sync:", err)
			return 1
		}
		fmt.Printf("Synced %d Herdr program(s) & template(s) using `liv <harness> <profile>`\n", n)
		return 0
	case "set":
		fs := flag.NewFlagSet("apps herdr set", flag.ContinueOnError)
		name := fs.String("name", "", "display name")
		icon := fs.String("icon", "", "icon path")
		runStr := fs.String("run", "", "space-separated command argv")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 || *runStr == "" {
			fmt.Fprintln(os.Stderr, "usage: liv apps herdr set <key> --run 'liv claude gateway' [--name 'Claude (Gateway)']")
			return 2
		}
		if err := apps.UpsertHerdrProgram("", apps.HerdrProgram{
			Key: fs.Arg(0), Name: *name, Icon: *icon, Run: strings.Fields(*runStr),
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Updated Herdr program %s\n", fs.Arg(0))
		return 0
	case "rm":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: liv apps herdr rm <key>")
			return 2
		}
		if err := apps.DeleteHerdrProgram("", args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Removed Herdr program %s\n", args[1])
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: liv apps herdr <list|sync|set|rm>")
		return 2
	}
}

func cmdAppsT3(args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		ov := apps.LoadT3Overview("")
		for _, p := range ov.Providers {
			st := "on "
			if !p.Enabled {
				st = "off"
			}
			fmt.Printf("  %-26s %-12s [%s] %-22s (%d models)\n", p.InstanceID, p.Driver, st, p.DisplayName, len(p.CustomModels))
		}
		return 0
	case "edit", "set":
		fs := flag.NewFlagSet("apps t3 edit", flag.ContinueOnError)
		displayName := fs.String("display-name", "", "displayName")
		driver := fs.String("driver", "", "driver (claudeAgent|codex|opencode)")
		binary := fs.String("binary", "", "binaryPath")
		homePath := fs.String("home-path", "", "homePath")
		models := fs.String("models", "", "comma-separated customModels")
		accent := fs.String("accent", "", "accentColor")
		enabledStr := fs.String("enabled", "", "true|false")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv apps t3 edit <instance-id> [--display-name ...] [--enabled true|false] [--models a,b]")
			return 2
		}
		patch := apps.T3ProviderPatch{InstanceID: fs.Arg(0), Driver: *driver}
		if *displayName != "" {
			patch.DisplayName = displayName
		}
		if *binary != "" {
			patch.BinaryPath = binary
		}
		if *homePath != "" {
			patch.HomePath = homePath
		}
		if *accent != "" {
			patch.AccentColor = accent
		}
		if *models != "" {
			patch.CustomModels = splitCSV(*models)
		}
		if *enabledStr != "" {
			b := *enabledStr == "true" || *enabledStr == "1"
			patch.Enabled = &b
		}
		if err := apps.UpsertT3Provider("", patch); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Updated T3 provider instance %s (sensitive env entries preserved)\n", fs.Arg(0))
		return 0
	case "rm":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: liv apps t3 rm <instance-id>")
			return 2
		}
		if err := apps.DeleteT3Provider("", args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Removed T3 provider instance %s\n", args[1])
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: liv apps t3 <list|edit|rm>")
		return 2
	}
}

func cmdAppsOpenClaw(args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		ov := apps.LoadOpenClawOverview("")
		for _, p := range ov.Profiles {
			fmt.Printf("  %-14s %s  (mcp:%d plugins:%d skills:%d)\n", p.Name, p.Dir, len(p.MCPServers), len(p.Plugins), len(p.Skills))
		}
		return 0
	case "create-profile":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: liv apps openclaw create-profile <name>")
			return 2
		}
		cfg, err := apps.CreateOpenClawProfile("", args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Created OpenClaw profile %q at %s\n", args[1], cfg)
		return 0
	case "set-plugin":
		fs := flag.NewFlagSet("apps openclaw set-plugin", flag.ContinueOnError)
		prof := fs.String("profile", "default", "openclaw profile name")
		enabled := fs.Bool("enabled", true, "enable or disable plugin")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv apps openclaw set-plugin [--profile default] [--enabled=true|false] <plugin>")
			return 2
		}
		if err := apps.SetOpenClawPlugin("", *prof, fs.Arg(0), *enabled); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("OpenClaw[%s] plugin %s enabled=%v\n", *prof, fs.Arg(0), *enabled)
		return 0
	case "set-mcp":
		fs := flag.NewFlagSet("apps openclaw set-mcp", flag.ContinueOnError)
		prof := fs.String("profile", "default", "openclaw profile name")
		command := fs.String("command", "", "stdio command")
		argStr := fs.String("args", "", "space-separated args")
		url := fs.String("url", "", "http url")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv apps openclaw set-mcp [--profile default] <name> --command <bin> [--args '...']")
			return 2
		}
		if err := apps.SetOpenClawMCP("", *prof, apps.OpenClawMCPView{
			Name: fs.Arg(0), Command: *command, Args: strings.Fields(*argStr), URL: *url,
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Updated OpenClaw[%s] MCP server %s\n", *prof, fs.Arg(0))
		return 0
	case "rm-mcp":
		fs := flag.NewFlagSet("apps openclaw rm-mcp", flag.ContinueOnError)
		prof := fs.String("profile", "default", "openclaw profile name")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: liv apps openclaw rm-mcp [--profile default] <name>")
			return 2
		}
		if err := apps.DeleteOpenClawMCP("", *prof, fs.Arg(0)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("Removed OpenClaw[%s] MCP server %s\n", *prof, fs.Arg(0))
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: liv apps openclaw <list|create-profile|set-plugin|set-mcp|rm-mcp>")
		return 2
	}
}

// --- Herdr mod checksum & archive export helpers ------------------------------------------------

func cmdHerdrChecksum(args []string) int {
	fs := flag.NewFlagSet("herdr checksum", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON manifest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	m, err := herdr.BundleManifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv herdr checksum:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
		return 0
	}
	fmt.Printf("%s  %s\n", m.BundleSHA256, m.Mod)
	for _, k := range model.SortStringsCopy(mapKeys(m.Files)) {
		fmt.Printf("  %s  %s\n", m.Files[k], k)
	}
	return 0
}

func cmdHerdrExportMod(args []string) int {
	fs := flag.NewFlagSet("herdr export-mod", flag.ContinueOnError)
	outPath := fs.String("out", "dist/livery-launcher-mod.tar.gz", "output tar.gz path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sha, m, err := herdr.ExportModArchive(*outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "liv herdr export-mod:", err)
		return 1
	}
	fmt.Printf("%s  %s (bundle_sha256=%s)\n", sha, *outPath, m.BundleSHA256)
	return 0
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
