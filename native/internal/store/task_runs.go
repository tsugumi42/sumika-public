package store

import "sumika-native/internal/taskrun"

// TaskRun 是 task_runs 的一行（规格-运行页 第七节）：模型调用以外的任务的一次结局。
// Detail 必须是调用方已经脱敏、截到 500 字节以内的（internal/taskrun.Detail），这里不再改写，只在超长时拒收。
type TaskRun struct {
	At       int64 // 开始时间，毫秒
	Module   string
	Outcome  string // ok / failed / timeout / cancelled / degraded / skipped
	Class    string // 失败或退化的原因类别，ok 时为空
	Ms       *int64
	Attempts int
	RootID   string
	Detail   string
}

// MaxTaskDetail 是 detail 的字节上限
const MaxTaskDetail = 500

// AddTaskRun 记一行 task_runs。每条任务都记，行很小；保留 90 天由存储页的清理规则管（第 6 步），这里不删
func (s *Store) AddTaskRun(r TaskRun) error {
	if r.Module == "" || r.Outcome == "" {
		return errTaskRun("module 和 outcome 不能空")
	}
	if len(r.Detail) > MaxTaskDetail {
		return errTaskRun("detail 超过 500 字节，调用方要先过 taskrun.Detail")
	}
	if r.Attempts < 1 {
		r.Attempts = 1
	}
	_, err := s.Trace.Exec(`INSERT INTO task_runs (at, module, outcome, class, ms, attempts, root_id, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At, r.Module, r.Outcome, r.Class, r.Ms, r.Attempts, nullable(r.RootID), nullable(r.Detail))
	return err
}

type errTaskRun string

func (e errTaskRun) Error() string { return "task_runs：" + string(e) }

// TaskSink 把 *Store 接成 taskrun.Sink：装配处 &store.TaskSink{S: st} 传给 taskrun.Degrader，插件不碰 store
type TaskSink struct{ S *Store }

// AddTaskRun 实现 taskrun.Sink。Detail 在这里再过一遍 taskrun.Detail：调用方漏了脱敏也进不了库
func (t *TaskSink) AddTaskRun(r taskrun.Row) error {
	row := TaskRun{At: r.At.UnixMilli(), Module: r.Module, Outcome: r.Outcome, Class: r.Class, Attempts: r.Attempts, RootID: r.RootID, Detail: taskrun.Detail(r.Class, r.Detail)}
	if r.Ms >= 0 {
		ms := r.Ms
		row.Ms = &ms
	}
	return t.S.AddTaskRun(row)
}

var _ taskrun.Sink = (*TaskSink)(nil)
