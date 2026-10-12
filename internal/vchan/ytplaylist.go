package vchan

// YouTube playlists: a YouTube channel whose youtube.json names a playlist
// rather than channels plays the playlist's videos in its order, looping,
// as a folder channel plays its files: around the clock from the epoch,
// or on the schedule in its channel.json (see schedule.go). There's no
// planner, first runs or repeat guard: the airings follow from the order
// and the videos' lengths, so the catalog is all that's kept, a single
// source holding the playlist's videos in order. It's listed again daily,
// and when youtube.json is edited, so videos added to the playlist join.

import (
	"cmp"
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"airwaves/internal/youtube"
)

// parseYtPlaylist reads a playlist's listing, from yt-dlp --flat-playlist
// -J with youtube.PlaylistArgs (see youtube.ParsePlaylist), undated as a
// playlist channel has it.
func parseYtPlaylist(raw []byte) (ytListing, error) {
	l, err := youtube.ParsePlaylist(raw)
	return listingOf(l, false), err
}

// mergePlaylist takes in a listing of the playlist: its videos in its
// order, keeping what's known about each. Videos taken out of it go, and
// one found unplayable stays so.
func (s *ytSource) mergePlaylist(l ytListing, now time.Time) (added int) {
	old := map[string]*ytVideo{}
	for _, v := range s.Videos {
		old[v.ID] = v
	}
	first := s.Listed.IsZero()
	out := make([]*ytVideo, 0, len(l.Videos))
	for _, v := range l.Videos {
		o := old[v.ID]
		if o == nil {
			if !first {
				v.Found = now
				added++
			}
			out = append(out, v)
			continue
		}
		o.Title, o.Channel = v.Title, cmp.Or(v.Channel, o.Channel)
		if v.Seconds > 0 {
			o.Seconds = v.Seconds
		}
		if o.Skip != "gone" {
			o.Skip = v.Skip
		}
		out = append(out, o)
	}
	s.ID, s.Name, s.Owner, s.OwnerID = l.ID, l.Name, l.Owner, l.OwnerID
	s.Listed, s.Dated, s.Videos = now, true, out
	return added
}

// isPlaylist reports whether the channel plays a playlist. y.mu is held.
func (y *YouTube) isPlaylist() bool { return y.err == nil && y.cfg.Playlist != "" }

// sequence is the playlist's videos that can play, in its order, and
// their lengths: what the schedule airs. It's made again, with y.videos,
// when the catalog changes. y.mu is held.
func (y *YouTube) sequence() ([]*ytVideo, []time.Duration) {
	if y.seq == nil {
		y.seq, y.lens, y.videos = []*ytVideo{}, nil, map[string]*ytVideo{}
		for _, s := range y.cat.Sources {
			for _, v := range s.Videos {
				y.videos[v.ID] = v
				if v.Skip == "" && v.Seconds > 0 {
					y.seq = append(y.seq, v)
					y.lens = append(y.lens, time.Duration(v.Seconds)*time.Second)
				}
			}
		}
	}
	return y.seq, y.lens
}

// airTimes is when the playlist airs: on the schedule in channel.json, or
// without one, around the clock from the epoch as folder channels loop.
// y.mu is held.
func (y *YouTube) airTimes() (s *sched, scheduled bool) {
	if s := folderSchedule(&y.record, y.Dir); s != nil {
		return s, true
	}
	return &sched{start: scheduleEpoch}, false
}

// Sequence implements Sequenced: the playlist's videos that can play, in
// its order. Uploads play in no order, so for them ok is false.
func (y *YouTube) Sequence() (items []SequenceItem, ok bool) {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.tend()
	if !y.isPlaylist() {
		return nil, false
	}
	seq, lens := y.sequence()
	items = make([]SequenceItem, len(seq))
	for i, v := range seq {
		items[i] = SequenceItem{Title: v.Title, Subtitle: y.channelOf(v), Length: lens[i]}
	}
	return items, true
}

// channelOf names the channel of a playlist's video: its own, or failing
// that the playlist's. y.mu is held.
func (y *YouTube) channelOf(v *ytVideo) string {
	if v.Channel != "" || len(y.cat.Sources) == 0 {
		return v.Channel
	}
	return y.cat.Sources[0].Owner
}

// playlistPrograms is the playlist's guide: an entry per video, and on a
// schedule, "Off air" between its blocks, saying when it's back and with
// what. y.mu is held.
func (y *YouTube) playlistPrograms(from, to time.Time) []Program {
	seq, lens := y.sequence()
	if len(seq) == 0 {
		return nil
	}
	s, scheduled := y.airTimes()
	var slots []slot
	if scheduled {
		slots = s.guide(lens, from, to, maxPrograms)
	} else {
		slots = s.slots(lens, from, to, maxPrograms)
	}
	now := y.clock()
	out := make([]Program, 0, len(slots))
	for k, sl := range slots {
		if sl.I >= 0 {
			v := seq[sl.I]
			p := Program{Start: sl.Start.Local(), End: sl.End.Local(), Title: v.Title, Image: youtube.Thumbnail(v.ID),
				Description: v.Description, AudioLang: v.AudioLang, Captions: v.Captions}
			p.Subtitle, p.Date = ytDated(y.channelOf(v), v, now)
			out = append(out, p)
			continue
		}
		// A gap ends with the next entry, or past the window, the next airing.
		next, ok := slot{}, false
		if k+1 < len(slots) {
			next, ok = slots[k+1], true
		} else {
			next, _, ok = s.at(lens, sl.End)
		}
		p := offAirProgram(sl, "", time.Time{}, false)
		if ok {
			p = offAirProgram(sl, seq[next.I].Title, next.Start, isPremiere(s, lens, next))
		}
		p.Start, p.End = p.Start.Local(), p.End.Local()
		out = append(out, p)
	}
	return out
}

// playlistAt is what the playlist airs at t: the airing on, and the one
// after it when that follows straight on; or while it's off the air, the
// next airing and what the slate says until then, as the guide does
// ("Back at 8:00 PM with Episode 3"). ok is false when nothing will air.
// y.mu is held.
func (y *YouTube) playlistAt(t time.Time) (on, next ytAiring, wait string, ok bool) {
	seq, lens := y.sequence()
	if len(seq) == 0 {
		return on, next, "", false
	}
	s, _ := y.airTimes()
	sl, onAir, ok := s.at(lens, t)
	if !ok {
		return on, next, "", false
	}
	on = playlistAiring(seq[sl.I], sl)
	if !onAir {
		note := offAirProgram(slot{-1, t, sl.Start}, on.Title, sl.Start.Local(), isPremiere(s, lens, sl))
		return on, next, strings.TrimSuffix(note.Description, "."), true
	}
	if after, _, ok := s.at(lens, sl.End); ok && after.Start.Equal(sl.End) {
		next = playlistAiring(seq[after.I], after)
	}
	return on, next, "", true
}

// playlistAfter is the airing after a on the playlist, after a gap or
// not. y.mu is held.
func (y *YouTube) playlistAfter(a ytAiring) ytAiring {
	seq, lens := y.sequence()
	if len(seq) == 0 {
		return ytAiring{}
	}
	s, _ := y.airTimes()
	if sl, _, ok := s.at(lens, a.End); ok {
		return playlistAiring(seq[sl.I], sl)
	}
	return ytAiring{}
}

func playlistAiring(v *ytVideo, sl slot) ytAiring {
	return ytAiring{ID: v.ID, Title: v.Title, Start: sl.Start, End: sl.End}
}

// listPlaylist lists the playlist at url again. y.mu isn't held.
func (y *YouTube) listPlaylist(ctx context.Context, url string) {
	tool := y.tool()
	tool.Jobs.Lock()
	raw, err := tool.Run(ctx, slices.Concat(youtube.PlaylistArgs, []string{url})...)
	tool.Jobs.Unlock()
	var l ytListing
	if err == nil {
		l, err = parseYtPlaylist(raw)
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if err != nil {
		y.tried[url] = y.clock()
		log.Printf("%s: listing %s: %v", y.label(), url, err)
		return
	}
	if s := y.source(url); s != nil { // unless no longer configured
		added := s.mergePlaylist(l, y.clock())
		y.changed()
		log.Printf("%s: playlist %q has %d videos (%d new)", y.label(), l.Name, len(l.Videos), added)
	}
}

// playlistLookups picks the playlist's videos to look up next: those
// without a length, premieres to check again, then what airs soon
// without a description. y.mu is held.
func (y *YouTube) playlistLookups(now time.Time) []string {
	var ids []string
	add := func(id string) {
		if len(ids) < ytLookups && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, s := range y.cat.Sources {
		for _, v := range s.Videos {
			switch {
			case v.Skip == "" && v.Seconds == 0 && now.Sub(v.Looked) >= 10*time.Minute,
				v.Skip == "upcoming" && now.Sub(v.Looked) >= time.Hour:
				add(v.ID)
			}
		}
	}
	seq, lens := y.sequence()
	if len(seq) == 0 {
		return ids
	}
	s, _ := y.airTimes()
	for _, sl := range s.slots(lens, now, now.Add(ytDescribeAhead), 2*ytLookups) {
		if v := seq[sl.I]; v.Looked.IsZero() {
			add(v.ID)
		}
	}
	return ids
}

// learnVideo takes in a video's looked-up details. A playlist's Shorts
// and recorded streams play like any other video. y.mu is held.
func (y *YouTube) learnVideo(v *ytVideo, i youtube.Info) {
	v.learn(i, y.clock())
	if y.isPlaylist() && (v.Skip == "short" || v.Skip == "live" && i.LiveStatus == "was_live") {
		v.Skip = ""
	}
}
