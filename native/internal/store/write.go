package store

// 写库，照 daemon/plugins/store.ts 的 appendEvent、stepStart、attachSteps、addRun（第 1 步用到的几个）。
// 只在 Go 自己的库上用（OpenFresh 打开的）；Bun 后台管着的真实库照旧只读（OpenReadOnly），切过去是第 6 步的事

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"sumika-native/internal/kernel"
)

var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// NewID 是一个 ULID（和 Bun 版的 util/ulid.ts 一样的格式：26 个字，时间在前）
func NewID(at time.Time) string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	if testFastSync.Load() {
		return testPerMsID(at)
	}
	return ulid.MustNew(ulid.Timestamp(at), ulidEntropy).String()
}

// testPerMsID 只在测试程序里用：每个毫秒各用一份单调的随机数。测试时钟常常固定在同一个毫秒，
// 几条测试并行时夹着别的时间戳的号，全局那份单调随机数会被打断重新抽，同一条测试里先后拿到的两个号可能倒序，
// 依赖“先建的号排前面”的断言就偶发红。正式程序不走这里。
var testPerMs = map[uint64]io.Reader{}

func testPerMsID(at time.Time) string {
	ms := ulid.Timestamp(at)
	e, ok := testPerMs[ms]
	if !ok {
		if len(testPerMs) > 4096 {
			testPerMs = map[uint64]io.Reader{}
		}
		e = ulid.Monotonic(rand.Reader, 0)
		testPerMs[ms] = e
	}
	return ulid.MustNew(ms, e).String()
}

// JSString 和 JS 的 JSON.stringify 一样地写 JSON：不转义 < > &，U+2028 / U+2029 原样（Go 的 encoding/json 这几样都会转义）
func JSString(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return unescapeLineSeps(strings.TrimSuffix(b.String(), "\n")), nil
}

// 把 Go 写成 \uXXXX、JS 不这么写的几个换回 JS 的写法：\u2028、\u2029 写原字，\u0008、\u000c 写成 \b、\f；
// 前面是转义过的反斜杠（\\u2028，原文就是这六个字）的不动
func unescapeLineSeps(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	jsForm := map[string]string{`\u2028`: "\u2028", `\u2029`: "\u2029", `\u0008`: `\b`, `\u000c`: `\f`}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if i+6 <= len(s) {
				if r, ok := jsForm[s[i:i+6]]; ok {
					b.WriteString(r)
					i += 5
					continue
				}
			}
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// NewEvent 是要写进 events 的一行（appendEvent 的参数）
type NewEvent struct {
	Channel, Conversation, ActorKind, ActorID, Origin, Kind, Text string
	Segments                                                      any // nil 写 NULL；别的 JSON.stringify 以后写
	ReplyTo                                                       *string
	CreatedAt                                                     int64
	// Tags 只有 AppendInputEvent 读：写进 segments.tags（和渠道事件的标签同一处，记忆读的就是它）。
	// 别的写法照旧用 Segments 自己带 tags
	Tags []string
}

// insertEvent 在事务里写 events 一行，返回 seq 和 id（AppendEvent、AppendInbound 共用，两处的列不会写岔）
func insertEvent(tx *sql.Tx, e NewEvent) (seq int64, id string, err error) {
	var seg sql.NullString
	if e.Segments != nil {
		j, err := JSString(e.Segments)
		if err != nil {
			return 0, "", err
		}
		seg = sql.NullString{String: j, Valid: true}
	}
	id = NewID(time.UnixMilli(e.CreatedAt))
	res, err := tx.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, origin, kind, text, segments_json, reply_to, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, e.Channel, e.Conversation, e.ActorKind, e.ActorID, e.Origin, e.Kind, e.Text, seg, e.ReplyTo, e.CreatedAt)
	if err != nil {
		return 0, "", err
	}
	seq, err = res.LastInsertId()
	return seq, id, err
}

// observe 记下这些角色看到了这条事件（已经记过的忽略），返回新记了几条
func observe(tx *sql.Tx, seq int64, characters []string) (added int, err error) {
	for _, c := range characters {
		res, err := tx.Exec(`INSERT OR IGNORE INTO observations (character_id, event_seq) VALUES (?, ?)`, c, seq)
		if err != nil {
			return added, err
		}
		if n, err := res.RowsAffected(); err == nil {
			added += int(n)
		}
	}
	return added, nil
}

// AppendEvent 记一条事件，并记下哪些角色看到了它（observations），在一个事务里；返回 seq
func (s *Store) AppendEvent(e NewEvent, observers []string) (int64, error) {
	tx, err := s.Events.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, _, err := insertEvent(tx, e)
	if err != nil {
		return 0, err
	}
	if _, err := observe(tx, seq, observers); err != nil {
		return 0, err
	}
	return seq, s.commitChanged(tx, e.Conversation)
}

// Step 是一轮里的一步（第 1 步只有思考）
type Step struct {
	ID, CharacterID, Conversation, TurnID, Kind, Status string
	Name                                                string  // 工具名（思考那一步是空，写 NULL）
	Args                                                *string // 参数的 JSON（store.ts：clip(JSON.stringify(args), 4KB)；nil 写 NULL）
	Preview                                             string
	StartedAt                                           int64
	Identity                                            kernel.Invocation
	Ordinal                                             *int
}

// StepStart 记一步开始（stepStart）。批次是这一轮里第几次模型回复调的工具（规格-右侧栏她在想什么：第 N 次请求之后的那批工具）：
// 带 Ordinal 的（同一次回复里的第几个调用）第 0 个开新批、后面的跟着这一轮最大的批号；不看写入那一刻还有没有工具在跑——
// 并发执行时快的工具先结束，按时机算会把同一次回复拆成两批（CI 上 TestSameAsBun/tool-more 时红时绿）。
// 不带 Ordinal 的（回合外补记的单个工具）照旧：还有工具在跑就同批，否则开新批。思考跟着这一轮最大的工具批号，没有工具就是 0。
// 只算真开始过的行（started_at 非空）、不算自己：审批身份的占位行（RecordStepIdentity）batch 是默认的 0，不能当成上一批。
// 冲突时（审批占位先到了）照 Bun 版：除了 started_at 留先到的、status 是 waiting 时留着，别的都换成这一次的。
func (s *Store) StepStart(st Step) error {
	var last, open sql.NullInt64
	err := s.Trace.QueryRow(`SELECT (SELECT MAX(batch) FROM steps WHERE turn_id = ?1 AND kind = 'tool' AND started_at IS NOT NULL AND id != ?2),
		(SELECT COUNT(*) FROM steps WHERE turn_id = ?1 AND kind = 'tool' AND status IN ('running', 'waiting'))`, st.TurnID, st.ID).Scan(&last, &open)
	if err != nil {
		return err
	}
	b := last.Int64
	switch {
	case st.Kind != "tool" || !last.Valid:
	case st.Ordinal != nil:
		if *st.Ordinal == 0 {
			b++
		}
	case open.Int64 == 0:
		b++
	}
	status := st.Status
	if status == "" {
		status = "running"
	}
	var identity, provider, step any
	if st.Identity.ToolID != "" {
		in := st.Identity
		if in.ToolID != st.ID || in.RunID != st.TurnID || in.Scope.CharacterID != st.CharacterID || in.Scope.Conversation != st.Conversation {
			return ErrInputConflict
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		identity, provider, step = string(raw), in.ProviderCallID, in.StepID
	}
	r, err := s.Trace.Exec(`INSERT INTO steps (id, character_id, conversation, turn_id, kind, name, args_json, preview, status, started_at, batch, identity_json, provider_call_id, step_id, ordinal)
		VALUES (?1, ?2, ?3, ?4, ?5, ?10, ?11, ?6, ?7, ?8, ?9, ?12, ?13, ?14, ?15)
		ON CONFLICT(id) DO UPDATE SET character_id = ?2, conversation = ?3, turn_id = ?4, kind = ?5,
			name = ?10, args_json = ?11, started_at = COALESCE(steps.started_at, ?8), batch = ?9,
			identity_json = COALESCE(?12, steps.identity_json), provider_call_id = COALESCE(?13, steps.provider_call_id),
			step_id = COALESCE(?14, steps.step_id), ordinal = COALESCE(?15, steps.ordinal),
			status = CASE WHEN steps.status = 'waiting' THEN steps.status ELSE ?7 END
		WHERE steps.identity_json IS NULL OR ?12 IS NULL OR steps.identity_json = ?12`,
		st.ID, st.CharacterID, st.Conversation, st.TurnID, st.Kind, nullable(st.Preview), status, st.StartedAt, b, nullable(st.Name), st.Args, identity, provider, step, st.Ordinal)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrInputConflict
	}
	return err
}

// StepApproval 照 stepApproval：审批的情况记进这一步（可能比工具开始先到，先占一行）；status 为空时不改原来的状态
func (s *Store) StepApproval(toolCallID, characterID, status, approvalJSON string) error {
	ins := status
	if ins == "" {
		ins = "waiting"
	}
	_, err := s.Trace.Exec(`INSERT INTO steps (id, character_id, kind, status, approval_json) VALUES (?1, ?2, 'tool', ?3, ?4)
		ON CONFLICT(id) DO UPDATE SET approval_json = ?4, status = COALESCE(?5, steps.status)`, toolCallID, characterID, ins, approvalJSON, nullable(status))
	return err
}

// 工具输出的预览留多少、超过多少另存全文（store.ts STEP_PREVIEW_MAX）
const stepPreviewMax = 16 * 1024

// 界面一屏能看的 diff 远小于 64 KB；完整输出另存 blob 留给规格 7.2 T10。
const stepDetailsMax = 64 * 1024

func stepDetailsJSON(details any) (string, error) {
	text, err := JSString(details)
	if err != nil || len(text) <= stepDetailsMax {
		return text, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return "", err
	}
	var diff, patch string
	if err := json.Unmarshal(fields["diff"], &diff); err != nil {
		return "", err
	}
	if err := json.Unmarshal(fields["patch"], &patch); err != nil {
		return "", err
	}
	fields["diff"], fields["patch"] = json.RawMessage(`""`), json.RawMessage(`""`)
	fields["truncated"] = json.RawMessage(`true`)
	base, err := JSString(fields)
	if err != nil {
		return "", err
	}
	remaining := stepDetailsMax - len(base)
	if remaining < 0 {
		return "", fmt.Errorf("工具详情的其他字段超过 %d 字节", stepDetailsMax)
	}
	budget := remaining / 2
	for key, value := range map[string]string{"diff": diff, "patch": patch} {
		// 按 JSON 转义后的字节计数，不能切断 UTF-8，也不能漏算换行、引号和控制字符。
		used, end := 0, 0
		for i, r := range value {
			n := utf8.RuneLen(r)
			switch r {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				n = 2
			default:
				if r < 0x20 {
					n = 6
				}
			}
			if used+n > budget {
				break
			}
			used += n
			_, size := utf8.DecodeRuneInString(value[i:])
			end = i + size
		}
		encoded, err := JSString(value[:end])
		if err != nil {
			return "", err
		}
		fields[key] = json.RawMessage(encoded)
	}
	return JSString(fields)
}

// StepEnd 记一步结束（stepEnd）：输出留开头结尾共 16 KB，details JSON 最多 64 KB；状态已经是拒绝 / 超时 / 取消的不改。
// 完整输出由托管结果服务保存；finalizeTool 再关联 full_hash/result_ref，预览不是全文。
func (s *Store) StepEnd(id, output string, isError bool, now int64, details any) error {
	var detailsJSON *string
	if details != nil {
		text, err := stepDetailsJSON(details)
		if err != nil {
			return fmt.Errorf("工具详情保存失败：%w", err)
		}
		detailsJSON = &text
	}
	status := "done"
	errInt := 0
	if isError {
		status, errInt = "error", 1
	}
	var preview *string
	if output != "" {
		p := Clip(output, stepPreviewMax)
		preview = &p
	}
	_, err := s.Trace.Exec(`UPDATE steps SET preview = ?, full_hash = NULL, is_error = ?, ended_at = ?, details_json = ?,
		status = CASE WHEN status IN ('denied', 'expired', 'cancelled') THEN status ELSE ? END WHERE id = ?`, preview, errInt, now, detailsJSON, status, id)
	return err
}

// AttachSteps：回复落盘以后，这一轮还没挂上的步骤都挂到这条回复上
func (s *Store) AttachSteps(turnID string, eventSeq int64) error {
	_, err := s.Trace.Exec(`UPDATE steps SET event_seq = ? WHERE turn_id = ? AND event_seq IS NULL`, eventSeq, turnID)
	return err
}

// Epoch 是一次压缩（context_epochs 的一行，agent.ts runCompact 写的那些）：成功的是一份快照，以后重建实例时
// 快照放最前面、从 FromSeq 这条起带原文；失败的也记（Error 写为什么），不当快照用
type Epoch struct {
	ID, Conversation, CharacterID string
	FromSeq                       int64  // 快照之后从哪条原文接着带（切点那条）
	Summary                       string // 摘要（失败的是空）
	Kind, Trigger                 string // kind：chat（话题以后是 topic）；trigger：rule / early / manual / memory
	TokensBefore, TokensAfter     int    // 压之前、压完以后的估算
	Dropped, Kept                 int    // 压掉了几条、留着几条原文
	Model, Status                 string // status：ok / failed
	Error                         *string
	Ms, CreatedAt                 int64
}

// AddEpoch 记一次压缩
func (s *Store) AddEpoch(e Epoch) error {
	_, err := s.Events.Exec(`INSERT INTO context_epochs (id, conversation, character_id, from_seq, summary, kind, trigger, tokens_before, tokens_after,
		dropped, kept, model, status, error, ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Conversation, e.CharacterID, e.FromSeq, e.Summary, e.Kind, e.Trigger, e.TokensBefore, e.TokensAfter,
		e.Dropped, e.Kept, e.Model, e.Status, e.Error, e.Ms, e.CreatedAt)
	return err
}

// RunSection 是 model_runs.sections 里的一段：全文按哈希另存进 prompt_sections，这里只记字数和哈希
type RunSection struct {
	ID, Source, Text string
}

// JSLen 是 JS 字符串的 length（UTF-16 码元数）：sections 的 chars 照它算
func JSLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 && utf8.ValidRune(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// JSSlice 像 JS 的 s.slice(0, n)（按 UTF-16 码元）。切在一对代理项中间时 JS 留下半个，Go 的字符串装不下半个，
// 这里整个字不要（规格第七节 A23）
func JSSlice(s string, n int) string {
	used := 0
	for i, r := range s {
		u := 1
		if r >= 0x10000 && utf8.ValidRune(r) {
			u = 2
		}
		if used+u > n {
			return s[:i]
		}
		used += u
	}
	return s
}

// Run 是一次模型调用（addRun 的参数，第 1 步用到的几项）
type Run struct {
	ID, CharacterID, Conversation, Stage, Model, TurnID string
	StartedAt                                           int64
	TTFTMs                                              *int64
	TotalMs                                             int64
	Input, Output, CacheRead                            int64
	CacheWrite, Reasoning                               *int64
	RouteProvider, RouteModel                           *string
	Sections                                            []RunSection
	Injected                                            []any
	EpochID, Rebuild, Error                             *string
	ErrorClass                                          string // 失败原因类别（llm.Class*）；Error 为空时必须空；Error 不空而这里空 = 调用方没分类，读的时候现算
	Context                                             string // context_json 的原样 JSON；空写 NULL
	Identity                                            kernel.Invocation
	UsageSeen                                           *bool
	Incomplete                                          bool
	CostMicros                                          *int64
	ResetReason                                         *string
	Now                                                 int64  // 记录的这一刻（prompt_sections.first_seen）
	Prompt                                              string // 发出去的 messages、tools 的指纹（llm.PromptHashes，不含正文）；空写 NULL，upsert 时不覆盖已有的
}

// AddRun 记一次模型调用（addRun）。完整的请求和回复默认不存。
// 临时：出错、清过句尾时要留的回复（response_json）先写 NULL，等 Go 有自己的回复对象再定存什么（规格第七节 T4、待办"后台改成 Go"）
func (s *Store) AddRun(r Run) error {
	type secMeta struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Chars  int    `json:"chars"`
		Hash   string `json:"hash"`
	}
	metas := make([]secMeta, 0, len(r.Sections))
	for _, sec := range r.Sections {
		h := sha256.Sum256([]byte(sec.Text))
		hash := hex.EncodeToString(h[:])
		if _, err := s.Trace.Exec(`INSERT OR IGNORE INTO prompt_sections (hash, text, first_seen) VALUES (?, ?, ?)`, hash, sec.Text, r.Now); err != nil {
			return err
		}
		metas = append(metas, secMeta{sec.ID, sec.Source, JSLen(sec.Text), hash})
	}
	sections, err := JSString(metas)
	if err != nil {
		return err
	}
	injected := r.Injected
	if injected == nil {
		injected = []any{}
	}
	inj, err := JSString(injected)
	if err != nil {
		return err
	}
	_, err = s.Trace.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, ttft_ms, total_ms,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, turn_id, route_provider, route_model,
 sections_json, injected_json, draft_json, context_json, epoch_id, rebuild, request_json, response_json, error,
 root_id, run_id, step_id, attempt_id, purpose, usage_seen, incomplete, cost_micros, reset_reason, error_class, prompt_hashes_json)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(attempt_id) WHERE attempt_id IS NOT NULL DO UPDATE SET
 stage=excluded.stage, turn_id=excluded.turn_id, ttft_ms=excluded.ttft_ms, total_ms=excluded.total_ms,
 sections_json=excluded.sections_json, injected_json=excluded.injected_json,
 context_json=CASE WHEN model_runs.context_json IS NULL THEN excluded.context_json
 WHEN excluded.context_json IS NULL THEN model_runs.context_json
 WHEN json_valid(model_runs.context_json) AND json_valid(excluded.context_json)
 THEN json_patch(model_runs.context_json, excluded.context_json) ELSE excluded.context_json END,
 epoch_id=excluded.epoch_id, rebuild=excluded.rebuild, reset_reason=excluded.reset_reason, error=excluded.error, error_class=excluded.error_class,
 prompt_hashes_json=COALESCE(excluded.prompt_hashes_json, model_runs.prompt_hashes_json)`,
		r.ID, r.CharacterID, r.Conversation, r.Stage, r.Model, r.StartedAt, r.TTFTMs, r.TotalMs,
		r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Reasoning, nullable(r.TurnID), r.RouteProvider, r.RouteModel,
		sections, inj, nullable(r.Context), r.EpochID, r.Rebuild, r.Error,
		nullable(r.Identity.RootID), nullable(r.Identity.RunID), nullable(r.Identity.StepID), nullable(r.Identity.AttemptID), nullable(r.Identity.Kind), r.UsageSeen, r.Incomplete, r.CostMicros, r.ResetReason, nullable(r.ErrorClass), nullable(r.Prompt))
	return err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Clip 照 daemon/util/clip.ts：按 UTF-8 字节截，留开头 60%、结尾 40%，中间写省略了多少字节，不切开一个字
func Clip(text string, maxBytes int) string {
	b := []byte(text)
	if len(b) <= maxBytes {
		return text
	}
	markerLen := len(fmt.Sprintf("\n……[省略 %d 字节]……\n", len(b)))
	room := maxBytes - markerLen
	head := func(n int) string {
		i := min(n, len(b))
		for i > 0 && i < len(b) && b[i]&0xc0 == 0x80 {
			i--
		}
		return string(b[:i])
	}
	tail := func(n int) string {
		i := max(0, len(b)-n)
		for i < len(b) && b[i]&0xc0 == 0x80 {
			i++
		}
		return string(b[i:])
	}
	if room <= 0 {
		return head(max(0, maxBytes))
	}
	h, t := head(int(math.Ceil(float64(room)*0.6))), tail(int(math.Floor(float64(room)*0.4)))
	return fmt.Sprintf("%s\n……[省略 %d 字节]……\n%s", h, len(b)-len(h)-len(t), t)
}
