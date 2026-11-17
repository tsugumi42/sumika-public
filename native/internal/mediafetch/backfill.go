package mediafetch

// BackfillKey 不含 URL；只供宿主把轮次目标和现有工人状态对齐。
type BackfillKey struct {
	Seq     int64
	Index   int
	Forward string
	Node    int
}
type BackfillActivity struct {
	States   map[BackfillKey]string
	Sweeping bool
}

func (f *Fetcher) SetBackfillNotify(notify func()) {
	f.mu.Lock()
	f.backfillNotify = notify
	f.mu.Unlock()
}
func (f *Fetcher) notifyBackfillLocked() {
	if f.backfillNotify != nil {
		f.backfillNotify()
	}
}
func (f *Fetcher) BackfillActivity() BackfillActivity {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := BackfillActivity{States: make(map[BackfillKey]string, len(f.inflight)), Sweeping: len(f.sweep1) > 0 || f.backfillSweeping}
	for k := range f.inflight {
		state := "queued"
		if f.backfillRunning[k] {
			state = "running"
		}
		v.States[BackfillKey{k.seq, k.idx, k.forward, k.node}] = state
	}
	return v
}
func (f *Fetcher) backfillStarted(j *job) {
	f.mu.Lock()
	if f.backfillRunning == nil {
		f.backfillRunning = map[key]bool{}
	}
	f.backfillRunning[jobKey(j)] = true
	f.notifyBackfillLocked()
	f.mu.Unlock()
}
func (f *Fetcher) backfillSweep(start bool) {
	f.mu.Lock()
	f.backfillSweeping = start
	f.notifyBackfillLocked()
	f.mu.Unlock()
}
