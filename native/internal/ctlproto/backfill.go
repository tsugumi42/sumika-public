package ctlproto

const CmdBackfill = "backfill"
const DefaultGapCount = 100
const MaxGapCount = 200

type BackfillMedia struct {
	Scope          string `json:"scope"`
	AsOfMS         int64  `json:"asOfMS"`
	State          string `json:"state"`
	Queued         int    `json:"queued"`
	Running        int    `json:"running"`
	Downloaded     int    `json:"downloaded"`
	Expired        int    `json:"expired"`
	Failed         int    `json:"failed"`
	AwaitingManual int    `json:"awaitingManual"`
	Skipped        int    `json:"skipped"`
	Unchecked      int    `json:"unchecked"`
}
type BackfillJob struct {
	ID                 string        `json:"jobID"`
	Scope              string        `json:"scope"`
	Trigger            string        `json:"trigger"`
	State              string        `json:"state"`
	StartedMS          int64         `json:"startedMS"`
	EndedMS            int64         `json:"endedMS"`
	MaskedConversation string        `json:"maskedConversation,omitempty"`
	Pages              int           `json:"pages"`
	Inserted           int           `json:"inserted"`
	Duplicates         int           `json:"duplicates"`
	Checked            int           `json:"checked"`
	Pending            int           `json:"pending"`
	Messages           string        `json:"messages"`
	CompleteCoverage   bool          `json:"completeCoverage"`
	Media              BackfillMedia `json:"media"`
	ReasonCode         string        `json:"reasonCode,omitempty"`
}
type BackfillStatus struct {
	Scope              string        `json:"scope"`
	AsOfMS             int64         `json:"asOfMS"`
	LastAttempt        *BackfillJob  `json:"lastAttempt"`
	CurrentJob         *BackfillJob  `json:"currentJob"`
	RemainingOpen      int           `json:"remainingOpen"`
	RemainingAbandoned int           `json:"remainingAbandoned"`
	Excluded           int           `json:"excluded"`
	Media              BackfillMedia `json:"media"`
	CanRun             bool          `json:"canRun"`
	ReasonCode         string        `json:"reasonCode,omitempty"`
	Discovery          string        `json:"discovery"`
}
type BackfillGap struct {
	GapHandle          string `json:"gapHandle"`
	MaskedConversation string `json:"maskedConversation"`
	FromMS             int64  `json:"fromMS"`
	ToMS               int64  `json:"toMS"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	CanRetry           bool   `json:"canRetry"`
	RetryAfterMS       int64  `json:"retryAfterMS"`
	JobID              string `json:"jobID,omitempty"`
}
type BackfillGaps struct {
	Items      []BackfillGap `json:"items"`
	NextCursor string        `json:"nextCursor"`
	HasMore    bool          `json:"hasMore"`
	AsOfMS     int64         `json:"asOfMS"`
	Scope      string        `json:"scope"`
}
type BackfillAccepted struct {
	Accepted bool   `json:"accepted"`
	JobID    string `json:"jobID"`
	Scope    string `json:"scope"`
	State    string `json:"state"`
}
type BackfillFailure struct {
	ReasonCode string `json:"reasonCode"`
}
