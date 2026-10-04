package vchan

// YouTube videos' loudness is measured as they air and kept in the
// catalog, so the next airing starts at the right level.

// ytAssumed is the loudness taken for a video not yet measured: YouTube's
// own playback level, which most uploads are near.
const ytAssumed = -14.0

// ytMemory remembers videos' loudness in the catalog.
type ytMemory struct{ y *YouTube }

func (m ytMemory) recall(id string) (loudness, bool) {
	m.y.mu.Lock()
	defer m.y.mu.Unlock()
	if v := m.y.videos[id]; v != nil && v.Loudness != nil {
		return *v.Loudness, true
	}
	return loudness{}, false
}

func (m ytMemory) remember(id string, l loudness) {
	m.y.mu.Lock()
	defer m.y.mu.Unlock()
	if v := m.y.videos[id]; v != nil {
		v.Loudness = &l
		m.y.catDirty = true
	}
}

// level is how an airing's sound is leveled, nil for not at all.
func (y *YouTube) level(id string) *itemLevel {
	if y.Loudness == 0 {
		return nil
	}
	return &itemLevel{Target: y.Loudness, Key: id, Memory: ytMemory{y}, Assumed: ytAssumed}
}

// Leveled is how many of the videos scheduled from now on, or a
// playlist's, have had their loudness measured as they aired, of how
// many.
func (y *YouTube) Leveled() (measured, total int) {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.tend()
	if y.isPlaylist() {
		seq, _ := y.sequence()
		for _, v := range seq {
			if v.Loudness != nil {
				measured++
			}
		}
		return measured, len(seq)
	}
	now := y.clock()
	seen := map[string]bool{}
	for _, a := range y.plan.Airings {
		if !a.End.After(now) || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		total++
		if v := y.videos[a.ID]; v != nil && v.Loudness != nil {
			measured++
		}
	}
	return measured, total
}
