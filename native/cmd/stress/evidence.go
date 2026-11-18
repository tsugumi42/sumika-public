package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type roundRecord struct {
	Session, TurnID      string
	Round                int
	Started              time.Time
	Elapsed              time.Duration
	Steer, Stop, Compact bool
	Errors               []string
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// 保留每轮、全部请求和完整运行记录；工具输出是本次短文件的完整 preview。
// 失败附上该会话的全部请求，压缩、插话前后的上下文都能查到，不只截最后一次。
func (k *kernel) evidence(dir string, rounds []*roundRecord) error {
	trace := map[string][]map[string]any{}
	for _, table := range []string{"turns", "steps", "model_runs"} {
		rows, err := k.set.Trace().Query("SELECT * FROM " + table + " ORDER BY started_at, id")
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			record := make(map[string]any, len(columns))
			for i, name := range columns {
				if b, ok := values[i].([]byte); ok {
					values[i] = string(b)
				}
				record[name] = values[i]
			}
			trace[table] = append(trace[table], record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	k.mu.Lock()
	requests := append([]requestRecord(nil), k.records...)
	k.mu.Unlock()
	type failure struct {
		Round    *roundRecord
		Requests []requestRecord
		Steps    []map[string]any
	}
	failures := []failure{}
	for _, rec := range rounds {
		var steps []map[string]any
		for _, step := range trace["steps"] {
			if rec.TurnID != "" && step["turn_id"] == rec.TurnID {
				steps = append(steps, step)
				if step["kind"] == "tool" && (step["is_error"] == int64(1) || step["ended_at"] == nil) {
					rec.Errors = append(rec.Errors, fmt.Sprintf("工具 %v：status=%v，输出=%v", step["name"], step["status"], step["preview"]))
				}
			}
		}
		if len(rec.Errors) == 0 {
			continue
		}
		f := failure{Round: rec, Steps: steps}
		for _, req := range requests {
			if req.Session == rec.Session {
				f.Requests = append(f.Requests, req)
			}
		}
		failures = append(failures, f)
	}
	for _, artifact := range []struct {
		name  string
		value any
	}{
		{"rounds.json", rounds}, {"requests.json", requests}, {"trace.json", trace}, {"failures.json", failures},
	} {
		if err := writeJSON(filepath.Join(dir, artifact.name), artifact.value); err != nil {
			return err
		}
	}
	return nil
}
