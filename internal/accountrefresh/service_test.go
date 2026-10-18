package accountrefresh

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"TcNo-Acc-Switcher/internal/platform"
)

type recorder struct {
	mu        sync.Mutex
	steam     int
	basic     []string
	gameStats []string
	progress  []Progress
}

func newTestService(st platform.PlatformStartup, r *recorder, basicErr map[string]error) *AccountRefreshService {
	return &AccountRefreshService{d: deps{
		startup:         func() (platform.PlatformStartup, error) { return st, nil },
		requireUnlocked: func() error { return nil },
		offline:         func() bool { return false },
		refreshSteam: func() error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.steam++
			return nil
		},
		refreshBasic: func(k string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.basic = append(r.basic, k)
			if basicErr != nil {
				return basicErr[k]
			}
			return nil
		},
		refreshGameStats: func(k string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.gameStats = append(r.gameStats, k)
		},
		emit: func(p Progress) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.progress = append(r.progress, p)
		},
	}}
}

func TestRefreshAllAccountsVisitsVisiblePlatformsWithAccounts(t *testing.T) {
	st := platform.PlatformStartup{
		HomePlatformOrder:     []string{"Steam", "Epic Games", "Discord", "Ubisoft", "Battle.net", "Steam"},
		DisabledPlatformNames: []string{"ubisoft"},
		PlatformAccountCounts: map[string]int{"Steam": 3, "Epic Games": 2, "Discord": 0, "Ubisoft": 4, "Battle.net": 1},
	}
	r := &recorder{}
	sum, err := newTestService(st, r, nil).RefreshAllAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Steam", "Epic Games", "Battle.net"}; !reflect.DeepEqual(sum.Refreshed, want) {
		t.Fatalf("Refreshed = %v, want %v", sum.Refreshed, want)
	}
	if want := []string{"Discord"}; !reflect.DeepEqual(sum.Skipped, want) {
		t.Fatalf("Skipped = %v, want %v", sum.Skipped, want)
	}
	if sum.AccountCount != 6 {
		t.Fatalf("AccountCount = %d, want 6", sum.AccountCount)
	}
	if r.steam != 1 {
		t.Fatalf("steam refreshed %d times, want 1", r.steam)
	}
	if want := []string{"Epic Games", "Battle.net"}; !reflect.DeepEqual(r.basic, want) {
		t.Fatalf("basic refreshed %v, want %v", r.basic, want)
	}
	if want := []string{"Steam", "Epic Games", "Battle.net"}; !reflect.DeepEqual(r.gameStats, want) {
		t.Fatalf("game stats refreshed %v, want %v", r.gameStats, want)
	}
	if len(r.progress) != 3 || r.progress[2].Done != 3 || r.progress[2].Total != 3 {
		t.Fatalf("progress = %+v", r.progress)
	}
}

func TestRefreshAllAccountsKeepsGoingAfterAFailure(t *testing.T) {
	st := platform.PlatformStartup{
		HomePlatformOrder:     []string{"Epic Games", "Riot Games", "Battle.net"},
		PlatformAccountCounts: map[string]int{"Epic Games": 1, "Riot Games": 1, "Battle.net": 1},
	}
	r := &recorder{}
	sum, err := newTestService(st, r, map[string]error{"Riot Games": errors.New("boom")}).RefreshAllAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Epic Games", "Battle.net"}; !reflect.DeepEqual(sum.Refreshed, want) {
		t.Fatalf("Refreshed = %v, want %v", sum.Refreshed, want)
	}
	if len(sum.Failed) != 1 || sum.Failed[0].Platform != "Riot Games" || sum.Failed[0].Error != "boom" {
		t.Fatalf("Failed = %+v", sum.Failed)
	}
	if r.progress[1].Error != "boom" {
		t.Fatalf("progress[1] = %+v, want the error", r.progress[1])
	}
}

func TestRefreshAllAccountsRecoversFromPanic(t *testing.T) {
	st := platform.PlatformStartup{
		HomePlatformOrder:     []string{"Epic Games", "Battle.net"},
		PlatformAccountCounts: map[string]int{"Epic Games": 1, "Battle.net": 1},
	}
	r := &recorder{}
	s := newTestService(st, r, nil)
	inner := s.d.refreshBasic
	s.d.refreshBasic = func(k string) error {
		if k == "Epic Games" {
			panic("bad platform data")
		}
		return inner(k)
	}
	sum, err := s.RefreshAllAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Failed) != 1 || len(sum.Refreshed) != 1 || sum.Refreshed[0] != "Battle.net" {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestRefreshAllAccountsOffline(t *testing.T) {
	r := &recorder{}
	s := newTestService(platform.PlatformStartup{HomePlatformOrder: []string{"Steam"}, PlatformAccountCounts: map[string]int{"Steam": 1}}, r, nil)
	s.d.offline = func() bool { return true }
	sum, err := s.RefreshAllAccounts()
	if err != nil || !sum.Offline || r.steam != 0 {
		t.Fatalf("sum=%+v err=%v steam=%d", sum, err, r.steam)
	}
}

func TestRefreshAllAccountsLocked(t *testing.T) {
	r := &recorder{}
	s := newTestService(platform.PlatformStartup{}, r, nil)
	locked := errors.New("locked")
	s.d.requireUnlocked = func() error { return locked }
	if _, err := s.RefreshAllAccounts(); !errors.Is(err, locked) {
		t.Fatalf("err = %v, want locked", err)
	}
}

func TestRefreshAllAccountsRejectsOverlap(t *testing.T) {
	s := newTestService(platform.PlatformStartup{}, &recorder{}, nil)
	s.running.Lock()
	defer s.running.Unlock()
	if sum, err := s.RefreshAllAccounts(); err != nil || !sum.Busy {
		t.Fatalf("sum = %+v, err = %v, want Busy", sum, err)
	}
}
