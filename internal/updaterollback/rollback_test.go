package updaterollback

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "TcNo-Acc-Switcher.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// simulateSwap is what the Wails helper does after Arm: the new build lands
// where the old one was.
func simulateSwap(t *testing.T, exe string) {
	t.Helper()
	if err := os.WriteFile(exe, []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestHealthyUpdateCleansUp(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.8", "v4.0.9"); err != nil {
		t.Fatal(err)
	}
	if read(t, previousPath(exe)) != "old build" {
		t.Fatal("previous copy does not hold the old build")
	}
	simulateSwap(t, exe)
	d, err := Boot(exe, "4.0.9")
	if err != nil || d.Relaunch || d.Toast != "" {
		t.Fatalf("first boot: %+v %v", d, err)
	}
	MarkHealthy(exe, "4.0.9")
	if exists(previousPath(exe)) || exists(statePath(exe)) {
		t.Fatal("healthy boot left rollback files behind")
	}
	if d, _ := Boot(exe, "4.0.9"); d.Relaunch {
		t.Fatal("rolled back after a healthy start")
	}
}

func TestCrashLoopRestoresPreviousVersion(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.8", "4.0.9"); err != nil {
		t.Fatal(err)
	}
	simulateSwap(t, exe)
	for i := 0; i < maxUnhealthyBoots; i++ {
		d, err := Boot(exe, "4.0.9")
		if err != nil || d.Relaunch {
			t.Fatalf("boot %d: %+v %v", i+1, d, err)
		}
	}
	d, err := Boot(exe, "4.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Relaunch {
		t.Fatal("did not roll back after repeated failed starts")
	}
	if got := read(t, exe); got != "old build" {
		t.Fatalf("exe = %q, want the old build back", got)
	}
	matches, _ := filepath.Glob(exe + failedInfix + "*")
	if len(matches) != 1 || read(t, matches[0]) != "new build" {
		t.Fatalf("failed build was not set aside: %v", matches)
	}
	// The restored (old) version starts and shows the toast once.
	d, err = Boot(exe, "4.0.8")
	if err != nil || d.Relaunch || d.Toast != ToastRolledBack {
		t.Fatalf("restored boot: %+v %v", d, err)
	}
	if exists(statePath(exe)) {
		t.Fatal("state survived the restored boot")
	}
	if d, _ := Boot(exe, "4.0.8"); d.Toast != "" {
		t.Fatal("toast shown twice")
	}
}

func TestVersionMismatchDisarms(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.8", "4.0.9"); err != nil {
		t.Fatal(err)
	}
	// The helper restored its own backup, so the old version is what starts.
	d, err := Boot(exe, "4.0.8")
	if err != nil || d.Relaunch || d.Toast != "" {
		t.Fatalf("%+v %v", d, err)
	}
	if exists(previousPath(exe)) || exists(statePath(exe)) {
		t.Fatal("stale rollback files kept")
	}
}

func TestNoStateIsNoop(t *testing.T) {
	exe := setup(t)
	d, err := Boot(exe, "4.0.9")
	if err != nil || d.Relaunch || d.Toast != "" {
		t.Fatalf("%+v %v", d, err)
	}
	MarkHealthy(exe, "4.0.9")
	if read(t, exe) != "old build" {
		t.Fatal("exe touched")
	}
}

func TestArmSameVersionIsNoop(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.9", "v4.0.9"); err != nil {
		t.Fatal(err)
	}
	if exists(previousPath(exe)) || exists(statePath(exe)) {
		t.Fatal("armed for a reinstall of the same version")
	}
}

func TestMissingPreviousCopyDoesNotBreakStartup(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.8", "4.0.9"); err != nil {
		t.Fatal(err)
	}
	simulateSwap(t, exe)
	_ = os.Remove(previousPath(exe))
	for i := 0; i <= maxUnhealthyBoots; i++ {
		d, err := Boot(exe, "4.0.9")
		if err != nil || d.Relaunch {
			t.Fatalf("boot %d: %+v %v", i+1, d, err)
		}
	}
	if read(t, exe) != "new build" {
		t.Fatal("exe replaced without a previous copy")
	}
}

func TestCorruptStateIsIgnored(t *testing.T) {
	exe := setup(t)
	if err := os.WriteFile(statePath(exe), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Boot(exe, "4.0.9")
	if err != nil || d.Relaunch {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestRolledBackNoteIgnoredByOtherVersions(t *testing.T) {
	exe := setup(t)
	if err := Arm(exe, "4.0.8", "4.0.9"); err != nil {
		t.Fatal(err)
	}
	simulateSwap(t, exe)
	for i := 0; i <= maxUnhealthyBoots; i++ {
		if _, err := Boot(exe, "4.0.9"); err != nil {
			t.Fatal(err)
		}
	}
	// The user installs 4.1.0 by hand before the restored build ever starts.
	d, err := Boot(exe, "4.1.0")
	if err != nil || d.Toast != "" || d.Relaunch {
		t.Fatalf("%+v %v", d, err)
	}
}
