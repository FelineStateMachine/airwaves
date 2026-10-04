package vchan

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Schedule is when a sequential channel airs, from its channel.json: its
// items in order from Start, beginning with First and looping at the
// end. With Blocks it airs only in them, standing by between; without,
// around the clock.
type Schedule struct {
	// Start is when the channel begins, in local time: "2026-10-04" (the
	// first block that day) or "2026-10-04T20:00".
	Start string `json:"start"`
	// First is the item it begins with, counting from 1; 0 is the first.
	First  int     `json:"first,omitempty"`
	Blocks []Block `json:"blocks,omitempty"`
}

// Block is a daily run of a scheduled channel: from At, Count items, or
// until Until. One of the two ends it.
type Block struct {
	// Days are the days it airs, "mon" to "sun"; none for every day.
	Days []string `json:"days,omitempty"`
	At   string   `json:"at"` // "20:00", local time
	// Count is how many items it airs, back to back.
	Count int `json:"count,omitempty"`
	// Until is when it stops: nothing starts from then, and the item on
	// finishes. One earlier than At is the next day's.
	Until string `json:"until,omitempty"`
}

// Sequenced is a channel whose items can air in order on a schedule:
// folder and Jellyfin channels, and YouTube channels playing a playlist.
type Sequenced interface {
	Channel
	// Sequence lists the items in the order they air; ok is false for a
	// channel that doesn't play in order (YouTube uploads).
	Sequence() (items []SequenceItem, ok bool)
}

// SequenceItem is an item of a Sequenced channel, for choosing where its
// schedule begins.
type SequenceItem struct {
	Title    string
	Subtitle string // an episode's name, or a video's channel
	Season   string
	Episode  string
	Length   time.Duration
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// sched is a Schedule read.
type sched struct {
	start  time.Time
	first  int // from 0
	blocks []block
}

type block struct {
	days  [7]bool       // by time.Weekday
	at    time.Duration // after midnight
	count int
	until time.Duration // after midnight; may be more than a day; 0 with a count
}

// Check reports what's wrong with the schedule, in words for the admin
// page.
func (s Schedule) Check() error {
	_, err := s.read(time.Local)
	return err
}

func (s Schedule) read(loc *time.Location) (*sched, error) {
	out := &sched{first: max(s.First-1, 0)}
	start := strings.TrimSpace(s.Start)
	var err error
	if out.start, err = time.ParseInLocation("2006-01-02T15:04", start, loc); err != nil {
		if out.start, err = time.ParseInLocation("2006-01-02", start, loc); err != nil {
			return nil, fmt.Errorf("the start %q isn't a date like 2026-10-04 or a time like 2026-10-04T20:00", s.Start)
		}
	}
	if s.First < 0 {
		return nil, errors.New("the first item counts from 1")
	}
	if len(s.Blocks) > 12 {
		return nil, errors.New("a channel airs in up to 12 blocks")
	}
	for i, b := range s.Blocks {
		var r block
		name := fmt.Sprintf("block %d", i+1)
		if r.at, err = clockTime(b.At); err != nil {
			return nil, fmt.Errorf("%s starts at %q: %w", name, b.At, err)
		}
		switch {
		case b.Count < 0 || b.Count > 100:
			return nil, fmt.Errorf("%s airs 1 to 100 items", name)
		case b.Count > 0 && b.Until != "":
			return nil, fmt.Errorf("%s has both a count and a time to stop; give one", name)
		case b.Count > 0:
			r.count = b.Count
		case b.Until == "":
			return nil, fmt.Errorf("%s needs a count or a time to stop", name)
		default:
			if r.until, err = clockTime(b.Until); err != nil {
				return nil, fmt.Errorf("%s stops at %q: %w", name, b.Until, err)
			}
			if r.until <= r.at {
				r.until += 24 * time.Hour
			}
		}
		if len(b.Days) == 0 {
			r.days = [7]bool{true, true, true, true, true, true, true}
		}
		for _, d := range b.Days {
			k := slices.Index(weekdays, strings.ToLower(strings.TrimSpace(d)))
			if k < 0 {
				return nil, fmt.Errorf("%s: %q isn't a day (mon to sun)", name, d)
			}
			r.days[k] = true
		}
		out.blocks = append(out.blocks, r)
	}
	slices.SortStableFunc(out.blocks, func(a, b block) int { return cmp.Compare(a.at, b.at) })
	return out, nil
}

// clockTime reads "20:00" as the time after midnight.
func clockTime(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, errors.New("give a time like 20:00")
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// slot is one airing on a scheduled channel: item I of the channel's,
// from Start to End. A gap, off the air, has I -1.
type slot struct {
	I          int
	Start, End time.Time
}

// scheduleReach is how far past its window a search for the next airing
// looks, as for a channel starting next month.
const scheduleReach = 400 * 24 * time.Hour

// slots returns the airings that end after from and start before to, in
// order, at most limit of them. lengths are the items' lengths in channel
// order; items without one are skipped.
func (s *sched) slots(lengths []time.Duration, from, to time.Time, limit int) []slot {
	var total time.Duration
	for _, l := range lengths {
		total += max(l, 0)
	}
	if total <= 0 || limit <= 0 {
		return nil
	}
	var out []slot
	k := s.first % len(lengths) // the next item
	emit := func(at time.Time) time.Time {
		for lengths[k] <= 0 {
			k = (k + 1) % len(lengths)
		}
		end := at.Add(lengths[k])
		if end.After(from) && at.Before(to) {
			out = append(out, slot{k, at, end})
		}
		k = (k + 1) % len(lengths)
		return end
	}

	if len(s.blocks) == 0 {
		// Around the clock: whole loops are skipped at once.
		at := s.start
		if from.After(at) {
			loops := from.Sub(at) / total
			at = at.Add(loops * total)
		}
		for at.Before(to) && len(out) < limit {
			at = emit(at)
		}
		return out
	}

	end := s.start // when the last airing ends
	loc := s.start.Location()
	y, m, d := s.start.Date()
	for day := time.Date(y, m, d, 0, 0, 0, 0, loc); day.Before(to) && len(out) < limit; day = day.AddDate(0, 0, 1) {
		for _, b := range s.blocks {
			if !b.days[day.Weekday()] {
				continue
			}
			at := dayAt(day, b.at)
			if at.Before(s.start) {
				continue
			}
			// A block that the one before ran into starts when it ends.
			at = maxTime(at, end)
			if b.count > 0 {
				for range b.count {
					at = emit(at)
				}
			} else {
				until := dayAt(day, b.until)
				for at.Before(until) {
					at = emit(at)
				}
			}
			end = at
			if len(out) >= limit {
				break
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// dayAt is the time d after midnight on day by the clock, so 20:00 is
// 20:00 on days that change for daylight saving too.
func dayAt(day time.Time, d time.Duration) time.Time {
	y, m, dd := day.Date()
	return time.Date(y, m, dd, int(d/time.Hour), int(d%time.Hour/time.Minute), 0, 0, day.Location())
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// at returns the airing on at t, or when the channel is off the air, the
// next one; ok is false when nothing will air.
func (s *sched) at(lengths []time.Duration, t time.Time) (on slot, onAir, ok bool) {
	next := s.slots(lengths, t, t.Add(scheduleReach), 1)
	if len(next) == 0 {
		next = s.slots(lengths, t, s.start.Add(scheduleReach), 1)
	}
	if len(next) == 0 {
		return slot{}, false, false
	}
	return next[0], !next[0].Start.After(t), true
}

// premiere returns the schedule's very first airing, which an off-air
// note says it "starts" with rather than is "back" with; ok is false when
// nothing will air.
func (s *sched) premiere(lengths []time.Duration) (first slot, ok bool) {
	if a := s.slots(lengths, s.start, s.start.Add(scheduleReach), 1); len(a) > 0 {
		return a[0], true
	}
	return slot{}, false
}

// guide returns what airs from from to to, with the gaps between: slots
// with I -1, the one before an airing ending when it starts, past to if
// need be. At most limit entries.
func (s *sched) guide(lengths []time.Duration, from, to time.Time, limit int) []slot {
	airs := s.slots(lengths, from, to, limit)
	// The airing after the window ends its last gap.
	if len(airs) < limit {
		after := s.slots(lengths, maxTime(to, from), to.Add(scheduleReach), 1)
		if len(after) == 0 {
			after = s.slots(lengths, maxTime(to, from), s.start.Add(scheduleReach), 1)
		}
		if len(after) > 0 && (len(airs) == 0 || after[0].Start.After(airs[len(airs)-1].Start)) {
			airs = append(airs, after[0])
		}
	}
	var out []slot
	at := from
	for _, a := range airs {
		if a.Start.After(at) {
			if len(out) == limit {
				break
			}
			out = append(out, slot{-1, at, a.Start})
		}
		if !a.Start.Before(to) {
			break
		}
		if len(out) == limit {
			break
		}
		out = append(out, a)
		at = a.End
	}
	if len(airs) == 0 && len(out) < limit {
		out = append(out, slot{-1, from, to})
	}
	return out
}

// offAirProgram is a guide entry for a gap: what's next and when, as a
// viewer planning to watch wants it.
func offAirProgram(gap slot, nextTitle string, next time.Time, premiere bool) Program {
	p := Program{Start: gap.Start, End: gap.End, Title: "Off air", OffAir: true}
	if nextTitle == "" {
		return p
	}
	when := next.Format("at 3:04 PM")
	if next.Format(time.DateOnly) != gap.Start.Format(time.DateOnly) {
		when = next.Format("Mon, Jan 2 at 3:04 PM")
	}
	if premiere {
		p.Description = fmt.Sprintf("Starts %s with %s.", when, nextTitle)
	} else {
		p.Description = fmt.Sprintf("Back %s with %s.", when, nextTitle)
	}
	return p
}
