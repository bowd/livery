package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	DefaultKeychainService = "livery-secret"
	LegacyKeychainService  = "agent-config-secret"
)

var (
	SecretNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	EnvNameRe       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	T3SecretFileRe  = regexp.MustCompile(`^provider-env-([A-Za-z0-9_-]+)-([A-Za-z0-9_-]+)\.bin$`)
	slugCleanRe     = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	ValidRefSchemes = []string{"store", "keychain", "file", "env"}
)

type SecretError struct {
	Msg string
}

func (e *SecretError) Error() string { return e.Msg }

func errf(format string, args ...any) error {
	return &SecretError{Msg: fmt.Sprintf(format, args...)}
}

func ExpandPath(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func DefaultStateDir() string {
	if env := strings.TrimSpace(os.Getenv("LIVERY_STATE_DIR")); env != "" {
		return ExpandPath(env)
	}
	if env := strings.TrimSpace(os.Getenv("AGENT_LAUNCH_STATE_DIR")); env != "" {
		return ExpandPath(env)
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	} else {
		base = ExpandPath(base)
	}
	// Prefer existing legacy state dir if livery dir doesn't exist yet so already-stored Linux secrets work seamlessly
	livDir := filepath.Join(base, "livery")
	legDir := filepath.Join(base, "agent-config")
	if _, err := os.Stat(filepath.Join(livDir, "secrets")); err == nil {
		return livDir
	}
	if _, err := os.Stat(filepath.Join(legDir, "secrets")); err == nil {
		return legDir
	}
	return livDir
}

func ParseRef(ref string) (scheme, target string, err error) {
	idx := strings.IndexByte(ref, ':')
	if idx <= 0 {
		return "", "", errf("invalid secret ref %q: expected <scheme>:<target>", ref)
	}
	scheme = ref[:idx]
	target = ref[idx+1:]
	valid := false
	for _, s := range ValidRefSchemes {
		if s == scheme {
			valid = true
			break
		}
	}
	if !valid {
		return "", "", errf("unsupported secret scheme %q in %q (supported: %s)", scheme, ref, strings.Join(ValidRefSchemes, ", "))
	}
	if target == "" {
		return "", "", errf("empty secret target in %q", ref)
	}
	for _, r := range target {
		if r < 32 {
			return "", "", errf("invalid control character in secret ref %q", ref)
		}
	}
	switch scheme {
	case "store":
		if !SecretNameRe.MatchString(target) {
			return "", "", errf("invalid store secret name %q", target)
		}
	case "keychain":
		svc, acc, ok := strings.Cut(target, "/")
		if !ok || strings.TrimSpace(svc) == "" || strings.TrimSpace(acc) == "" {
			return "", "", errf("keychain ref %q must be keychain:<service>/<account>", ref)
		}
	case "file":
		if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "~/") {
			return "", "", errf("file secret ref %q must be an absolute or ~/ path", ref)
		}
	case "env":
		if !EnvNameRe.MatchString(target) {
			return "", "", errf("env secret ref %q must name a valid environment variable", ref)
		}
	}
	return scheme, target, nil
}

type Store struct {
	Backend         string
	StateRoot       string
	SecretsDir      string
	IndexPath       string
	KeychainService string
	LegacyServices  []string
}

type StoreOption func(*Store)

func WithBackend(b string) StoreOption   { return func(s *Store) { s.Backend = b } }
func WithStateRoot(r string) StoreOption { return func(s *Store) { s.StateRoot = r } }
func WithKeychainService(svc string) StoreOption {
	return func(s *Store) { s.KeychainService = svc }
}

func NewStore(opts ...StoreOption) (*Store, error) {
	s := &Store{}
	for _, opt := range opts {
		opt(s)
	}
	if s.Backend == "" {
		envB := strings.TrimSpace(os.Getenv("LIVERY_SECRET_BACKEND"))
		if envB == "" {
			envB = strings.TrimSpace(os.Getenv("AGENT_LAUNCH_SECRET_BACKEND"))
		}
		if envB != "" {
			s.Backend = strings.ToLower(envB)
		} else if runtime.GOOS == "darwin" {
			s.Backend = "keychain"
		} else {
			s.Backend = "file"
		}
	}
	if s.Backend != "keychain" && s.Backend != "file" {
		return nil, errf("unsupported secret backend %q (expected 'keychain' or 'file')", s.Backend)
	}
	if s.StateRoot == "" {
		s.StateRoot = DefaultStateDir()
	}
	s.SecretsDir = filepath.Join(s.StateRoot, "secrets")
	s.IndexPath = filepath.Join(s.SecretsDir, ".index.json")
	if s.KeychainService == "" {
		if svc := strings.TrimSpace(os.Getenv("LIVERY_KEYCHAIN_SERVICE")); svc != "" {
			s.KeychainService = svc
		} else if svc := strings.TrimSpace(os.Getenv("AGENT_LAUNCH_KEYCHAIN_SERVICE")); svc != "" {
			s.KeychainService = svc
		} else {
			// Default to legacy service for seamless compatibility with existing macOS Keychain items
			s.KeychainService = LegacyKeychainService
			s.LegacyServices = []string{DefaultKeychainService}
		}
	}
	return s, nil
}

func (s *Store) ensureSecretsDir() error {
	if err := os.MkdirAll(s.SecretsDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.SecretsDir, 0o700); err != nil {
		return err
	}
	lst, err := os.Lstat(s.SecretsDir)
	if err != nil {
		return err
	}
	if lst.Mode()&os.ModeSymlink != 0 || !lst.IsDir() {
		return errf("secrets directory %s must be a real directory", s.SecretsDir)
	}
	if lst.Mode().Perm()&0o077 != 0 {
		return errf("secrets directory %s has insecure permissions (%o)", s.SecretsDir, lst.Mode().Perm())
	}
	return nil
}

type indexEntry struct {
	Backend   string `json:"backend"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) loadIndex() map[string]indexEntry {
	out := map[string]indexEntry{}
	b, err := os.ReadFile(s.IndexPath)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Store) saveIndex(idx map[string]indexEntry) error {
	if err := s.ensureSecretsDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writePrivateAtomic(s.IndexPath, b, 0o600)
}

func writePrivateAtomic(dest string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".livery-sec-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *Store) keychainGet(service, account string) (string, error) {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w")
		out, err := cmd.Output()
		if err != nil {
			return "", errf("keychain item %s/%s not found", service, account)
		}
		raw := strings.TrimRight(string(out), "\r\n")
		if strings.HasPrefix(raw, "b64:") {
			dec, err := base64.StdEncoding.DecodeString(raw[4:])
			if err != nil {
				return "", errf("keychain item %s/%s is corrupted", service, account)
			}
			return string(dec), nil
		}
		return raw, nil
	}
	if st, err := exec.LookPath("secret-tool"); err == nil {
		cmd := exec.Command(st, "lookup", "service", service, "account", account)
		out, err := cmd.Output()
		if err != nil {
			return "", errf("secret-tool item %s/%s not found", service, account)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", errf("keychain backend unavailable on %s", runtime.GOOS)
}

func (s *Store) keychainSet(service, account, value string) error {
	if runtime.GOOS != "darwin" {
		return errf("keychain write is only supported on macOS")
	}
	encoded := "b64:" + base64.StdEncoding.EncodeToString([]byte(value))
	// Strictly pass via `security -i` stdin so the secret NEVER appears in process argv (`ps`).
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -l livery:%s -w %s\n", service, account, account, encoded)
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(cmdLine)
	if err := cmd.Run(); err != nil {
		return errf("failed to store secret %q in macOS Keychain", account)
	}
	return nil
}

func (s *Store) keychainRemove(service, account string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	cmd := exec.Command("security", "delete-generic-password", "-s", service, "-a", account)
	return cmd.Run() == nil
}

func ReadPrivateFile(pathStr string, allowSymlink bool) (string, error) {
	p := ExpandPath(pathStr)
	lst, err := os.Lstat(p)
	if err != nil {
		return "", errf("secret file %s does not exist", p)
	}
	if !allowSymlink && lst.Mode()&os.ModeSymlink != 0 {
		return "", errf("refusing symlinked secret file %s", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", errf("secret file %s cannot be read", p)
	}
	if !st.Mode().IsRegular() {
		return "", errf("secret path %s is not a regular file", p)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", errf("refusing secret file %s with permissive mode %04o (run chmod 600)", p, st.Mode().Perm())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	val := strings.TrimRight(string(b), "\r\n")
	if val == "" {
		return "", errf("secret file %s is empty", p)
	}
	return val, nil
}

func (s *Store) SetManaged(name, value string) error {
	if !SecretNameRe.MatchString(name) {
		return errf("invalid secret name %q", name)
	}
	if value == "" {
		return errf("secret value must not be empty")
	}
	if s.Backend == "keychain" {
		if err := s.keychainSet(s.KeychainService, name, value); err != nil {
			return err
		}
	} else {
		if err := s.ensureSecretsDir(); err != nil {
			return err
		}
		dest := filepath.Join(s.SecretsDir, name)
		if lst, err := os.Lstat(dest); err == nil && lst.Mode()&os.ModeSymlink != 0 {
			return errf("refusing to overwrite symlink at %s", dest)
		}
		if err := writePrivateAtomic(dest, []byte(value), 0o600); err != nil {
			return err
		}
	}
	idx := s.loadIndex()
	idx[name] = indexEntry{
		Backend:   s.Backend,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return s.saveIndex(idx)
}

func (s *Store) RemoveManaged(name string) (bool, error) {
	if !SecretNameRe.MatchString(name) {
		return false, errf("invalid secret name %q", name)
	}
	removed := false
	if s.Backend == "keychain" {
		removed = s.keychainRemove(s.KeychainService, name)
		for _, alt := range s.LegacyServices {
			if s.keychainRemove(alt, name) {
				removed = true
			}
		}
	} else {
		dest := filepath.Join(s.SecretsDir, name)
		if err := os.Remove(dest); err == nil {
			removed = true
		}
	}
	idx := s.loadIndex()
	if _, ok := idx[name]; ok {
		delete(idx, name)
		_ = s.saveIndex(idx)
	}
	return removed, nil
}

type ManagedRecord struct {
	Name      string `json:"name"`
	Backend   string `json:"backend"`
	Present   bool   `json:"present"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) ListManaged() []ManagedRecord {
	idx := s.loadIndex()
	names := map[string]bool{}
	for k := range idx {
		names[k] = true
	}
	if s.Backend == "file" {
		if entries, err := os.ReadDir(s.SecretsDir); err == nil {
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".") && SecretNameRe.MatchString(e.Name()) {
					names[e.Name()] = true
				}
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	out := make([]ManagedRecord, 0, len(sorted))
	for _, n := range sorted {
		meta := idx[n]
		bk := meta.Backend
		if bk == "" {
			bk = s.Backend
		}
		out = append(out, ManagedRecord{
			Name:      n,
			Backend:   bk,
			Present:   s.Exists("store:"+n, nil),
			UpdatedAt: meta.UpdatedAt,
		})
	}
	return out
}

func (s *Store) Resolve(ref string, callerEnv map[string]string) (string, error) {
	scheme, target, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	switch scheme {
	case "store":
		if s.Backend == "keychain" {
			val, err := s.keychainGet(s.KeychainService, target)
			if err == nil {
				return val, nil
			}
			for _, alt := range s.LegacyServices {
				if v, e2 := s.keychainGet(alt, target); e2 == nil {
					return v, nil
				}
			}
			return "", err
		}
		return ReadPrivateFile(filepath.Join(s.SecretsDir, target), false)
	case "keychain":
		svc, acc, _ := strings.Cut(target, "/")
		return s.keychainGet(svc, acc)
	case "file":
		return ReadPrivateFile(target, true)
	case "env":
		var val string
		if callerEnv != nil {
			val = callerEnv[target]
		} else {
			val = os.Getenv(target)
		}
		if val == "" {
			return "", errf("environment variable %s is not set (referenced by %q)", target, ref)
		}
		return val, nil
	}
	return "", errf("unsupported secret ref %q", ref)
}

func (s *Store) Exists(ref string, callerEnv map[string]string) bool {
	val, err := s.Resolve(ref, callerEnv)
	return err == nil && val != ""
}

// ComputeHMAC returns HMAC-SHA256(nonce, value) hex for cross-host secret parity checks (never exposing plaintext).
func ComputeHMAC(nonce []byte, value string) string {
	mac := hmac.New(sha256.New, nonce)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// --- T3 Code safe no-echo secret discovery & import ---------------------------------------------

type T3SecretMeta struct {
	InstanceID    string `json:"instance_id"`
	EnvVar        string `json:"env_var"`
	Label         string `json:"label"`
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	Mode          string `json:"mode"`
	ModeOK        bool   `json:"mode_ok"`
	SuggestedName string `json:"suggested_name"`
}

type T3ImportResult struct {
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
	Action  string   `json:"action"`
	Backend string   `json:"backend"`
}

func b64urlDecode(s string) string {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return string(b)
	}
	return s
}

func SuggestT3SecretName(instanceID, envVar, baseURL string) string {
	switch envVar {
	case "COMPANY_GATEWAY_API_KEY":
		return "company-gateway-api-key"
	case "SPLASH_API_KEY":
		return "splash-api-key"
	case "ANTHROPIC_AUTH_TOKEN":
		if strings.Contains(baseURL, "company-gateway") {
			return "company-gateway-api-key"
		}
		if strings.Contains(strings.ToLower(instanceID), "qwen") {
			return "qwen-claude-token"
		}
	}
	cleaned := strings.Trim(slugCleanRe.ReplaceAllString(strings.ToLower(instanceID+"-"+envVar), "-"), "-")
	if len(cleaned) > 120 {
		cleaned = cleaned[:120]
	}
	if cleaned == "" {
		return "secret"
	}
	return cleaned
}

func loadT3BaseURLs(t3Dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(t3Dir, "settings.json"))
	if err != nil {
		return out
	}
	var doc struct {
		ProviderInstances map[string]struct {
			Environment []struct {
				Name      string `json:"name"`
				Value     string `json:"value"`
				Sensitive bool   `json:"sensitive"`
			} `json:"environment"`
		} `json:"providerInstances"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return out
	}
	for iid, inst := range doc.ProviderInstances {
		for _, ev := range inst.Environment {
			if ev.Name == "ANTHROPIC_BASE_URL" && !ev.Sensitive && strings.TrimSpace(ev.Value) != "" {
				out[iid] = strings.TrimSpace(ev.Value)
			}
		}
	}
	return out
}

func DiscoverT3Secrets(t3Dir string) []T3SecretMeta {
	if t3Dir == "" {
		home, _ := os.UserHomeDir()
		t3Dir = filepath.Join(home, ".t3", "userdata")
	} else {
		t3Dir = ExpandPath(t3Dir)
	}
	sdir := filepath.Join(t3Dir, "secrets")
	entries, err := os.ReadDir(sdir)
	if err != nil {
		return nil
	}
	baseURLs := loadT3BaseURLs(t3Dir)
	var out []T3SecretMeta
	for _, e := range entries {
		m := T3SecretFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		instID := b64urlDecode(m[1])
		envVar := b64urlDecode(m[2])
		p := filepath.Join(sdir, e.Name())
		lst, err := os.Lstat(p)
		if err != nil {
			continue
		}
		isReg := lst.Mode().IsRegular() && lst.Mode()&os.ModeSymlink == 0
		modeOK := isReg && (lst.Mode().Perm()&0o077 == 0)
		var sz int64
		if isReg {
			sz = lst.Size()
		}
		out = append(out, T3SecretMeta{
			InstanceID:    instID,
			EnvVar:        envVar,
			Label:         instID + "/" + envVar,
			Path:          p,
			Size:          sz,
			Mode:          fmt.Sprintf("0o%o", lst.Mode().Perm()),
			ModeOK:        modeOK,
			SuggestedName: SuggestT3SecretName(instID, envVar, baseURLs[instID]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func ImportT3Secrets(store *Store, t3Dir string, mappings map[string]string, dryRun bool) ([]T3ImportResult, error) {
	discovered := DiscoverT3Secrets(t3Dir)
	byLabel := map[string]T3SecretMeta{}
	for _, d := range discovered {
		byLabel[d.Label] = d
	}
	type pair struct {
		rec  T3SecretMeta
		dest string
	}
	var selected []pair
	if len(mappings) > 0 {
		keys := make([]string, 0, len(mappings))
		for k := range mappings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, lbl := range keys {
			rec, ok := byLabel[lbl]
			if !ok {
				return nil, errf("T3 secret %q not found", lbl)
			}
			dest := mappings[lbl]
			if !SecretNameRe.MatchString(dest) {
				return nil, errf("invalid target secret name %q", dest)
			}
			selected = append(selected, pair{rec: rec, dest: dest})
		}
	} else {
		for _, rec := range discovered {
			selected = append(selected, pair{rec: rec, dest: rec.SuggestedName})
		}
	}
	type stagedItem struct {
		val     string
		sources []string
	}
	staged := map[string]*stagedItem{}
	for _, p := range selected {
		val, err := ReadPrivateFile(p.rec.Path, false)
		if err != nil {
			return nil, err
		}
		if cur, ok := staged[p.dest]; ok {
			if !hmac.Equal([]byte(cur.val), []byte(val)) {
				return nil, errf("conflict: %s and %s have different values but both map to store:%s",
					strings.Join(cur.sources, ", "), p.rec.Label, p.dest)
			}
			cur.sources = append(cur.sources, p.rec.Label)
		} else {
			staged[p.dest] = &stagedItem{val: val, sources: []string{p.rec.Label}}
		}
	}
	names := make([]string, 0, len(staged))
	for k := range staged {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []T3ImportResult
	for _, name := range names {
		st := staged[name]
		same := false
		if existing, err := store.Resolve("store:"+name, nil); err == nil {
			same = hmac.Equal([]byte(existing), []byte(st.val))
		}
		action := "imported"
		if same {
			action = "unchanged"
		} else if dryRun {
			action = "would-import"
		}
		if !dryRun && !same {
			if err := store.SetManaged(name, st.val); err != nil {
				return nil, err
			}
			rb, err := store.Resolve("store:"+name, nil)
			if err != nil || !hmac.Equal([]byte(rb), []byte(st.val)) {
				return nil, errf("verification failed after importing store:%s", name)
			}
		}
		out = append(out, T3ImportResult{
			Name:    name,
			Sources: st.sources,
			Action:  action,
			Backend: store.Backend,
		})
	}
	return out, nil
}
