package model

import (
	"testing"
)

func TestMatrixGuardrailsSelectAllAndDuplicateProfile(t *testing.T) {
	root := copyRepoFixture(t)

	// 1. Direct skill toggle on ops@server (on -> off)
	if conf, err := SetMatrixCell(root, "skills", "beta", "ops@server", true); err != nil || conf != nil {
		t.Fatalf("enable direct skill failed: conf=%+v err=%v", conf, err)
	}
	repo, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	ops, _ := repo.Target("ops@server")
	items, src := repo.ResolveHostCategoryItems(ops, "skills")
	if !containsStr(items, "beta") || src["beta"] != "direct" {
		t.Fatalf("expected direct beta on ops@server, got items=%v src=%v", items, src)
	}
	if conf, err := SetMatrixCell(root, "skills", "beta", "ops@server", false); err != nil || conf != nil {
		t.Fatalf("disable direct skill failed: conf=%+v err=%v", conf, err)
	}

	// 2. Profile guardrail: attempting to disable a prompt inherited from `base` must return MatrixConflict!
	conf, err := SetMatrixCell(root, "prompts", "identity/owner", "ops@server", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf == nil || conf.Profile != "base" {
		t.Fatalf("expected MatrixConflict for profile base, got %+v", conf)
	}

	// 3. Duplicate profile without that item & swap onto ops@server
	if err := DuplicateProfileWithoutItem(root, "ops@server", "base", "base-no-id", "prompts", "identity/owner"); err != nil {
		t.Fatalf("DuplicateProfileWithoutItem: %v", err)
	}
	repo2, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	ops2, _ := repo2.Target("ops@server")
	if !containsStr(ops2.Profiles, "base-no-id") || containsStr(ops2.Profiles, "base") {
		t.Fatalf("expected ops@server switched to base-no-id, got %v", ops2.Profiles)
	}
	frags2, _ := repo2.ResolveHostCategoryItems(ops2, "prompts")
	if containsStr(frags2, "identity/owner") {
		t.Fatalf("expected identity/owner removed in base-no-id, still got %v", frags2)
	}

	// 4. Row Select All across all host environments (Matrix is host-only; projects live in Skillshare)
	envs := repo2.BuildMatrixEnvs()
	for _, e := range envs {
		if e.Kind != "host" {
			t.Fatalf("expected host-only matrix envs, got %+v", e)
		}
	}
	updated, skipped, err := SetMatrixRow(root, "skills", "beta", true)
	if err != nil {
		t.Fatalf("SetMatrixRow enableAll: %v", err)
	}
	if len(updated) != 2 || len(skipped) != 0 {
		t.Fatalf("expected 2 updated host envs and 0 skipped, got updated=%v skipped=%v", updated, skipped)
	}

	// 5. SaveProfile & DeleteProfile (detaching cleanly from hosts)
	if err := SaveProfile(root, "temp-prof", "Temp", nil, []string{"style/concise"}, []string{"alpha"}, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := SetMatrixCell(root, "profiles", "temp-prof", "ops@server", true); err != nil {
		t.Fatalf("attach temp-prof: %v", err)
	}
	if err := DeleteProfile(root, "temp-prof"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	repo3, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := repo3.Profiles["temp-prof"]; exists {
		t.Fatalf("temp-prof should be deleted")
	}
	ops3, _ := repo3.Target("ops@server")
	if containsStr(ops3.Profiles, "temp-prof") {
		t.Fatalf("temp-prof should be detached from ops@server")
	}

	// 6. Global prompt fragment ranking (`prompts/order.toml`) governs compilation order across profiles & direct toggles
	savedOrder, err := SavePromptOrder(root, []string{"style/concise", "identity/owner"})
	if err != nil {
		t.Fatalf("SavePromptOrder: %v", err)
	}
	if len(savedOrder) < 2 || savedOrder[0] != "style/concise" || savedOrder[1] != "identity/owner" {
		t.Fatalf("unexpected saved order: %v", savedOrder)
	}
	repo4, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	dev4, _ := repo4.Target("dev@macbook")
	// Directly enable style/concise if not already enabled; even though identity/owner is in base profile,
	// global rank puts style/concise (#1) ahead of identity/owner (#2)!
	_, _ = SetMatrixCell(root, "prompts", "style/concise", "dev@macbook", true)
	repo4, _ = LoadRepo(root)
	dev4, _ = repo4.Target("dev@macbook")
	resolvedFrags, _, err := repo4.Resolve(dev4)
	if err != nil {
		t.Fatalf("Resolve with FragmentOrder: %v", err)
	}
	idxStyle, idxId := -1, -1
	for i, fid := range resolvedFrags {
		if fid == "style/concise" {
			idxStyle = i
		} else if fid == "identity/owner" {
			idxId = i
		}
	}
	if idxStyle == -1 || idxId == -1 || idxStyle > idxId {
		t.Fatalf("expected style/concise before identity/owner per global rank, got %v", resolvedFrags)
	}
}
