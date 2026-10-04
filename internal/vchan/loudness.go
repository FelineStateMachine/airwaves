package vchan

// Loudness: custom channels' sound can be evened out to one level, as ATSC
// A/85 asks of broadcast TV (-24 LKFS), so a loud YouTube upload doesn't
// follow a quiet Jellyfin episode. Nothing extra is read for it: the sound
// is metered as it streams, in the raw samples each decoder hands the
// encoder (ITU-R BS.1770: K-weighted, gated 400 ms blocks), and turned up
// or down slowly toward the target as each video's loudness becomes
// known. What a video measured is remembered, so its next airing starts
// at the right level.

import (
	"encoding/json"
	"math"
	"os"
	"sync"
)

// DefaultLoudness is broadcast TV's level (ATSC A/85), in LUFS (the same
// as LKFS): the level to even custom channels out to.
const DefaultLoudness = -24.0

// loudness is what airings of a video measured: its gated integrated
// loudness, over so many seconds of gated sound.
type loudness struct {
	LUFS    float64 `json:"lufs"`
	Seconds float64 `json:"seconds,omitempty"`
}

const (
	minGain = -20.0
	maxGain = 12.0
	// quietest is the loudness below which a video is taken for silent
	// and left as it is, rather than raised.
	quietest = -50.0
	// peakCeiling is the sample peak the leveled sound is held under,
	// -1 dBFS, so raised sound doesn't clip.
	peakCeiling = 0.891
)

// gainFor is the gain in dB that brings sound of lufs to target: within
// -20 to +12 dB, and none for near silence.
func gainFor(lufs, target float64) float64 {
	if target == 0 || math.IsNaN(lufs) || lufs < quietest {
		return 0
	}
	return min(max(target-lufs, minGain), maxGain)
}

// levelMemory remembers videos' loudness from one airing to the next.
type levelMemory interface {
	recall(key string) (loudness, bool)
	remember(key string, l loudness)
}

// itemLevel says how to level an item's sound.
type itemLevel struct {
	Target float64 // LUFS
	// Key is the video in Memory, which remembers what it measures; nil
	// remembers nothing.
	Key    string
	Memory levelMemory
	// Assumed is the loudness taken for a video not yet measured, 0 for
	// the target's.
	Assumed float64
}

// start is the gain an airing starts at: from what the video measured
// before, else what's assumed of it.
func (lv *itemLevel) start() float64 {
	if lv.Memory != nil {
		if l, ok := lv.Memory.recall(lv.Key); ok {
			return gainFor(l.LUFS, lv.Target)
		}
	}
	if lv.Assumed != 0 {
		return gainFor(lv.Assumed, lv.Target)
	}
	return 0
}

// remembered merges a new measurement into what was known: an average of
// their energies, by seconds measured, the old counting at most an hour
// so new airings keep mattering.
func remembered(old, new loudness, known bool) loudness {
	if !known || old.Seconds <= 0 {
		return new
	}
	wo, wn := min(old.Seconds, 3600), new.Seconds
	e := (wo*math.Pow(10, old.LUFS/10) + wn*math.Pow(10, new.LUFS/10)) / (wo + wn)
	return loudness{LUFS: math.Round(100*10*math.Log10(e)) / 100, Seconds: min(old.Seconds+new.Seconds, 1e6)}
}

// The meter: BS.1770's K-weighting at 48 kHz, two biquads per channel.
var kWeight = [2]biquad{
	{b0: 1.53512485958697, b1: -2.69169618940638, b2: 1.19839281085285, a1: -1.69065929318241, a2: 0.73248077421585},
	{b0: 1.0, b1: -2.0, b2: 1.0, a1: -1.99004745483398, a2: 0.99007225036621},
}

type biquad struct{ b0, b1, b2, a1, a2 float64 }

const (
	subBlock = sampleRate / 10 // 100 ms: a quarter block, the gating step
	// The gated loudness is kept as a histogram of block loudness, 0.1 dB
	// a bin, from the absolute gate (-70 LUFS) to +5.
	histFloor = -70.0
	histBins  = 750
)

// meter measures the gated integrated loudness of 48 kHz stereo (ITU-R
// BS.1770-4): mean squares of K-weighted sound over 400 ms blocks, every
// 100 ms; blocks under -70 LUFS left out, then those 10 LU under the rest.
type meter struct {
	state  [2][2][2]float64 // by channel and stage, the filters' state
	subE   [4]float64       // the last four 100 ms energies
	subs   int              // 100 ms parts measured
	acc    float64          // energy of the 100 ms part under way
	n      int              // its samples so far
	count  [histBins]int32  // blocks over the absolute gate, by loudness
	energy [histBins]float64
	blocks int
	total  float64
}

// add meters one stereo sample, reporting whether it completed a 100 ms
// step.
func (m *meter) add(l, r float64) bool {
	for c, x := range [2]float64{l, r} {
		for s := range kWeight {
			q, st := &kWeight[s], &m.state[c][s]
			y := q.b0*x + st[0]
			st[0] = q.b1*x - q.a1*y + st[1]
			st[1] = q.b2*x - q.a2*y
			x = y
		}
		m.acc += x * x
	}
	if m.n++; m.n < subBlock {
		return false
	}
	m.subE[m.subs%4] = m.acc
	m.subs++
	m.acc, m.n = 0, 0
	if m.subs >= 4 {
		z := (m.subE[0] + m.subE[1] + m.subE[2] + m.subE[3]) / (4 * subBlock)
		if l := blockLoudness(z); l >= histFloor {
			b := min(int((l-histFloor)*10), histBins-1)
			m.count[b]++
			m.energy[b] += z
			m.blocks++
			m.total += z
		}
	}
	return true
}

func blockLoudness(z float64) float64 { return -0.691 + 10*math.Log10(z) }

// integrated is the gated loudness so far, and how many seconds of sound
// it covers; NaN before any sound.
func (m *meter) integrated() (lufs, seconds float64) {
	if m.blocks == 0 {
		return math.NaN(), 0
	}
	gate := blockLoudness(m.total/float64(m.blocks)) - 10
	from := max(int(math.Ceil((gate-histFloor)*10)), 0)
	var e float64
	var n int32
	for b := from; b < histBins; b++ {
		e += m.energy[b]
		n += m.count[b]
	}
	if n == 0 {
		return math.NaN(), 0
	}
	return blockLoudness(e / float64(n)), float64(n) / 10
}

// The automatic gain: it holds the start gain until a few seconds of sound
// are measured, then moves toward the gain the measured loudness asks for,
// trusting it more as more is measured, no faster than 3 dB a second.
const (
	settle    = 3.0  // seconds measured before the gain moves
	trusted   = 12.0 // seconds measured for the measurement to count fully
	trustedIf = 60.0 // likewise when the video's loudness was remembered
	glide     = 2.0  // the time constant of the gain's moves, seconds
	maxSlew   = 0.3  // dB per 100 ms step: 3 dB a second at most
	// release is how fast the peak guard lets go, per sample: 50 ms.
	release = 1.0 / (0.05 * sampleRate)
)

// leveler levels one airing's sound as it streams.
type leveler struct {
	lv      *itemLevel
	m       meter
	start   float64 // dB
	recall  bool    // start came from a measurement
	gain    float64 // dB, where the gain is heading this step
	cur     float64 // linear gain on the next sample
	step    float64 // its change per sample this step
	guard   float64 // the peak guard's gain, linear, at most 1
	samples int
}

func newLeveler(lv *itemLevel) *leveler {
	l := &leveler{lv: lv, start: lv.start(), guard: 1}
	if lv.Memory != nil {
		_, l.recall = lv.Memory.recall(lv.Key)
	}
	l.gain = l.start
	l.cur = math.Pow(10, l.gain/20)
	return l
}

// process levels raw 16-bit stereo samples in place.
func (l *leveler) process(b []byte) {
	for i := 0; i+4 <= len(b); i += 4 {
		left := float64(int16(uint16(b[i])|uint16(b[i+1])<<8)) / 32768
		right := float64(int16(uint16(b[i+2])|uint16(b[i+3])<<8)) / 32768
		if l.m.add(left, right) {
			l.adjust()
		}
		g := l.cur * l.guard
		peak := max(math.Abs(left), math.Abs(right)) * g
		if peak > peakCeiling {
			l.guard = peakCeiling / (max(math.Abs(left), math.Abs(right)) * l.cur)
			g = l.cur * l.guard
		} else if l.guard < 1 {
			l.guard += (1 - l.guard) * release
		}
		putSample(b[i:], left*g)
		putSample(b[i+2:], right*g)
		l.cur += l.step
		l.samples++
	}
}

func putSample(b []byte, x float64) {
	v := int32(math.Round(x * 32768))
	v = min(max(v, -32768), 32767)
	b[0], b[1] = byte(v), byte(v>>8)
}

// adjust sets the gain's course for the next 100 ms.
func (l *leveler) adjust() {
	want := l.start
	lufs, secs := l.m.integrated()
	if secs >= settle && !math.IsNaN(lufs) {
		full := trusted
		if l.recall {
			full = trustedIf
		}
		trust := min((secs-settle)/(full-settle), 1)
		want = l.start + (gainFor(lufs, l.lv.Target)-l.start)*trust
	}
	move := (want - l.gain) * 0.1 / glide
	l.gain += min(max(move, -maxSlew), maxSlew)
	next := math.Pow(10, l.gain/20)
	l.step = (next - l.cur) / subBlock
}

// finish remembers what the airing measured.
func (l *leveler) finish() {
	if l.lv.Memory == nil {
		return
	}
	lufs, secs := l.m.integrated()
	if secs < settle || math.IsNaN(lufs) {
		return
	}
	old, known := l.lv.Memory.recall(l.lv.Key)
	l.lv.Memory.remember(l.lv.Key, remembered(old, loudness{LUFS: math.Round(lufs*100) / 100, Seconds: secs}, known))
}

// loudnessFile remembers loudness in a hidden file in a channel's folder,
// by key.
type loudnessFile struct {
	mu     sync.Mutex
	path   string
	loaded bool
	m      map[string]loudness
	keep   map[string]bool // the keys in use; others are dropped on saving
}

// loudnessFileName names the file in a channel's folder.
const loudnessFileName = ".loudness.json"

func (c *loudnessFile) load() {
	if c.loaded {
		return
	}
	c.loaded, c.m = true, map[string]loudness{}
	if raw, err := os.ReadFile(c.path); err == nil && c.path != "" {
		_ = json.Unmarshal(raw, &c.m)
	}
}

func (c *loudnessFile) recall(key string) (loudness, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	l, ok := c.m[key]
	return l, ok
}

// count is how many of keys are remembered.
func (c *loudnessFile) count(keys []string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	n := 0
	for _, k := range keys {
		if _, ok := c.m[k]; ok {
			n++
		}
	}
	return n
}

// use sets the file and the keys in use.
func (c *loudnessFile) use(path string, keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if path != c.path {
		c.path, c.loaded = path, false
	}
	c.keep = map[string]bool{}
	for _, k := range keys {
		c.keep[k] = true
	}
}

// remember records a measurement and saves the file, whole or not at all.
func (c *loudnessFile) remember(key string, l loudness) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.m[key] = l
	for k := range c.m {
		if c.keep != nil && !c.keep[k] && k != key {
			delete(c.m, k)
		}
	}
	raw, err := json.Marshal(c.m)
	if err != nil || c.path == "" {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// memoryLevels remembers loudness while the server runs.
type memoryLevels struct {
	mu sync.Mutex
	m  map[string]loudness
}

func (c *memoryLevels) recall(key string) (loudness, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.m[key]
	return l, ok
}

func (c *memoryLevels) remember(key string, l loudness) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]loudness{}
	}
	c.m[key] = l
}
