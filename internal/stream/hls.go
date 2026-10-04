// Package stream turns a tuner input or a recording into HLS with ffmpeg.
// Each client (a desktop app, a browser) gets its own session; starting a
// new stream for a client replaces its previous one.
package stream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/phase"
	"airwaves/internal/tuner"
)

// maxSessions bounds concurrent transcodes; the oldest is evicted.
const maxSessions = 4

// Playback locates a running stream.
type Playback struct {
	ID string `json:"id"`
	// URL is absolute when the server can tell (loopback mode, or rewritten
	// by a remote client).
	URL string `json:"url"`
	// Path is the playlist relative to the server's origin.
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
	// Offset is where a recording's playback starts, in seconds, so the
	// player can show positions in the whole recording.
	Offset float64 `json:"offset,omitempty"`
	// Timing lists when each step of starting the stream happened, from
	// the tune call (see phase).
	Timing []phase.Mark `json:"timing,omitempty"`
}

// Server owns the ffmpeg sessions and serves their files under /live/.
type Server struct {
	ffmpeg string
	root   string
	ln     net.Listener
	srv    *http.Server

	mu       sync.Mutex
	sessions map[string]*session // by client
}

type session struct {
	id      string
	client  string
	dir     string
	started time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	stderr  *tail
}

// FindFFmpeg locates the ffmpeg binary.
func FindFFmpeg() (string, error) {
	if bin, err := exec.LookPath("ffmpeg"); err == nil {
		return bin, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("ffmpeg not found; install it with `brew install ffmpeg`")
}

// NewServer prepares a stream server. With loopback set it also listens on
// 127.0.0.1 so a local player can fetch streams directly; otherwise mount
// it on an existing HTTP server.
func NewServer(loopback bool) (*Server, error) {
	bin, err := FindFFmpeg()
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "airwaves-hls-")
	if err != nil {
		return nil, err
	}
	s := &Server{ffmpeg: bin, root: root, sessions: make(map[string]*session)}
	if loopback {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		s.ln = ln
		s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = s.srv.Serve(ln) }()
	}
	return s, nil
}

// FFmpeg returns the ffmpeg binary in use.
func (s *Server) FFmpeg() string { return s.ffmpeg }

// FFprobe returns the ffprobe binary beside ffmpeg.
func (s *Server) FFprobe() string { return filepath.Join(filepath.Dir(s.ffmpeg), "ffprobe") }

// Start replaces client's stream with one from in. It returns once the
// first segments are ready, or with ffmpeg's error output. When calls for
// the same client overlap (fast channel surfing) the last one wins and
// earlier ones fail; no ffmpeg process outlives its replacement.
func (s *Server) Start(ctx context.Context, client string, in tuner.Input) (*Playback, error) {
	// The steps are timed from the tune call when its context has a timer.
	t := phase.FromContext(ctx)
	if t == nil {
		t = phase.New("stream", "")
	}
	// Stop first so a network tuner frees its tuner before the next tune.
	if s.stop(client) {
		t.Mark("previous stopped")
	}

	id := newID()
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(phase.NewContext(context.Background(), t))
	// ffmpeg reports its progress on fd 3, which tells when it has
	// encoded its first frame.
	progress, progressW, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	args := append([]string{"-progress", "pipe:3", "-stats_period", "0.1"}, ffmpegArgs(in, dir)...)
	cmd := exec.CommandContext(runCtx, s.ffmpeg, args...)
	cmd.ExtraFiles = []*os.File{progressW}
	sess := &session{
		id: id, client: client, dir: dir, started: time.Now(),
		cancel: cancel, done: make(chan struct{}), stderr: &tail{max: 4096},
	}
	cmd.Stderr = sess.stderr
	var source io.WriteCloser
	if in.Source != nil {
		if source, err = cmd.StdinPipe(); err != nil {
			progress.Close()
			progressW.Close()
			cancel()
			return nil, err
		}
	}
	err = cmd.Start()
	progressW.Close()
	if err != nil {
		progress.Close()
		cancel()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	t.Mark("ffmpeg started")
	go watchProgress(progress, func() { t.Mark("first frame") })
	if source != nil {
		go func() {
			defer source.Close()
			w := &firstWrite{w: source, mark: func() { t.Mark("first data") }}
			if err := in.Source(runCtx, w); err != nil && runCtx.Err() == nil {
				fmt.Fprintf(sess.stderr, "%v\n", err)
			}
		}()
	}
	if subtitled(in) {
		go newSubtitler(dir, in.Subtitles).run(runCtx)
	}
	go func() {
		_ = cmd.Wait()
		close(sess.done)
	}()

	var evict []*session
	s.mu.Lock()
	if prev := s.sessions[client]; prev != nil {
		evict = append(evict, prev)
	}
	s.sessions[client] = sess
	for len(s.sessions) > maxSessions {
		oldest := sess
		for _, x := range s.sessions {
			if x.started.Before(oldest.started) {
				oldest = x
			}
		}
		delete(s.sessions, oldest.client)
		evict = append(evict, oldest)
	}
	s.mu.Unlock()
	for _, x := range evict {
		x.stop()
	}

	if err := waitReady(ctx, sess, playlists(in), t); err != nil {
		s.mu.Lock()
		if s.sessions[client] == sess {
			delete(s.sessions, client)
		}
		s.mu.Unlock()
		sess.stop()
		log.Printf("%s failed after %.1fs: %s", t.Label, t.Since().Seconds(), t)
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte(masterPlaylist(in)), 0o644); err != nil {
		s.Stop(client)
		return nil, err
	}
	t.Mark("ready")
	pb := &Playback{ID: id, Path: "/live/" + id + "/master.m3u8", Note: in.Note, Offset: in.Offset, Timing: t.Marks()}
	if s.ln != nil {
		pb.URL = "http://" + s.ln.Addr().String() + pb.Path
	}
	log.Printf("%s: %s", t.Label, t)
	phase.Tunes.Add(t.Kind, phase.Ready, t.Since())
	return pb, nil
}

// firstWrite marks the first write to w.
type firstWrite struct {
	w    io.Writer
	once sync.Once
	mark func()
}

func (f *firstWrite) Write(p []byte) (int, error) {
	f.once.Do(f.mark)
	return f.w.Write(p)
}

// waitReady blocks until every media playlist lists two segments, marking
// on t when the first segment is done.
func waitReady(ctx context.Context, sess *session, lists []string, t *phase.Timer) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stream did not start in time: %s", sess.stderr.String())
		case <-sess.done:
			return fmt.Errorf("ffmpeg exited: %s", sess.stderr.String())
		case <-tick.C:
			ready := 0
			for i, name := range lists {
				b, err := os.ReadFile(filepath.Join(sess.dir, name))
				n := bytes.Count(b, []byte("#EXTINF"))
				if err == nil && i == 0 && n >= 1 {
					t.Mark("first segment")
				}
				if err == nil && n >= 2 {
					ready++
				}
			}
			if ready == len(lists) {
				return nil
			}
		}
	}
}

// watchProgress reads ffmpeg's progress reports until it exits, calling
// encoded once the first frame is out.
func watchProgress(r io.ReadCloser, encoded func()) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	done := false
	for sc.Scan() {
		if n, ok := strings.CutPrefix(sc.Text(), "frame="); ok && !done {
			if frames, _ := strconv.Atoi(strings.TrimSpace(n)); frames > 0 {
				encoded()
				done = true
			}
		}
	}
	// ffmpeg must never wait on a report nobody reads.
	_, _ = io.Copy(io.Discard, r)
}

// liveWindow is how far back a viewer can pause or rewind live TV.
const liveWindow = 30 * time.Minute

// variants is how many media playlists ffmpeg writes: the video (with
// its audio when there is a single track), plus one per audio track when
// there are several.
func variants(in tuner.Input) int {
	if len(in.Audio) > 1 {
		return 1 + len(in.Audio)
	}
	return 1
}

// playlists names a session's media playlists: ffmpeg's, and the
// subtitles' when it has them.
func playlists(in tuner.Input) []string {
	var out []string
	for i := range variants(in) {
		out = append(out, fmt.Sprintf("stream_%d.m3u8", i))
	}
	if subtitled(in) {
		out = append(out, subsPlaylist)
	}
	return out
}

// subtitled reports whether a session has a WebVTT subtitle rendition:
// live streams with their captions as text.
func subtitled(in tuner.Input) bool { return in.Subtitles != nil && !in.VOD }

// ffmpegArgs transcodes to H.264 and AAC in HLS. Several audio tracks become
// separate renditions ("v:0 a:0 a:1"); closed captions (CEA-608 in the
// picture of a broadcast or a custom channel) are carried into the H.264
// stream by libx264.
func ffmpegArgs(in tuner.Input, dir string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, probeArgs(in)...)
	args = append(args, in.Args...)
	args = append(args, "-map", in.Video)
	for _, a := range in.Audio {
		args = append(args, "-map", a.Map)
	}
	if in.Broadcast {
		// Deinterlace only frames flagged interlaced (1080i, 480i).
		args = append(args, "-vf", "bwdif=mode=send_frame:deint=interlaced")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
		"-profile:v", "high", "-crf", "21", "-maxrate", "8M", "-bufsize", "12M",
		"-g", "60", "-keyint_min", "60", "-sc_threshold", "0",
		"-force_key_frames", "expr:gte(t,n_forced*2)", "-a53cc", "1",
	)
	streamMap := "v:0"
	switch len(in.Audio) {
	case 0:
	case 1:
		streamMap += ",a:0"
	default:
		for i := range in.Audio {
			streamMap += fmt.Sprintf(" a:%d", i)
		}
	}
	if len(in.Audio) > 0 {
		args = append(args, "-c:a", "aac", "-b:a", "160k", "-ac", "2", "-ar", "48000")
	}
	args = append(args, "-f", "hls", "-var_stream_map", streamMap)
	if in.VOD {
		// Keep every segment so the player can seek back to the start.
		args = append(args, "-hls_time", "4", "-hls_list_size", "0",
			"-hls_playlist_type", "event", "-hls_flags", "independent_segments+temp_file")
	} else {
		// A sliding window long enough to pause and rewind live TV.
		segments := int(liveWindow / (2 * time.Second))
		args = append(args, "-hls_time", "2", "-hls_list_size", strconv.Itoa(segments),
			"-hls_flags", "delete_segments+independent_segments+omit_endlist+temp_file")
	}
	return append(args,
		"-hls_segment_filename", filepath.Join(dir, "seg_%v_%05d.ts"),
		filepath.Join(dir, "stream_%v.m3u8"),
	)
}

// probeArgs keep ffmpeg's first look at a live input short. Before it
// starts, ffmpeg reads an MPEG-TS input for as long as it may analyze it
// (5 seconds by default) even once it knows every stream, and for a live
// input that is as long a wait. Half a second finds a broadcast's audio
// tracks and a custom channel's picture and sound. A broadcast's picture
// size may only come with its first full frame, later, which ffmpeg takes
// in stride. A recording is read as fast as it can be, so it keeps the
// default.
func probeArgs(in tuner.Input) []string {
	if in.VOD {
		return nil
	}
	return []string{"-probesize", "2000000", "-analyzeduration", "500000"}
}

// masterPlaylist describes the session's streams to the player: the video,
// each audio track as a selectable rendition, and closed captions.
func masterPlaylist(in tuner.Input) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	codecs, attrs := "avc1.640028", ""
	if len(in.Audio) > 0 {
		codecs += ",mp4a.40.2"
	}
	if len(in.Audio) > 1 {
		for i, a := range in.Audio {
			name, lang := TrackName(a, i)
			def := "NO"
			if i == 0 {
				def = "YES"
			}
			extra := ""
			if a.Described {
				extra = `,CHARACTERISTICS="public.accessibility.describes-video"`
			}
			fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES%s,URI=\"stream_%d.m3u8\"\n",
				name, lang, def, extra, i+1)
		}
		attrs += `,AUDIO="aud"`
	}
	// Captions are never picked by the player (AUTOSELECT=NO): the app turns
	// them on and off, as the viewer asked or the program needs.
	switch {
	case subtitled(in):
		// Text rather than the same captions in the picture: one track.
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"English\",LANGUAGE=\"en\",DEFAULT=NO,AUTOSELECT=NO,FORCED=NO,URI=%q\n", subsPlaylist)
		attrs += `,SUBTITLES="subs",CLOSED-CAPTIONS=NONE`
	case in.Broadcast || in.Captions:
		b.WriteString(`#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="English",LANGUAGE="en",INSTREAM-ID="CC1",DEFAULT=NO,AUTOSELECT=NO` + "\n")
		attrs += `,CLOSED-CAPTIONS="cc"`
	default:
		attrs += ",CLOSED-CAPTIONS=NONE"
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=8000000,CODECS=%q%s\nstream_0.m3u8\n", codecs, attrs)
	return b.String()
}

// languages maps the ISO 639-2 codes broadcasters use to BCP 47 tags and
// display names.
var languages = map[string][2]string{
	"eng": {"en", "English"}, "spa": {"es", "Spanish"}, "fre": {"fr", "French"}, "fra": {"fr", "French"},
	"kor": {"ko", "Korean"}, "vie": {"vi", "Vietnamese"}, "chi": {"zh", "Chinese"}, "zho": {"zh", "Chinese"},
	"rus": {"ru", "Russian"}, "por": {"pt", "Portuguese"}, "ger": {"de", "German"}, "deu": {"de", "German"},
	"ita": {"it", "Italian"}, "jpn": {"ja", "Japanese"}, "tgl": {"tl", "Tagalog"}, "ara": {"ar", "Arabic"},
	"hin": {"hi", "Hindi"},
}

// TrackName labels an audio track for the player: "Spanish",
// "English (Described)", or "Audio 2" when the broadcast gives no language.
func TrackName(a tuner.AudioTrack, i int) (name, lang string) {
	l, ok := languages[strings.ToLower(a.Lang)]
	switch {
	case ok:
		name, lang = l[1], l[0]
	case a.Lang != "":
		name, lang = strings.ToUpper(a.Lang), strings.ToLower(a.Lang)
	default:
		name, lang = fmt.Sprintf("Audio %d", i+1), "und"
	}
	if a.Described {
		name += " (Described)"
	}
	return name, lang
}

// Stop ends client's stream and removes its files.
func (s *Server) Stop(client string) { s.stop(client) }

// stop ends client's stream, reporting whether it had one.
func (s *Server) stop(client string) bool {
	s.mu.Lock()
	sess := s.sessions[client]
	delete(s.sessions, client)
	s.mu.Unlock()
	if sess != nil {
		sess.stop()
	}
	return sess != nil
}

func (sess *session) stop() {
	sess.cancel()
	select {
	case <-sess.done:
	case <-time.After(3 * time.Second):
	}
	_ = os.RemoveAll(sess.dir)
}

// Close stops every stream and the loopback listener.
func (s *Server) Close() {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, x := range s.sessions {
		all = append(all, x)
	}
	s.sessions = make(map[string]*session)
	s.mu.Unlock()
	for _, x := range all {
		x.stop()
	}
	if s.srv != nil {
		_ = s.srv.Close()
	}
	_ = os.RemoveAll(s.root)
}

// ServeHTTP serves /live/<id>/<file> for live sessions.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	rest, ok := strings.CutPrefix(r.URL.Path, "/live/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, file, ok := strings.Cut(rest, "/")
	var sess *session
	s.mu.Lock()
	for _, x := range s.sessions {
		if x.id == id {
			sess = x
		}
	}
	s.mu.Unlock()
	if !ok || sess == nil || file != filepath.Base(file) {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(file) {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".vtt":
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	default:
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(sess.dir, file))
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// tail keeps the last max bytes written, for error messages.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.buf))
	if s == "" {
		return "no output"
	}
	return s
}

// ProbeAudio lists the audio tracks of a recording with ffprobe, in stream
// order. AC-4 tracks (ATSC 3.0) cannot be decoded and are skipped.
func (s *Server) ProbeAudio(ctx context.Context, url string) ([]tuner.AudioTrack, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.FFprobe(), "-v", "error", "-seekable", "0",
		"-probesize", "8M", "-analyzeduration", "5M", "-select_streams", "a",
		"-show_entries", "stream=codec_name:stream_tags=language:stream_disposition=visual_impaired",
		"-of", "json", url).Output()
	if err != nil {
		return nil, fmt.Errorf("probe recording: %w", err)
	}
	var body struct {
		Streams []struct {
			Codec       string            `json:"codec_name"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil, fmt.Errorf("probe recording: %w", err)
	}
	var tracks []tuner.AudioTrack
	for i, st := range body.Streams {
		if st.Codec == "" || st.Codec == "ac4" {
			continue
		}
		tracks = append(tracks, tuner.AudioTrack{
			Map: fmt.Sprintf("0:a:%d", i), Lang: st.Tags["language"], Described: st.Disposition["visual_impaired"] == 1,
		})
	}
	return tracks, nil
}
