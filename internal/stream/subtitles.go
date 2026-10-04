package stream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/cc"
)

// A stream whose captions come as text (a custom channel's, from its
// cc.Script) gets them as a WebVTT subtitle rendition beside the video:
// subs.m3u8 lists a .vtt segment for each video segment, with the same
// sequence numbers and lengths, so players fetch them in step, rewinding
// included.
//
// Cue times in the script count from the stream's first frame, which is
// the first picture of the first segment: ffmpeg's transcode keeps the
// source's frames one for one. Each WebVTT segment maps its own segment's
// first picture time (X-TIMESTAMP-MAP's MPEGTS, in the 90 kHz clock the
// player aligns video by) to its time in the stream (LOCAL), and gives its
// cues stream times, so they line up wherever a player starts or seeks.

const (
	subsPlaylist = "subs.m3u8"
	subsPoll     = 200 * time.Millisecond
	subsAhead    = 10 * time.Second
)

// subtitler writes a session's WebVTT segments as its video segments
// appear.
type subtitler struct {
	dir    string
	script *cc.Script
	first  int64 // PTS of the stream's first picture; -1 until known
	segs   map[int]subSegment
}

// subSegment is a video segment's place in time.
type subSegment struct {
	pts   int64 // its first picture's PTS, unwrapped
	raw   int64 // the same within the 33-bit clock
	start time.Duration
	dur   float64 // seconds, as the playlist lists it
}

func newSubtitler(dir string, script *cc.Script) *subtitler {
	return &subtitler{dir: dir, script: script, first: -1, segs: map[int]subSegment{}}
}

// run keeps the subtitles in step with the video until ctx ends.
func (s *subtitler) run(ctx context.Context) {
	t := time.NewTicker(subsPoll)
	defer t.Stop()
	for {
		_ = s.update()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// mediaPlaylist is the part of an HLS media playlist the subtitler reads.
type mediaPlaylist struct {
	target   string // EXT-X-TARGETDURATION
	sequence int    // of the first segment
	segs     []playlistSegment
}

type playlistSegment struct {
	dur float64
	uri string
}

func parseMediaPlaylist(b []byte) (mediaPlaylist, error) {
	var p mediaPlaylist
	sc := bufio.NewScanner(bytes.NewReader(b))
	dur := -1.0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			p.target = strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			p.sequence, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			dur, _ = strconv.ParseFloat(v, 64)
		case line != "" && !strings.HasPrefix(line, "#") && dur >= 0:
			p.segs = append(p.segs, playlistSegment{dur: dur, uri: line})
			dur = -1
		}
	}
	if p.target == "" {
		return p, fmt.Errorf("not a media playlist")
	}
	return p, sc.Err()
}

// update writes WebVTT for video segments new since the last call, and
// subs.m3u8 to list them.
func (s *subtitler) update() error {
	raw, err := os.ReadFile(filepath.Join(s.dir, "stream_0.m3u8"))
	if err != nil {
		return err
	}
	p, err := parseMediaPlaylist(raw)
	if err != nil || len(p.segs) == 0 {
		return err
	}
	var list bytes.Buffer
	fmt.Fprintf(&list, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%s\n#EXT-X-MEDIA-SEQUENCE:%d\n", p.target, p.sequence)
	for i, seg := range p.segs {
		n := p.sequence + i
		if _, done := s.segs[n]; !done {
			if err := s.write(n, seg); err != nil {
				break // list only what's written; try again shortly
			}
		}
		fmt.Fprintf(&list, "#EXTINF:%.6f,\n%s\n", seg.dur, subName(n))
	}
	if err := writeAtomic(filepath.Join(s.dir, subsPlaylist), list.Bytes()); err != nil {
		return err
	}
	// Drop what has left the playlist, keeping a couple for players
	// mid-fetch.
	for n := range s.segs {
		if n < p.sequence-2 {
			_ = os.Remove(filepath.Join(s.dir, subName(n)))
			delete(s.segs, n)
		}
	}
	if first, ok := s.segs[p.sequence]; ok {
		s.script.Forget(first.start - time.Minute)
	}
	return nil
}

func subName(n int) string { return fmt.Sprintf("sub_%05d.vtt", n) }

// write makes the WebVTT segment for video segment n.
func (s *subtitler) write(n int, seg playlistSegment) error {
	pts, err := firstPTS(filepath.Join(s.dir, seg.uri))
	if err != nil {
		prev, ok := s.segs[n-1]
		if !ok {
			return err
		}
		// Gone already, or unreadable: carry on from the one before.
		pts = (prev.raw + int64(prev.dur*90000)) & ptsMask
	}
	cur := subSegment{raw: pts, dur: seg.dur}
	if prev, ok := s.segs[n-1]; ok {
		cur.pts = prev.pts + (pts-prev.raw)&ptsMask
	} else if s.first < 0 {
		s.first, cur.pts = pts, pts
	} else {
		// A gap in what's been seen: the nearest way round the clock.
		cur.pts = s.first + (pts-s.first)&ptsMask
	}
	cur.start = time.Duration(cur.pts-s.first) * time.Second / 90000
	end := cur.start + time.Duration(seg.dur*float64(time.Second))

	b := fmt.Appendf(nil, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:%d,LOCAL:%s\n\n", pts, cc.VTTTime(cur.start))
	// Cues starting a little later come too, so players that lay captions
	// out ahead know what's coming; they're repeated in their own segments.
	for _, c := range s.script.Between(cur.start, end+subsAhead) {
		b = cc.AppendVTT(b, c)
	}
	if err := writeAtomic(filepath.Join(s.dir, subName(n)), b); err != nil {
		return err
	}
	s.segs[n] = cur
	return nil
}

const ptsMask = 1<<33 - 1

// firstPTS reads the presentation time of a segment's first picture, in
// 90 kHz ticks.
func firstPTS(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	pkt := make([]byte, 188)
	for range 4096 {
		if _, err := io.ReadFull(r, pkt); err != nil {
			return 0, fmt.Errorf("%s: no picture time", filepath.Base(path))
		}
		if pkt[0] != 0x47 || pkt[1]&0x40 == 0 || pkt[3]&0x10 == 0 {
			continue
		}
		p := pkt[4:]
		if pkt[3]&0x20 != 0 {
			if int(pkt[4]) >= 183 {
				continue
			}
			p = pkt[5+int(pkt[4]):]
		}
		// A video PES with a PTS.
		if len(p) >= 14 && p[0] == 0 && p[1] == 0 && p[2] == 1 && p[3]&0xf0 == 0xe0 && p[7]&0x80 != 0 {
			return int64(p[9]>>1&7)<<30 | int64(p[10])<<22 | int64(p[11]>>1)<<15 | int64(p[12])<<7 | int64(p[13]>>1), nil
		}
	}
	return 0, fmt.Errorf("%s: no picture time", filepath.Base(path))
}

// writeAtomic replaces a file so readers never see it half written.
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
