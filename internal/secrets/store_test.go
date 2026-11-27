package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStorePermissionsAndHMAC(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(WithBackend("file"), WithStateRoot(dir))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	secretVal := "sk-super-secret-do-not-leak-12345"
	if err := st.SetManaged("test-token", secretVal); err != nil {
		t.Fatalf("SetManaged: %v", err)
	}
	got, err := st.Resolve("store:test-token", nil)
	if err != nil || got != secretVal {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if !st.Exists("store:test-token", nil) {
		t.Fatalf("expected Exists=true")
	}

	// Verify 0600 file permissions and 0700 dir permissions
	fi, err := os.Stat(filepath.Join(dir, "secrets", "test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %o, want 0600", fi.Mode().Perm())
	}

	// Refuse overly permissive file (0644) and verify error never contains secret value
	if err := os.Chmod(filepath.Join(dir, "secrets", "test-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = st.Resolve("store:test-token", nil)
	if err == nil {
		t.Fatalf("expected error on 0644 secret file")
	}
	if strings.Contains(err.Error(), secretVal) {
		t.Fatalf("secret value leaked in error message")
	}
	_ = os.Chmod(filepath.Join(dir, "secrets", "test-token"), 0o600)

	// HMAC parity check
	nonce := []byte("ephemeral-nonce-32-bytes-example")
	h1 := ComputeHMAC(nonce, secretVal)
	h2 := ComputeHMAC(nonce, secretVal)
	h3 := ComputeHMAC(nonce, "other-value")
	if h1 != h2 || h1 == h3 || strings.Contains(h1, secretVal) {
		t.Fatalf("unexpected HMAC result")
	}

	recs := st.ListManaged()
	if len(recs) != 1 || recs[0].Name != "test-token" || !recs[0].Present {
		t.Fatalf("ListManaged = %+v", recs)
	}
	ok, err := st.RemoveManaged("test-token")
	if err != nil || !ok {
		t.Fatalf("RemoveManaged = %v, %v", ok, err)
	}
	if st.Exists("store:test-token", nil) {
		t.Fatalf("secret still exists after RemoveManaged")
	}
}
