package api

import "context"

// GapRef 只定位已登记闭区间；接收和执行时都要重读，不能据此扩大恢复范围。
type GapRef struct {
	ConversationKey ConversationKey
	ID              int64
	FromMS, ToMS    int64
}

type GapQuery struct {
	ConversationKey ConversationKey
	Character       string
	FromMS, ToMS    int64
	Cursor          Cursor
	Limit           int
}

type GapView struct {
	GapRef
	Reason, State                                 string
	Tries                                         int
	LastTryMS, LastManualMS, CreatedMS, UpdatedMS int64
	AcknowledgedMS                                int64
	OriginalCount                                 int
	JobID                                         string
	CanRetry                                      bool
	RetryAfterMS                                  int64
	ReasonCode                                    string
}

type BackfillJob struct {
	ID, Scope, Trigger, State                     string
	Gap                                           *GapRef
	StartedMS, EndedMS                            int64
	CurrentConversation                           ConversationKey
	Pages, Inserted, Duplicates, Checked, Pending int
	Messages                                      string
	CompleteCoverage                              bool
	Media                                         AlignmentMedia
	ReasonCode                                    string
}

// AlignmentMedia 的范围是连接补扫近七天加本轮新增段；未检查不能冒充零失败。
type AlignmentMedia struct {
	Scope                                                                            string
	AsOfMS                                                                           int64
	State                                                                            string
	Queued, Running, Downloaded, Expired, Failed, AwaitingManual, Skipped, Unchecked int
}

type AlignmentView struct {
	Scope                                       string
	AsOfMS                                      int64
	LastAttempt, CurrentJob                     *BackfillJob
	RemainingOpen, RemainingAbandoned, Excluded int
	Media                                       AlignmentMedia
	CanRun                                      bool
	ReasonCode                                  string
	Discovery                                   string
}

type GapReader interface {
	ReadGaps(context.Context, GapQuery) (Result[List[GapView]], error)
	ReadAlignmentGaps(context.Context, Cursor, int) (Result[List[GapView]], error)
	WatchGaps(context.Context, GapQuery) (Subscription[List[GapView]], error)
	WatchAlignment(context.Context) (Subscription[AlignmentView], error)
}

type BackfillActions interface {
	RequestGapBackfill(context.Context, GapRef) (BackfillJob, error)
	RequestAlignment(context.Context) (BackfillJob, error)
	WatchBackfill(context.Context, string) (Subscription[BackfillJob], error)
}

// GapAcknowledgements 只控制提醒；人工确认不提供历史完整性证明。
type GapAcknowledgements interface {
	SetGapAcknowledged(context.Context, GapRef, bool) (GapView, error)
}

// BackfillError 的代码固定；外部 I/O 详情由宿主统一脱敏，不带入任务快照。
type BackfillError struct{ Code string }

func (e *BackfillError) Error() string  { return "补录：" + e.Code }
func BackfillFailure(code string) error { return &BackfillError{Code: code} }
