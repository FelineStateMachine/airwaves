// Package sd keeps a ZIP code's over-the-air listings from Schedules Direct
// (schedulesdirect.org), the licensed source of Gracenote's TV data for
// personal use: two weeks of schedules with descriptions, cast, original
// air dates, ratings, movie runtimes and star ratings, and sports teams.
//
// It is a careful client. It logs in only when its token runs out, asks for
// what changed since its last update and nothing else, updates once a day,
// and stops trying altogether when the account is refused, until the
// account is changed: Schedules Direct blocks clients that keep failing.
package sd

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"airwaves/internal/guide"
)

// Days is how many days of listings are kept.
const Days = 14

// Status is how the listings stand, for the admin page.
type Status struct {
	// User is the account's user name; "" with no account set.
	User string `json:"user,omitempty"`
	// OK is set when the last update worked.
	OK bool `json:"ok"`
	// Stopped says why updating stopped until the account is changed:
	// a refused login, an expired or locked account.
	Stopped string `json:"stopped,omitempty"`
	// Error is the last update's failure, when it failed.
	Error string `json:"error,omitempty"`
	// Expires is when the Schedules Direct account runs out.
	Expires time.Time `json:"expires,omitzero"`
	// Lineup is the account's antenna lineup, "USA-OTA-80302".
	Lineup   string `json:"lineup,omitempty"`
	Stations int    `json:"stations,omitempty"`
	Programs int    `json:"programs,omitempty"`
	// Updated is when the listings were last brought up to date, Through
	// how far ahead they reach, and Next when they're next updated.
	Updated time.Time `json:"updated,omitzero"`
	Through time.Time `json:"through,omitzero"`
	Next    time.Time `json:"next,omitzero"`
	// Updating is set while an update runs.
	Updating bool `json:"updating,omitempty"`
}

// Source keeps the listings of ZIP's antenna lineup up to date.
type Source struct {
	// Dir keeps the account and what was fetched.
	Dir string
	// ZIP is the antenna's.
	ZIP string
	// HTTP makes the requests; UserAgent names this program and its
	// version, as Schedules Direct requires.
	HTTP      *http.Client
	UserAgent string
	// BaseURL is the API's; API by default.
	BaseURL string
	// OnUpdate, when set, is told of new listings.
	OnUpdate func()

	once    sync.Once
	updates chan struct{}

	mu     sync.Mutex
	status Status
	st     *state
	g      *guide.Guide
}

func (s *Source) accountPath() string { return filepath.Join(s.Dir, "account.json") }
func (s *Source) statePath() string   { return filepath.Join(s.Dir, "listings.json") }

func (s *Source) init() {
	s.once.Do(func() {
		s.updates = make(chan struct{}, 1)
		a, err := LoadAccount(s.accountPath())
		if err != nil {
			log.Printf("listings: %v", err)
		}
		st, err := loadState(s.statePath())
		if err != nil {
			log.Printf("listings: %v; starting afresh", err)
		}
		if st.oldLogos() {
			st.LineupModified = "" // read the lineup again, for its logos
			st.Updated = time.Time{}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.st = st
		s.status.User = a.User
		s.status.Stopped = st.Stopped
		if st.Updated.After(time.Time{}) {
			s.status.OK = st.Stopped == ""
			s.status.Updated, s.status.Lineup = st.Updated, st.Lineup
			s.status.Expires = st.Expires
		}
		s.g = st.guide(s.ZIP, time.Now())
		s.count()
	})
}

// count notes the listings' size in the status. Called with s.mu held.
func (s *Source) count() {
	s.status.Stations, s.status.Programs, s.status.Through = 0, 0, time.Time{}
	if s.g == nil {
		return
	}
	s.status.Stations = len(s.g.Channels)
	for _, ps := range s.g.Programs {
		s.status.Programs += len(ps)
		if n := len(ps); n > 0 && ps[n-1].End.After(s.status.Through) {
			s.status.Through = ps[n-1].End
		}
	}
}

// Status reports how the listings stand.
func (s *Source) Status() Status {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Guide is the listings, nil while there are none: no account, or none
// fetched yet.
func (s *Source) Guide() *guide.Guide {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.User == "" {
		return nil
	}
	return s.g
}

// SetAccount keeps a new login and updates with it at once; an empty user
// removes the account and its listings. An empty password keeps the saved
// one for the same user.
func (s *Source) SetAccount(user, password string) error {
	s.init()
	old, _ := LoadAccount(s.accountPath())
	a := Account{User: user, PasswordSHA1: HashPassword(password)}
	switch {
	case user == "":
		a = Account{}
	case password == "" && user == old.User:
		a.PasswordSHA1 = old.PasswordSHA1
	case password == "":
		return errors.New("enter the Schedules Direct password")
	}
	if err := SaveAccount(s.accountPath(), a); err != nil {
		return err
	}
	s.mu.Lock()
	// A new login: try again, with a token of its own.
	s.st.Stopped, s.st.Token, s.st.TokenExpires = "", "", time.Time{}
	s.status = Status{User: a.User}
	if a.User == "" {
		s.st = &state{}
		s.g = nil
	}
	err := s.st.save(s.statePath())
	s.mu.Unlock()
	if a.Set() {
		s.Kick()
	} else if s.OnUpdate != nil {
		s.OnUpdate() // back to the public grid
	}
	return err
}

// Kick asks for an update now.
func (s *Source) Kick() {
	s.init()
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

// Run keeps the listings up to date until ctx ends: about once a day, at
// a minute of its own (Schedules Direct asks clients not to all come at
// once), and on Kick. After a failure it tries again in an hour; a refused account
// stops it until the account changes.
func (s *Source) Run(ctx context.Context) {
	s.init()
	// The same minute of the day every day, picked once per server.
	offset := time.Duration(rand.IntN(24*60)) * time.Minute
	for {
		a, _ := LoadAccount(s.accountPath())
		next := time.Time{}
		if a.Set() {
			s.mu.Lock()
			stopped, updated := s.st.Stopped, s.st.Updated
			s.mu.Unlock()
			if stopped == "" {
				// The server's own minute of the day, at least half a day
				// after the last update: a day apart, give or take.
				soonest := updated.Add(12 * time.Hour)
				if next = soonest.Truncate(24 * time.Hour).Add(offset); next.Before(soonest) {
					next = next.Add(24 * time.Hour)
				}
				if updated.IsZero() || !time.Now().Before(next) {
					s.update(ctx, a)
					continue
				}
			}
		}
		s.mu.Lock()
		s.status.Next = next
		s.mu.Unlock()
		var wake <-chan time.Time
		if !next.IsZero() {
			wake = time.After(time.Until(next))
		}
		select {
		case <-ctx.Done():
			return
		case <-s.updates:
			s.mu.Lock()
			stopped := s.st.Stopped
			s.mu.Unlock()
			// A stopped account waits for SetAccount, which clears it.
			if a, _ := LoadAccount(s.accountPath()); a.Set() && stopped == "" {
				s.update(ctx, a)
			}
		case <-wake:
		}
	}
}

// update brings the listings up to date with a, and notes how it went.
// After a failure that isn't the account's, it waits an hour.
func (s *Source) update(ctx context.Context, a Account) {
	// This update answers any Kick waiting.
	select {
	case <-s.updates:
	default:
	}
	s.mu.Lock()
	s.status.Updating = true
	st := s.st.clone()
	s.mu.Unlock()

	c := &client{base: s.BaseURL, http: s.HTTP, ua: s.UserAgent, account: a, st: st}
	err := c.update(ctx, s.ZIP, time.Now())
	var stop *StopError
	if errors.As(err, &stop) {
		st.Stopped = stop.Error()
	}
	if err == nil {
		st.Updated = time.Now()
	}
	if serr := st.save(s.statePath()); serr != nil {
		log.Printf("listings: %v", serr)
	}

	s.mu.Lock()
	s.st = st
	s.status.Updating = false
	s.status.Stopped, s.status.Lineup, s.status.Expires = st.Stopped, st.Lineup, st.Expires
	if err == nil {
		s.status.OK, s.status.Error, s.status.Updated = true, "", st.Updated
		s.g = st.guide(s.ZIP, time.Now())
		s.count()
	} else {
		s.status.OK, s.status.Error = false, err.Error()
	}
	s.mu.Unlock()

	if err == nil && s.OnUpdate != nil {
		s.OnUpdate()
	}
	switch {
	case err == nil:
		log.Printf("listings: up to date from Schedules Direct, %d programs on %d stations", s.status.Programs, s.status.Stations)
	case stop != nil:
		log.Printf("listings: stopped until the account is changed: %v", err)
	default:
		log.Printf("listings: %v; trying again in an hour", err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Hour):
		case <-s.updates:
		}
	}
}
