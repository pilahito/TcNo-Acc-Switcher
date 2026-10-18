package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"

	"TcNo-Acc-Switcher/internal/appclient"
	"TcNo-Acc-Switcher/internal/crashlog"
	"TcNo-Acc-Switcher/internal/i18n"
	"TcNo-Acc-Switcher/internal/updatecheck"
	"TcNo-Acc-Switcher/internal/updaterollback"
)

var (
	launchCheckOnce       sync.Once
	updaterLastErrorStage atomic.Value // updater.Stage
)

const (
	AppUpdateAvailableEvent = "app-update-available"
	UpdateCheckFailedEvent  = "update-check-failed"

	wailsUpdateCheckTimeout   = 60 * time.Second
	apiUpdateCheckTimeout     = 15 * time.Second
	periodicUpdateCheckPeriod = 6 * time.Hour
)

type UpdateAvailablePayload struct {
	Message     string `json:"message"`
	DownloadURL string `json:"downloadUrl"`
}

func emitAppUpdateAvailable(message string) {
	app := application.Get()
	if app == nil {
		return
	}
	const downloadURL = "https://github.com/TCNOco/TcNo-Acc-Switcher/releases/latest"
	_ = app.Event.Emit(AppUpdateAvailableEvent, UpdateAvailablePayload{
		Message:     message,
		DownloadURL: downloadURL,
	})
	NotifyNative("tcno-update-available", "TcNo Account Switcher", message, map[string]interface{}{
		"type": "update-available",
		"url":  downloadURL,
	})
}

func emitUpdateCheckFailed() {
	app := application.Get()
	if app == nil {
		return
	}
	_ = app.Event.Emit(UpdateCheckFailedEvent, true)
}

func (*PlatformService) NotifyLaunchUpdateCheck() {
	launchCheckOnce.Do(func() {
		// The frontend calls this once it has loaded and can reach Go, which is
		// the best sign there is that a freshly installed update works.
		markUpdateHealthy()
		go runLaunchUpdateCheck()
	})
}

func markUpdateHealthy() {
	if !updaterollback.Supported() {
		return
	}
	exe, err := updaterollback.ExecutablePath()
	if err != nil {
		return
	}
	updaterollback.MarkHealthy(exe, appVersionFromBuildConfig())
}

// armUpdateRollback keeps a copy of the running (old) executable before the
// updater restarts into rel, so a build that cannot start can be undone.
func armUpdateRollback(app *application.App, rel *updater.Release) {
	if rel == nil || !updaterollback.Supported() {
		return
	}
	exe, err := updaterollback.ExecutablePath()
	if err != nil {
		return
	}
	if err := updaterollback.Arm(exe, appVersionFromBuildConfig(), rel.Version); err != nil && app != nil {
		// Not fatal: the update still installs, it just cannot be undone
		// automatically if it fails to start.
		app.Logger.Warn("update: could not keep previous version for rollback", "error", err)
	}
}

// nativeUpdateText localises the OS notifications sent while updating. They
// are raised from Go, outside the frontend's i18n store.
func nativeUpdateText(key, fallback string) string {
	exeDir, err := ResolveExeDir()
	if err != nil {
		return fallback
	}
	language := "en-US"
	if s, err := loadSettings(exeDir); err == nil && strings.TrimSpace(s.Language) != "" {
		language = s.Language
	}
	if v := i18n.T(exeDir, language, key, nil); v != "" && v != key {
		return v
	}
	return fallback
}

func runLaunchUpdateCheck() {
	defer crashlog.Capture()
	time.Sleep(updatecheck.LaunchCheckDelay)

	exeDir, err := ResolveExeDir()
	if err != nil {
		return
	}
	s, err := loadSettings(exeDir)
	if err != nil || s.OfflineMode {
		return
	}

	currentVersion := appVersionFromBuildConfig()
	apiResultCh := make(chan updatecheck.LaunchAPICheckResult, 1)
	go func() {
		apiResultCh <- updatecheck.FetchLaunchAPICheck(context.Background(), currentVersion)
	}()

	go runLaunchPlatformsJSONCheck(exeDir)

	wailsCtx, wailsCancel := context.WithTimeout(context.Background(), wailsUpdateCheckTimeout)
	defer wailsCancel()
	if _, ok := tryWailsUpdateCheck(wailsCtx); ok {
		return
	}

	updatecheck.HandleLaunchAPICheckResult(<-apiResultCh, exeDir, currentVersion, emitAppUpdateAvailable, emitUpdateCheckFailed)
}

func (*PlatformService) CheckForUpdatesAndInstall() error {
	defer crashlog.Capture()
	app := application.Get()
	if app == nil {
		return errors.New("application is not available")
	}
	exeDir, err := ResolveExeDir()
	if err != nil {
		return err
	}
	s, err := loadSettings(exeDir)
	if err != nil {
		return err
	}
	if s.OfflineMode {
		return nil
	}
	return app.Updater.CheckAndInstall(app.Context())
}

// EnableAutoRestartAfterUpdate wires Wails updater behaviour after Init:
// auto-restart when staged, retry after check-stage errors, and silent periodic
// polling (Wails CheckInterval opens the update window on every failure).
func EnableAutoRestartAfterUpdate(app *application.App) {
	if app == nil {
		return
	}
	// Bound to the download, not to EventUpdateAvailable: a check that only
	// finds a release does not download anything, and announcing one there
	// tells users an update is being applied behind their back.
	app.Event.On(updater.EventDownloadStarted, func(*application.CustomEvent) {
		NotifyNative("tcno-update-downloading", "TcNo Account Switcher", nativeUpdateText("Notify_UpdateDownloading", "Downloading the update."), map[string]interface{}{
			"type": "update-downloading",
		})
	})
	app.Event.On(updater.EventUpdateReady, func(e *application.CustomEvent) {
		NotifyNative("tcno-update-ready", "TcNo Account Switcher", nativeUpdateText("Notify_UpdateReady", "Update installed. Restarting TcNo Account Switcher."), map[string]interface{}{
			"type": "update-ready",
		})
		rel, _ := e.Data.(*updater.Release)
		go func() {
			defer crashlog.Capture()
			// Before Restart: the helper it spawns replaces this executable.
			armUpdateRollback(app, rel)
			if err := app.Updater.Restart(app.Context()); err != nil {
				app.Logger.Error("update: Restart", "error", err)
			}
		}()
	})
	app.Event.On(updater.EventError, func(e *application.CustomEvent) {
		if info, ok := e.Data.(updater.ErrorInfo); ok {
			updaterLastErrorStage.Store(info.Stage)
		}
	})
	// Wails' built-in "Try Again" emits user:install, which only re-downloads.
	// After a check failure there is no pending release, so retry the full flow.
	app.Event.On(updater.EventUserInstall, func(*application.CustomEvent) {
		if app.Updater.State() != updater.StateError {
			return
		}
		stage, _ := updaterLastErrorStage.Load().(updater.Stage)
		if stage != updater.StageCheck {
			return
		}
		go func() {
			defer crashlog.Capture()
			if err := app.Updater.CheckAndInstall(app.Context()); err != nil {
				app.Logger.Warn("update: retry check", "error", err)
			}
		}()
	})
	go runPeriodicSilentUpdateCheck(app.Context(), app)
}

func runPeriodicSilentUpdateCheck(ctx context.Context, app *application.App) {
	defer crashlog.Capture()
	ticker := time.NewTicker(periodicUpdateCheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSilentUpdateCheck(ctx, app)
		}
	}
}

func runSilentUpdateCheck(ctx context.Context, app *application.App) {
	if app == nil || appclient.IsOfflineMode() {
		return
	}
	exeDir, err := ResolveExeDir()
	if err != nil {
		return
	}
	s, err := loadSettings(exeDir)
	if err != nil || s.OfflineMode {
		return
	}

	const maxAttempts = 3
	retryDelays := []time.Duration{0, 30 * time.Second, 2 * time.Minute}
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if !sleepUntilUpdateRetry(ctx, retryDelays[attempt]) {
				return
			}
		}
		checkCtx, cancel := context.WithTimeout(ctx, wailsUpdateCheckTimeout)
		rel, err := app.Updater.Check(checkCtx)
		cancel()
		if err == nil {
			if rel != nil {
				// Notify only. CheckAndInstall would open the updater window
				// and start downloading straight away — Wails has no "ask
				// first" mode, so consent comes from the in-app update bar,
				// whose Update Now button calls CheckForUpdatesAndInstall.
				emitAppUpdateAvailable(wailsReleaseMessage(rel))
			}
			return
		}
		lastErr = err
		if !isTransientNetworkError(err) {
			break
		}
		if attempt+1 < maxAttempts {
			app.Logger.Debug("update: periodic check retry", "attempt", attempt+1, "error", err)
		}
	}
	if lastErr != nil && !errors.Is(lastErr, updater.ErrNotConfigured) {
		app.Logger.Warn("update: periodic check failed", "error", lastErr)
	}
}

func sleepUntilUpdateRetry(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporary failure in name resolution")
}

func wailsReleaseMessage(rel *updater.Release) string {
	if rel == nil {
		return ""
	}
	if msg := strings.TrimSpace(rel.Notes); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(rel.Name); msg != "" {
		return msg
	}
	if v := strings.TrimSpace(rel.Version); v != "" {
		return "v" + strings.TrimPrefix(v, "v")
	}
	return ""
}

// tryWailsUpdateCheck uses the signed GitHub provider configured in gui.go.
// Returns ("available"|"up-to-date", true) on success, ("", false) when the
// updater is unavailable or the check failed (caller should fall back to API).
func tryWailsUpdateCheck(ctx context.Context) (string, bool) {
	app := application.Get()
	if app == nil {
		return "", false
	}
	if appclient.IsOfflineMode() {
		return "", false
	}

	rel, err := app.Updater.Check(ctx)
	if err != nil {
		if !errors.Is(err, updater.ErrNotConfigured) {
			app.Logger.Warn("update: wails check failed, falling back to API", "error", err)
		}
		return "", false
	}
	if rel == nil {
		return "up-to-date", true
	}
	emitAppUpdateAvailable(wailsReleaseMessage(rel))
	return "available", true
}

func runAPIUpdateCheck(ctx context.Context) string {
	return updatecheck.RunManualCheck(ctx, appVersionFromBuildConfig(), emitAppUpdateAvailable)
}

// CheckForUpdatesManually checks for updates when triggered from the UI.
// Uses the Wails signed GitHub updater first; falls back to the tcno.co API.
// Returns "available", "up-to-date", "offline", or "failed".
func (*PlatformService) CheckForUpdatesManually() string {
	exeDir, err := ResolveExeDir()
	if err != nil {
		return "failed"
	}
	s, err := loadSettings(exeDir)
	if err != nil {
		return "failed"
	}
	if s.OfflineMode {
		return "offline"
	}

	wailsCtx, wailsCancel := context.WithTimeout(context.Background(), wailsUpdateCheckTimeout)
	defer wailsCancel()
	if result, ok := tryWailsUpdateCheck(wailsCtx); ok {
		return result
	}

	apiCtx, apiCancel := context.WithTimeout(context.Background(), apiUpdateCheckTimeout)
	defer apiCancel()
	return runAPIUpdateCheck(apiCtx)
}
