package app

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sumika-native/internal/memwrite"
)

func (a *App) transitionSummaries(ctx context.Context, old, next *ModelRuntime) (map[string]string, bool) {
	a.memMu.Lock()
	defer a.memMu.Unlock()
	reasons, changed := old.summaryErrors, false
	for _, id := range a.memoryIDs {
		hadModel := old.Values.Background(id) != ""
		hasModel := next.Values.Background(id) != ""
		if hadModel == hasModel {
			continue
		}
		if !changed {
			reasons = maps.Clone(reasons)
			changed = true
		}
		delete(reasons, id)
		note := func(err error) {
			if reasons == nil {
				reasons = map[string]string{}
			}
			reasons[id] = err.Error()
		}
		m := a.memories[id]
		if !hasModel {
			m.SetSummaryEnabled(false, false)
			if m.Writable() {
				if err := m.ResetCatchUp(ctx); err != nil {
					note(err)
				}
			}
			continue
		}
		if !a.cfg.Memory.Write.Summarizes(id) {
			continue
		}
		db := a.paths.MemoryDB(id)
		in := a.memoryWriteInput(id, db)
		in.HasModel = true
		if err := memwrite.EnableSummary(ctx, in, m); err != nil {
			note(err)
		}
	}
	return reasons, changed
}

func modelApplyErrors(reasons map[string]string) []string {
	ids := make([]string, 0, len(reasons))
	for id := range reasons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%s：%s", id, reasons[id]))
	}
	return out
}
