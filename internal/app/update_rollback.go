package app

import (
	"log"
	"os"
	"os/exec"
	"time"

	buildinfo "TcNo-Acc-Switcher/build"
	"TcNo-Acc-Switcher/internal/crashlog"
	"TcNo-Acc-Switcher/internal/updaterollback"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// updateHealthyAfter is how long a freshly updated build has to stay up before
// it counts as working even if its window never reported in (for example when
// it starts hidden in the tray). A build that crashes on start does not last
// this long; the frontend normally reports in well before it.
const updateHealthyAfter = 60 * time.Second

// checkUpdateRollbackOnBoot runs before any window or single-instance lock
// exists. When the build that was just installed has failed to start several
// times in a row it puts the previous version back, starts it, and exits this
// process. Otherwise it returns an i18n toast to show, or "".
func checkUpdateRollbackOnBoot() string {
	if !updaterollback.Supported() {
		return ""
	}
	version := buildinfo.Version()
	if version == "" {
		return ""
	}
	exe, err := updaterollback.ExecutablePath()
	if err != nil {
		return ""
	}
	d, err := updaterollback.Boot(exe, version)
	if err != nil {
		log.Printf("update rollback: %v", err)
	}
	if !d.Relaunch {
		return d.Toast
	}
	log.Printf("update rollback: version %s did not start correctly; restored the previous version", version)
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		log.Printf("update rollback: relaunch: %v", err)
	}
	os.Exit(0)
	return ""
}

// scheduleUpdateHealthyMark disarms rollback once this build has stayed up for
// updateHealthyAfter. The frontend disarms it sooner, as soon as it has loaded
// (see platform.NotifyLaunchUpdateCheck).
func scheduleUpdateHealthyMark(wailsApp *application.App) {
	if wailsApp == nil || !updaterollback.Supported() {
		return
	}
	wailsApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			defer crashlog.Capture()
			time.Sleep(updateHealthyAfter)
			markUpdateHealthy()
		}()
	})
}

func markUpdateHealthy() {
	exe, err := updaterollback.ExecutablePath()
	if err != nil {
		return
	}
	updaterollback.MarkHealthy(exe, buildinfo.Version())
}
