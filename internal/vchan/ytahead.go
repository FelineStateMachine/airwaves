package vchan

import (
	"context"
	"errors"
	"log"
	"time"

	"airwaves/internal/phase"
	"airwaves/internal/youtube"
)

// Finding where to stream a video from takes yt-dlp seconds, too long to
// wait for when tuning in. So each YouTube channel keeps the addresses of
// the video on now and the next found ahead of time (see warm), and finds
// them again before they expire. That's one yt-dlp run per video as it
// comes up, and one more for a video longer than its addresses last.

// ytResolved is a video's details with its addresses, found ahead.
type ytResolved struct {
	info youtube.Info
	// caps are its English captions, fetched as soon as it's found, so
	// tuning in doesn't wait for them either; nil when it has none.
	caps   *captions
	height int       // the most picture asked for
	until  time.Time // when its addresses may stop working
}

type ytAhead struct {
	started bool          // asking yt-dlp, rather than waiting to
	done    chan struct{} // closed at the end
}

const (
	// ytWarmIdle is how long a channel nobody lists, guides or watches
	// keeps finding its videos ahead.
	ytWarmIdle = 6 * time.Hour
	// ytWarmEvery is how often it looks at what to find.
	ytWarmEvery = 30 * time.Second
	// ytMinLeft is how long addresses must still work to be used, or
	// kept: they're found again sooner.
	ytMinLeft = 20 * time.Minute
	// ytMissedRetry spaces out tries to find a video ahead that failed.
	ytMissedRetry = 10 * time.Minute
)

// ytWarm turns finding videos ahead on; tests turn it off.
var ytWarm = true

// warm keeps the addresses of the videos on now and next found, so
// tuning in never waits for yt-dlp. It runs while the channel is tended
// (listed, guided or watched) and stops ytWarmIdle after it last was, or
// once the channel leaves the library.
func (y *YouTube) warm() {
	for {
		y.mu.Lock()
		if y.closed || time.Since(y.touched) > ytWarmIdle {
			y.warming = false
			y.mu.Unlock()
			return
		}
		ids := y.warmDue(time.Now())
		y.mu.Unlock()
		for _, id := range ids {
			y.lookAhead(id)
		}
		time.Sleep(ytWarmEvery)
	}
}

// warmDue lists the videos on and next whose addresses need finding, and
// forgets addresses that expired. y.mu is held.
func (y *YouTube) warmDue(now time.Time) []string {
	for id, r := range y.resolved {
		if now.After(r.until) {
			delete(y.resolved, id)
		}
	}
	if !y.ready() || y.err != nil {
		return nil
	}
	on, next, _, ok := y.airingAt(y.clock())
	if !ok {
		return nil
	}
	if next.ID == "" {
		next = y.after(on)
	}
	var ids []string
	for _, a := range []ytAiring{on, next} {
		if a.ID == "" || y.ahead[a.ID] != nil || now.Sub(y.missed[a.ID]) < ytMissedRetry {
			continue
		}
		if v := y.videos[a.ID]; v != nil && v.Skip == "gone" {
			continue
		}
		if r, ok := y.resolved[a.ID]; ok && r.height == y.cfg.MaxHeight && r.until.Sub(now) >= ytMinLeft {
			continue
		}
		if len(ids) == 0 || ids[0] != a.ID {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// lookAhead finds a video's addresses now, one video at a time across
// channels, so a tune for it waits for this rather than asking yt-dlp too.
func (y *YouTube) lookAhead(id string) {
	y.mu.Lock()
	if y.ahead[id] != nil {
		y.mu.Unlock()
		return
	}
	f := &ytAhead{done: make(chan struct{})}
	y.ahead[id] = f
	height := y.cfg.MaxHeight
	y.mu.Unlock()
	tool := y.tool()
	tool.Ahead.Lock()
	y.mu.Lock()
	f.started = true
	y.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), ytResolveTime)
	info, err := y.fetch(ctx, id, height)
	cancel()
	tool.Ahead.Unlock()
	y.mu.Lock()
	delete(y.ahead, id)
	switch {
	case err == nil:
		delete(y.missed, id)
		y.keep(id, info, height)
	case youtube.Gone(err):
		if v := y.videos[id]; v != nil {
			v.Skip, v.New, v.Looked = "gone", false, y.clock()
			y.changed()
		}
		log.Printf("%s: %s can't play: %v", y.label(), id, err)
	default:
		y.missed[id] = time.Now()
		log.Printf("%s: finding %s ahead: %v", y.label(), id, err)
	}
	y.mu.Unlock()
	close(f.done)
}

// keep remembers a video's addresses until they expire, and starts
// fetching its captions. y.mu is held.
func (y *YouTube) keep(id string, info youtube.Info, height int) ytResolved {
	r := ytResolved{info: info, height: height, until: youtube.AddressesUntil(info, time.Now())}
	if old, ok := y.resolved[id]; ok && old.caps != nil && !old.caps.failed() {
		r.caps = old.caps // the same captions, fetched already or under way
	} else if sub, ok := info.Captions(); ok {
		client := y.tool().Client()
		r.caps = newCaptions(id, func(ctx context.Context) ([]byte, error) { return youtube.FetchCaptions(ctx, client, sub) })
		r.caps.start()
	}
	y.resolved[id] = r
	return r
}

// resolve finds where to stream a video from, and its captions: what was
// found ahead of time, waiting for it when that's under way, else yt-dlp
// now. fresh skips what was found before. What's found is kept for the
// next tune.
func (y *YouTube) resolve(ctx context.Context, id string, fresh bool) (ytResolved, error) {
	y.mu.Lock()
	f := y.ahead[id]
	busy := f != nil && f.started
	y.mu.Unlock()
	if busy && !fresh {
		select {
		case <-f.done:
		case <-ctx.Done():
			return ytResolved{}, ctx.Err()
		}
		phase.Step(ctx, "waited for yt-dlp")
	}
	y.mu.Lock()
	r, ok := y.resolved[id]
	height := y.cfg.MaxHeight
	if ok && !fresh && r.height == height && time.Until(r.until) >= ytMinLeft {
		if r.caps != nil && r.caps.failed() {
			r = y.keep(id, r.info, height) // the captions, tried again
		}
		y.mu.Unlock()
		phase.Step(ctx, "found ahead")
		return r, nil
	}
	if fresh {
		delete(y.resolved, id)
	}
	y.mu.Unlock()
	info, err := y.fetch(ctx, id, height)
	phase.Step(ctx, "yt-dlp")
	if err != nil {
		return ytResolved{}, err
	}
	y.mu.Lock()
	r = y.keep(id, info, height)
	y.mu.Unlock()
	return r, nil
}

// fetch asks yt-dlp where to stream a video from.
func (y *YouTube) fetch(ctx context.Context, id string, height int) (youtube.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, ytResolveTime)
	defer cancel()
	raw, err := y.tool().Run(ctx, "-J", "--no-playlist", "-f", youtube.FormatSpec(height), youtube.WatchURL(id))
	if err != nil {
		return youtube.Info{}, err
	}
	info, err := youtube.ParseInfo(raw)
	if err == nil && len(info.Inputs()) == 0 {
		err = errors.New("yt-dlp found no stream")
	}
	return info, err
}

// prefetch finds an airing's video a minute before it starts, unless it
// has been already, so the change from one video to the next is quick
// even while the channel isn't finding videos ahead.
func (y *YouTube) prefetch(ctx context.Context, a ytAiring) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Until(a.Start.Add(-time.Minute))):
	}
	y.mu.Lock()
	r, ok := y.resolved[a.ID]
	found := ok && r.height == y.cfg.MaxHeight && time.Until(r.until) >= ytMinLeft
	y.mu.Unlock()
	if !found {
		y.lookAhead(a.ID)
	}
}

// close stops the channel finding videos ahead, once it's left the
// library.
func (y *YouTube) close() {
	y.mu.Lock()
	y.closed = true
	y.mu.Unlock()
}
