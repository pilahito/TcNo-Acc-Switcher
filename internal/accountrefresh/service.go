// Package accountrefresh refreshes saved account details on every platform at
// once.
//
// "Updating" an account in this app never means touching the account itself:
// the switcher only swaps login files and registry values, and it stores no
// passwords. What goes stale is what the app shows about each saved account -
// profile images, display names, Steam ban/VAC state and per-game stats. Each
// platform page can already refresh that for its own accounts (F5, or the
// "Refresh accounts" command). This service runs that same refresh across
// every platform the user has on the home screen, in one action.
//
// The work itself is the existing per-platform refresh paths, which queue
// background downloads with their own bounded concurrency and cooldowns, so a
// batch refresh costs the same as visiting each platform page and pressing F5.
package accountrefresh

import (
	"errors"
	"log/slog"
	"strings"
	"sync"

	"TcNo-Acc-Switcher/internal/appclient"
	"TcNo-Acc-Switcher/internal/basic"
	"TcNo-Acc-Switcher/internal/crashlog"
	"TcNo-Acc-Switcher/internal/platform"
	"TcNo-Acc-Switcher/internal/security"
	"TcNo-Acc-Switcher/internal/steam"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ProgressEvent is emitted once per platform while a batch refresh runs, so the
// UI can show "n of m" instead of a single toast that says nothing until the
// end.
const ProgressEvent = "accounts-refresh-all-progress"

// Progress is the payload of ProgressEvent.
type Progress struct {
	Platform string `json:"platform"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Error    string `json:"error,omitempty"`
}

// PlatformFailure names one platform whose refresh could not be queued.
type PlatformFailure struct {
	Platform string `json:"platform"`
	Error    string `json:"error"`
}

// Summary is what RefreshAllAccounts returns to the UI.
type Summary struct {
	// Refreshed lists the platforms whose refresh was queued.
	Refreshed []string `json:"refreshed"`
	// Skipped lists visible platforms with no saved accounts.
	Skipped []string `json:"skipped"`
	// Failed lists platforms whose refresh could not be started.
	Failed []PlatformFailure `json:"failed"`
	// Offline is true when offline mode stopped the batch before it began:
	// every source this refresh reads from is on the network.
	Offline bool `json:"offline"`
	// Busy is true when another batch refresh was still being queued, so this
	// request did nothing.
	Busy bool `json:"busy"`
	// AccountCount is the number of saved accounts across Refreshed.
	AccountCount int `json:"accountCount"`
}

// deps is everything the batch touches, so tests can drive it without Wails,
// the disk or the network.
type deps struct {
	startup          func() (platform.PlatformStartup, error)
	requireUnlocked  func() error
	offline          func() bool
	refreshSteam     func() error
	refreshBasic     func(platformKey string) error
	refreshGameStats func(platformKey string)
	emit             func(Progress)
}

// AccountRefreshService is the Wails service behind "Refresh all accounts".
type AccountRefreshService struct {
	running sync.Mutex
	d       deps
}

// NewService wires the batch refresh to the app's existing services.
func NewService(platformSvc *platform.PlatformService, basicSvc *basic.BasicService, steamSvc *steam.SteamService) *AccountRefreshService {
	return &AccountRefreshService{d: deps{
		startup:         platformSvc.GetStartup,
		requireUnlocked: security.RequireUnlocked,
		offline:         appclient.IsOfflineMode,
		// The same two calls the Steam page makes on F5: the public community
		// data (avatars, names, VAC/ban state), then the authenticated Steam
		// Guard sweep (CS2 rank, cooldown, Prime, owned games). The second is a
		// no-op when Steam Guard is not in use.
		refreshSteam: func() error {
			if err := steamSvc.RefreshAllSteamImages(); err != nil {
				return err
			}
			return steamSvc.RefreshSteamGuardData()
		},
		refreshBasic: basicSvc.RefreshAllBasicProfileImages,
		// The package-level force refresh rather than BasicService.RefreshAllGameStats:
		// the latter also marks the platform as the one the process monitor
		// watches, and a batch over every platform must not steal that from the
		// page the user is actually on.
		refreshGameStats: basic.ForceGameStatsRefresh,
		emit: func(p Progress) {
			if app := application.Get(); app != nil {
				app.Event.Emit(ProgressEvent, p)
			}
		},
	}}
}

// RefreshAllAccounts refreshes profile images, names and game stats for the
// saved accounts on every platform shown on the home screen. Hidden platforms
// and platforms without saved accounts are left alone.
//
// It returns once every platform's refresh has been queued; the downloads then
// finish in the background and each platform page updates as they land.
func (s *AccountRefreshService) RefreshAllAccounts() (Summary, error) {
	defer crashlog.Capture()
	if !s.running.TryLock() {
		return Summary{Refreshed: []string{}, Skipped: []string{}, Failed: []PlatformFailure{}, Busy: true}, nil
	}
	defer s.running.Unlock()
	return s.run()
}

func (s *AccountRefreshService) run() (Summary, error) {
	sum := Summary{Refreshed: []string{}, Skipped: []string{}, Failed: []PlatformFailure{}}
	if err := s.d.requireUnlocked(); err != nil {
		return sum, err
	}
	if s.d.offline != nil && s.d.offline() {
		sum.Offline = true
		return sum, nil
	}
	st, err := s.d.startup()
	if err != nil {
		return sum, err
	}

	targets, skipped := pickPlatforms(st)
	sum.Skipped = skipped
	for i, name := range targets {
		perr := s.refreshOne(name)
		p := Progress{Platform: name, Done: i + 1, Total: len(targets)}
		if perr != nil {
			p.Error = perr.Error()
			sum.Failed = append(sum.Failed, PlatformFailure{Platform: name, Error: perr.Error()})
			slog.Default().Warn("refresh all accounts: platform failed", "platform", name, "error", perr)
		} else {
			sum.Refreshed = append(sum.Refreshed, name)
			sum.AccountCount += st.PlatformAccountCounts[name]
		}
		if s.d.emit != nil {
			s.d.emit(p)
		}
	}
	return sum, nil
}

// refreshOne runs one platform's refresh. A panic in one platform's code must
// not stop the rest of the batch, so it is turned into that platform's error.
func (s *AccountRefreshService) refreshOne(name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("unexpected error while refreshing")
			slog.Default().Error("refresh all accounts: panic", "platform", name, "panic", r)
		}
	}()
	if strings.EqualFold(name, steam.PlatformKey) {
		if err := s.d.refreshSteam(); err != nil {
			return err
		}
	} else if err := s.d.refreshBasic(name); err != nil {
		return err
	}
	if s.d.refreshGameStats != nil {
		s.d.refreshGameStats(name)
	}
	return nil
}

// pickPlatforms returns, in home-screen order, the visible platforms that have
// at least one saved account, plus the visible ones that were skipped for
// having none.
func pickPlatforms(st platform.PlatformStartup) (targets, skipped []string) {
	disabled := make(map[string]struct{}, len(st.DisabledPlatformNames))
	for _, n := range st.DisabledPlatformNames {
		disabled[strings.ToLower(strings.TrimSpace(n))] = struct{}{}
	}
	seen := map[string]struct{}{}
	targets = []string{}
	skipped = []string{}
	for _, raw := range st.HomePlatformOrder {
		name := strings.TrimSpace(raw)
		key := strings.ToLower(name)
		if name == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, off := disabled[key]; off {
			continue
		}
		if st.PlatformAccountCounts[name] <= 0 {
			skipped = append(skipped, name)
			continue
		}
		targets = append(targets, name)
	}
	return targets, skipped
}
