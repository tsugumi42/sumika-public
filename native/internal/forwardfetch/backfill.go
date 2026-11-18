package forwardfetch

// BackfillActivity 只给轮次关联缺树任务；不把树的文本或远端地址交给状态接口。
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
func (f *Fetcher) BackfillActivity() (map[string]bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.pending))
	for id := range f.pending {
		out[id] = true
	}
	return out, f.backfillSweeping
}
func (f *Fetcher) backfillSweep(start bool) {
	f.mu.Lock()
	f.backfillSweeping = start
	f.notifyBackfillLocked()
	f.mu.Unlock()
}
