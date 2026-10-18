// Package updaterollback puts the previous version back when an in-app update
// installs a build that cannot start.
//
// The Wails updater already verifies every download (SHA256SUMS plus the
// release's ed25519 signature, fail-closed) and restores its own backup if the
// binary swap or the first launch of the new executable fails outright. What
// it cannot catch is a new build that launches and then dies before the window
// is usable: by then the helper has deleted its backup and the user is left
// with an app that closes every time they open it, and no in-app way back.
//
// This package covers that gap:
//
//  1. Arm, called just before the updater restarts into a staged update, keeps
//     a copy of the executable that is still running (the old version) next to
//     it as "<exe>.previous" and records the version pair.
//  2. Boot, called at the start of every GUI launch, counts launches of the new
//     version that never became healthy. After maxUnhealthyBoots of them it
//     swaps "<exe>.previous" back into place and asks the caller to relaunch.
//  3. MarkHealthy, called once the new version's window has loaded (or it has
//     simply stayed up for a while), disarms everything and deletes the copy.
//
// Nothing here downloads or trusts anything new: the only binary it can ever
// restore is the one the user was already running.
package updaterollback

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	stateFileName  = "TcNo-Acc-Switcher.updateRollback.json"
	previousSuffix = ".previous"
	failedInfix    = ".failed-"

	// maxUnhealthyBoots is how many launches of a freshly installed version may
	// fail to reach a healthy state before the previous version is restored.
	// Two, not one: a single failed launch is as likely to be the PC (a crash
	// during shutdown, a forced reboot) as the build.
	maxUnhealthyBoots = 2
)

// ToastRolledBack is the i18n toast shown on the first launch after a rollback.
const ToastRolledBack = "i18n:Toast_UpdateRolledBack"

type state struct {
	FromVersion    string `json:"fromVersion"`
	ToVersion      string `json:"toVersion"`
	ArmedAt        string `json:"armedAt"`
	UnhealthyBoots int    `json:"unhealthyBoots"`
	RolledBack     bool   `json:"rolledBack,omitempty"`
	RolledBackAt   string `json:"rolledBackAt,omitempty"`
}

// Decision tells the caller of Boot what to do next.
type Decision struct {
	// Relaunch is true when the previous version was just restored: the caller
	// should start the executable again and exit without opening a window.
	Relaunch bool
	// Toast, when set, is an i18n toast key to show once the window is up.
	Toast string
}

var mu sync.Mutex

// Supported reports whether rollback can run on this platform. macOS updates
// replace a whole .app bundle, which a copy of the inner binary cannot undo,
// and an AppImage is replaced as a single file the running process does not
// see as its own executable.
func Supported() bool {
	switch runtime.GOOS {
	case "windows":
		return true
	case "linux":
		return strings.TrimSpace(os.Getenv("APPIMAGE")) == ""
	default:
		return false
	}
}

// ExecutablePath is os.Executable with symlinks resolved, matching the path
// the Wails update helper swaps.
func ExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

func statePath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), stateFileName)
}

func previousPath(exePath string) string { return exePath + previousSuffix }

func readState(exePath string) (state, bool, error) {
	data, err := os.ReadFile(statePath(exePath))
	if errors.Is(err, os.ErrNotExist) {
		return state{}, false, nil
	}
	if err != nil {
		return state{}, false, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		// A state file we cannot read is one we cannot act on safely.
		return state{}, false, nil
	}
	return st, true, nil
}

func writeState(exePath string, st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(statePath(exePath), data, 0o644)
}

func disarm(exePath string) {
	_ = os.Remove(previousPath(exePath))
	_ = os.Remove(statePath(exePath))
	sweepFailed(exePath)
}

// sweepFailed removes executables set aside by an earlier rollback. On Windows
// one of them may still be running (the process that performed the rollback),
// so failures are ignored and retried next time.
func sweepFailed(exePath string) {
	matches, _ := filepath.Glob(exePath + failedInfix + "*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// Arm keeps a copy of the running executable so Boot can restore it if the
// update about to be applied does not start. Call it after the update is
// staged and before the updater restarts.
func Arm(exePath, fromVersion, toVersion string) error {
	mu.Lock()
	defer mu.Unlock()
	fromVersion = strings.TrimPrefix(strings.TrimSpace(fromVersion), "v")
	toVersion = strings.TrimPrefix(strings.TrimSpace(toVersion), "v")
	if exePath == "" || toVersion == "" || fromVersion == toVersion {
		return nil
	}
	if err := copyFileAtomic(exePath, previousPath(exePath)); err != nil {
		_ = os.Remove(previousPath(exePath))
		return fmt.Errorf("updaterollback: keep previous version: %w", err)
	}
	return writeState(exePath, state{
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		ArmedAt:     time.Now().UTC().Format(time.RFC3339),
	})
}

// Boot records one more launch of currentVersion and, when the freshly
// installed version has failed to become healthy too many times in a row,
// restores the previous executable.
func Boot(exePath, currentVersion string) (Decision, error) {
	mu.Lock()
	defer mu.Unlock()
	currentVersion = strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	st, ok, err := readState(exePath)
	if err != nil || !ok {
		return Decision{}, err
	}

	if st.RolledBack {
		// Running the restored version: tell the user once, then forget it.
		// Anything else means a different build was installed since, and the
		// note no longer applies to it.
		disarm(exePath)
		if currentVersion == st.FromVersion {
			return Decision{Toast: ToastRolledBack}, nil
		}
		return Decision{}, nil
	}

	if currentVersion != st.ToVersion {
		// The update never took (the helper restored its own backup), or the
		// user installed something else by hand. Either way the kept copy no
		// longer describes what is installed.
		disarm(exePath)
		return Decision{}, nil
	}

	if st.UnhealthyBoots < maxUnhealthyBoots {
		st.UnhealthyBoots++
		return Decision{}, writeState(exePath, st)
	}

	if _, err := os.Stat(previousPath(exePath)); err != nil {
		disarm(exePath)
		return Decision{}, nil
	}
	if err := restorePrevious(exePath); err != nil {
		return Decision{}, err
	}
	st.RolledBack = true
	st.RolledBackAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeState(exePath, st); err != nil {
		return Decision{Relaunch: true}, err
	}
	return Decision{Relaunch: true}, nil
}

// MarkHealthy disarms rollback for the running version. Safe to call often.
func MarkHealthy(exePath, currentVersion string) {
	mu.Lock()
	defer mu.Unlock()
	currentVersion = strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	st, ok, err := readState(exePath)
	if err != nil {
		return
	}
	if !ok {
		sweepFailed(exePath)
		return
	}
	if st.RolledBack {
		// Boot has not seen the restored version yet; leave the toast for it.
		return
	}
	if st.ToVersion == currentVersion {
		disarm(exePath)
	}
}

// restorePrevious swaps "<exe>.previous" into place. The running executable is
// renamed aside rather than overwritten, which Windows allows for a running
// image, and is put back if the copy cannot be completed.
func restorePrevious(exePath string) error {
	aside := fmt.Sprintf("%s%s%d", exePath, failedInfix, time.Now().UnixNano())
	if err := os.Rename(exePath, aside); err != nil {
		return fmt.Errorf("updaterollback: set failed version aside: %w", err)
	}
	if err := copyFileAtomic(previousPath(exePath), exePath); err != nil {
		if rerr := os.Rename(aside, exePath); rerr != nil {
			return fmt.Errorf("updaterollback: restore previous: %v (and could not undo: %v)", err, rerr)
		}
		return fmt.Errorf("updaterollback: restore previous: %w", err)
	}
	_ = os.Remove(previousPath(exePath))
	return nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
