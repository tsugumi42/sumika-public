package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// 写入底座（规格-记忆-v2 13.2 的 W2）：角色库写锁、一个事务 apply、memory_jobs 幂等键、游标、同文查重、世代号、向量补齐。
// 纯 Go，不调模型：解析（W4）、整理和计时（W5）、三个工具（W3）都建在这上面。
// 日志只写计数、原因码和 ref，不写原文、模型输出，也不写会话 id（QQ 会话 id 里有号码，B16、边界 ㉑）。

// DefaultWriteWait：等写锁最多等多久（remember 要的是 5 秒，13.2.3）；整理可以在 Batch.Wait 里给更长，它已经花过钱了
const DefaultWriteWait = 5 * time.Second

var (
	ErrRememberExpired   = errors.New("同文记忆仍已过期，未恢复为现状")
	ErrRememberRevoked   = errors.New("共用记忆已撤销或拒绝")
	ErrBusy              = errors.New("记忆库正忙")
	ErrClosed            = errors.New("记忆库已经关了")
	ErrGenerationChanged = errors.New("世代号变了：这一批在忘掉或真删之前算的，作废")
	ErrCursorMoved       = errors.New("游标已经被别的批挪过")
	ErrReplaceGone       = errors.New("要替换的那条已经不是有效的了（被别处改过、撤销了，或编号不对），这次什么都没写")
	ErrReplaceProtected  = errors.New("受保护的记忆不能由模型替换")
	ErrReplaceAmbiguous  = errors.New("这个短码对上了不止一条有效的事实，这次什么都没写")
)

// 谁在写（Batch.By，也写进 fact_revisions.by）。整理（AI）只能取代、撤销 confirmed_by = 'ai' 的有效事实，其余一律受保护；
// 主人自己（remember）可以动任何有效事实（4.4 第 188 行；规格-记忆写入上线 W2）
const (
	ByRememberAI = "remember-ai" // 模型提炼，不代表主人确认
	ByRemember   = "remember"    // 主人在私聊里让她记
	BySummary    = "summary"     // 后台整理
)

// ConfirmedOwnerSaid：remember 写的事实的 confirmed_by（来源已绑定主人本人的原话，但文字是模型提炼的，不是主人放行）。
// 不是 owner：界面和规格里"你确认过"只留给主人亲手放行的；也不是 ai：整理不能把它当"她自己攒的"取代、撤销。
// 谁能动它见 protectedFrom：整理动不了（白名单本来就只认 ai），remember（ByRememberAI）和主人本人动得了
const ConfirmedOwnerSaid = "owner_said"

// 一批写入数过的东西（Applied.Counts、memory_jobs.counts_json 的键）。整理那一步自己数的（读了几条、丢了几条……）另用别的键，合并进同一张表
const (
	CountAddedActive    = "added_active"
	CountAddedCandidate = "added_candidate"
	CountAddedEpisode   = "added_episode"
	CountMerged         = "merged"          // 同文：合并进了已有的
	CountExists         = "exists"          // 同文：已经有了，没写（remember）
	CountDupRevoked     = "dup_revoked"     // 同文的被撤销或 AI 没通过：不新建、不复活
	CountEvidenceAdded  = "evidence_added"  // 新记了一次证据
	CountEvidenceRepeat = "evidence_repeat" // 同一天同一会话又说了一遍：还是一次
	CountEvidenceBySelf = "evidence_by_subject"
	CountEvidenceGone   = "evidence_gone" // 整理的 evidence 动作写入时，那条事实已经不是待确认的了（被放行、撤销、取代，或编号不存在）：没记
	// 转述的证据，说话的会话看不见这条待确认的事实：不记，不替它投票（同文合并按（主体，文本）全库查重、不看范围，别的会话里的人说同一句不该算数）
	CountEvidenceHidden    = "evidence_hidden"
	CountScopesWidened     = "scopes_widened" // 本人说的，范围并上了
	CountScopesKept        = "scopes_kept"    // 别人转述的，范围没动
	CountSuperseded        = "superseded"
	CountPromoted          = "promoted"
	CountOwnerSaidUpgraded = "owner_said_upgraded" // remember 遇到同文的 ai 有效事实、来源绑了主人本人的原话：confirmed_by 从 ai 升成 owner_said
	CountOwnerConfirmed    = "owner_confirmed"     // remember 遇到同文的有效事实、主人本人又说了一遍：confirmed_by 提成 owner（范围不动）
	CountSupersedeLost     = "supersede_lost"      // 要取代的那条在模型看到它以后已经不是有效的了（被别处取代、撤销）：没取代任何东西
	CountRevoked           = "revoked"
	CountRevokeNoop        = "revoke_noop" // 要撤销的不是有效的（已经撤过、被取代、编号不存在）：没动，也没记修订
	CountJobDuplicate      = "job_duplicate"

	// 取代保护（规格-记忆写入上线 W2）：整理只能动 confirmed_by = 'ai' 的有效事实
	CountSupersedeDeferred  = "supersede_deferred"   // 整理写的候选带 supersedes：写入时不取代任何有效事实，想取代谁记在 add 修订的 wants_supersede，提升时才定
	CountSupersedeProtected = "supersede_protected"  // 想取代的有效事实受保护（confirmed_by 不是 ai）：没取代；和"在模型看到它以后被别人改了"的 supersede_lost 分开
	CountRevokeWrongSubject = "revoke_wrong_subject" // remember 一类（ByRememberAI）想撤销的有效事实讲的不是主人或她自己：没动，也没记修订
	CountRevokeProtected    = "revoke_protected"     // 要撤销的有效事实受保护：没动，也没记修订；和"本来就不是有效的"的 revoke_noop 分开
	// 整理想取代的旧事实讲的是另一个人（主体和这条候选不一样）：不记想取代谁、已记下的提升时也不取代，候选本身照常走（复核后补）。
	// 取代是"同一个人的同一件事更新了"，不是一条新话把别人名下的事顶掉
	CountSupersedeSubjectMismatch = "supersede_subject_mismatch"
	// 候选提升时想取代的旧事实受保护：这条候选不提升（W3b）。记账模式下攒够了证据但想取代的旧事实受保护的，也数这个，不数 would_confirm。
	// 同一条候选每多一次证据会再数一次，数的是"次"不是"条"
	CountConfirmBlockedProtected = "confirm_blocked_protected"

	// 待确认攒够证据变有效（规格-记忆写入上线 W3b；confirmed_by = ai）
	CountConfirmed    = "confirmed"     // 提升成有效了
	CountWouldConfirm = "would_confirm" // 记账模式（Options.AutoConfirm 为假）：攒够了，没动手。一个事务里同一条只数一次；每批再来证据会再数，数的是"次"不是"条"
)

// Op 是写进角色库的一个动作：FactOp、RevokeOp、EpisodeOp、EvidenceOp、ConfirmOp。范围、归属（有效还是待确认）都由调用方按 4.2、13.2.6 算好，这里不替它判断
type Op interface{ isOp() }

// FactOp 写一条事实
type FactOp struct {
	Kind        string // fact / promise / preference / boundary
	Text        string
	Subject     string // owner / self / person:<id>；空不记
	Status      string // active / candidate
	ConfirmedBy string // 有效的另记谁放行的：owner / ai
	Scopes      []string
	Supersedes  string // 要取代的有效事实的编号（全长）
	// SupersedesCode 是 remember 的 6 位短码（编号的末 6 位，大写，不带 #），和 Supersedes 只给一个：
	// 在事务里、写锁下找成全长编号，恰好一条有效的才行（ErrReplaceGone：0 条；ErrReplaceAmbiguous：2 条以上）。
	// 查和换在同一个事务里，查到的那条不会在中间被别的会话换掉（13.2.3）
	SupersedesCode   string
	Temporal         string
	ValidTo          *int64  // 停止作为现状召回的时间，不表示已完成
	SourceSeqs       []int64 // 原话的消息编号
	Origin           string
	Conversation     string
	AudienceAtOrigin string
	AttributedTo     string
	EventAt          int64     // 毫秒
	Evidence         *Evidence // 这句话算一次证据；整理给，remember 不给
	// OwnerSaid：remember 查到的"这个会话里她看到的最近一句人说的话"是主人本人说的（Event 的 ActorKind 和 Origin 都是 owner，会话对得上）。
	// 只有 Remember 按 Event 的身份字段设；同文的有效事实被主人再说一遍要放行（提成 owner）时只认它，不认名字、不认模型给的任何字段。
	// 整理不设，设了也不起作用：reportDup 只在 ByRemember 的批里看它
	OwnerSaid bool
	Share     bool // 程序按可信原话和 only_her 分类算，不读模型自报的 Share。
	LearnedBy string
	// Whisper：这条事实是悄悄话处理那一轮里 remember 写的（宿主给的来源，不读模型自报）；只写进 facts.whisper_json，不影响任何放行判断
	Whisper *WhisperSource
}

// RevokeOp 撤销一条有效的事实（只动有效的，B14；整理还要它是 confirmed_by = 'ai' 的，其余受保护，主人自己的 remember 不受这条限制）。
// ID（全长）和 Code（memory_search 给的 6 位短码，大写不带 #）只给一个：短码在事务里按有效事实现找，找不到、不止一条都报错（和 remember 的 replaces 一样）。
// Reason 记进这条撤销的修订；ByRememberAI（memory_forget）还只动关于主人或她自己的事实
type RevokeOp struct{ ID, Code, Reason string }

// EpisodeOp 写一段经历（片段）
type EpisodeOp struct {
	Conversation     string
	Text             string
	FromSeq, ToSeq   int64
	Scopes           []string
	Origin           string
	AudienceAtOrigin string
	EventAt          int64
}

// EvidenceOp 给一条已有的待确认事实记一次证据（整理模型的 evidence 动作：这批对话里有人再次说到、或明确确认了这条听说的事，规格-记忆写入上线 W3a）。
// FactID 是全长编号；Evidence 里的会话、时间、说话人、BySubject 都是程序按那一行算好的，不来自模型。
// 只有整理（BySummary）能带它；事务里事实要还是待确认才记，别的状态（提示词给出之后被放行、撤销、取代了）不记、数 evidence_gone。
// 只记一行 fact_evidence：它自己不改状态、范围、修订；记完以后同一个事务里看这条候选攒够没有（confirmByEvidence，W3b）
type EvidenceOp struct {
	FactID   string
	Evidence Evidence
}

// ConfirmOp 检查一条待确认的事实攒够证据没有，够了就提升（confirmByEvidence，规格-记忆写入上线 W3b）。只有整理（BySummary）能带它。
// 证据记完以后的提升是自动检查的，不用它；它给启动扫描用：打开自动提升的那次启动，对已经攒够证据、却没等到下一次证据的候选补一次，一条一个事务
type ConfirmOp struct{ FactID string }

func (FactOp) isOp()     {}
func (RevokeOp) isOp()   {}
func (EpisodeOp) isOp()  {}
func (EvidenceOp) isOp() {}
func (ConfirmOp) isOp()  {}

// Evidence 是"这句话在某一天、某个会话里出现过"（4.4 第 1 条）。键是（事实，日期，会话）：同一天同一会话说几遍只算一次
type Evidence struct {
	Conversation string
	At           int64 // 说这句话的时间（毫秒），定"哪一天"；0 用现在
	Seq          int64 // 原话的消息编号，0 不记
	Speaker      string
	// BySubject：说这句话的就是这条事实讲的那个人（主人的事 = 主人说的；某人的事 = 他本人说的）。
	// 同文合并时只有本人说的才把范围并上去，别人转述的不扩范围（4.2 第 167 行的组语义只在本人说时成立，13.2.4 第 8 步）；
	// 也是 4.4 "关于某人自己的事，3 次里至少 1 次要是本人说的"的依据
	BySubject bool
}

// DupMode：撞上同文（去掉空白标点后一样、主体一样）的有效或待确认事实时怎么办
type DupMode int

const (
	DupMerge  DupMode = iota // 整理：合并进已有的那条，记一次证据
	DupReport                // remember：什么都不写，告诉调用方"已经记着了"；撤销过的同文不挡（主人明说要记就是放行）
)

// Batch 是一个事务里要写的全部
type Batch struct {
	By    string // ByRemember / BySummary
	Ops   []Op   // 按顺序执行
	OnDup DupMode
	Job   *Job // 整理才有：幂等键、计数、游标
	// Generation 不是 nil 时，事务里比对世代号，不一样就整批不写（忘掉、真删期间算出来的结果作废）
	Generation *int64
	Wait       time.Duration // 等写锁最多等多久，0 用 DefaultWriteWait
	// Audience 保留证据来源关联：跨场合能想起，不代表别人的转述能替这条候选记账。
	// 没给只认 public 和证据所在会话，缺上下文只能少记。
	Audience *Audience
	// 共用池发布和补提升还服从来源角色当前的自动提升状态；不写进持久交接记录。
	confirmFrom *Memory
}

// Job 是一批整理：(会话, 起 seq, 止 seq, 版本) 是幂等键（B2），游标挪到 To
type Job struct {
	Conversation string
	From, To     int64 // 这一批读到的第一条和最后一条（被丢掉的行也算）
	Version      int
	// Prev 是读这一批时库里的游标，nil 表示当时没有这一行。写的时候要还是这个值才写（compare-and-set），
	// 旧的批次迟到也不会把游标往回拉，两个批次重叠了也只有一个生效
	Prev   *int64
	Counts map[string]int // 调用方已经数好的，和写入数的一起存进 memory_jobs.counts_json
}

// Result 是一个动作的结局
type Result string

const (
	ResultAdded   Result = "added"
	ResultMerged  Result = "merged"          // 同文：合并进了已有的
	ResultExists  Result = "exists"          // 同文：已经有了，没写
	ResultRefused Result = "refused_revoked" // 同文的被撤销或 AI 没通过：没新建，也没复活
	ResultRevoked Result = "revoked"
	ResultNoop    Result = "noop"
	// ResultEvidence：EvidenceOp 记下了一次证据（同一天同一会话又说了一遍也算，只是不新增行）
	ResultEvidence Result = "evidence"
	// ResultConfirmed：ConfirmOp 把一条攒够证据的待确认事实提升成了有效
	ResultConfirmed Result = "confirmed"
)

// Outcome 是第 i 个动作的结局
type Outcome struct {
	Result   Result
	Ref      string // 新写的，或同文撞上的那一条（f:… / e:…）
	Status   string // Ref 这条现在的状态
	Replaced *Replaced
	Promoted bool // 同文的待确认事实被主人明说放行，提成了有效（remember）
	// Confirmed：这条待确认的事实这一次攒够了证据，被提升成有效（confirmed_by = ai，W3b）。Status 已经是 active
	Confirmed bool
}

// Replaced 是被取代的那一条（remember 的成功文字里要写它原来的正文）
type Replaced struct{ ID, Text string }

// Applied 是一批写完的结果
type Applied struct {
	Duplicate bool // 幂等键已经有了：什么都没写，只把游标挪过去（B2）
	Outcomes  []Outcome
	Counts    map[string]int
	Added     []string      // 新写的条目编号（向量已经排进后台补齐）
	Waited    time.Duration // 等写锁等了多久
}

// writer 是 Memory 的写入那一侧，只在 Options.Writable 为真时有
type writer struct {
	m  *Memory
	db *sql.DB

	// sem 是角色库的写锁（容量 1 的通道，因为要能限时等）：remember 和整理都拿它；模型调用不持。closed 只在持锁时读写
	sem    chan struct{}
	closed bool
	// needReload：上一次写好了、内存里的条目没刷新成；下一次拿到锁先补
	needReload bool
	// staleOnce：整理提示词没有候选块的提醒只说一次（summary.go 的 warnStale）
	staleOnce sync.Once
	// placeSeen：场合没放行的提醒每个会话只说一次（write_places.go 的 skipPlace）；键是会话 id，只在内存里，不进日志
	placeSeen sync.Map

	now   func() time.Time
	loc   *time.Location // 证据的"哪一天"按哪个时区算
	newID func() string
	logf  func(format string, args ...any)
}

func newWriter(m *Memory, db *sql.DB) *writer {
	w := &writer{m: m, db: db, sem: make(chan struct{}, 1), now: m.now, loc: time.Local,
		newID: func() string { return ulid.Make().String() }, logf: m.opt.Logf}
	if w.logf == nil {
		w.logf = log.Printf
	}
	return w
}

// writer 返回写入那一侧；Writable 为假时是 ErrNotWritable
func (m *Memory) writer() (*writer, error) {
	if m.w == nil {
		return nil, ErrNotWritable
	}
	return m.w, nil
}

// background 把一次调用的模型和生成器一起冻结；离线工具仍可给固定端口。
func (m *Memory) background() (Generator, string, error) {
	gen, model := m.opt.Gen, ""
	if m.opt.Background != nil {
		model, gen = m.opt.Background()
	} else if m.opt.Model != nil {
		model = strings.TrimSpace(m.opt.Model())
	}
	if gen == nil {
		return nil, model, ErrNoGenerator
	}
	return gen, model, nil
}

func (m *Memory) events() (EventSource, error) {
	if m.opt.Events == nil {
		return nil, ErrNoEvents
	}
	return m.opt.Events, nil
}

// lock 拿写锁，最多等 wait；拿到后库已经关了就还回去，返回 ErrClosed
func (w *writer) lock(ctx context.Context, wait time.Duration) (unlock func(), err error) {
	if wait <= 0 {
		wait = DefaultWriteWait
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case w.sem <- struct{}{}:
	case <-t.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if w.closed {
		<-w.sem
		return nil, ErrClosed
	}
	return func() { <-w.sem }, nil
}

// close：停掉写入那一侧，顺序是 13.2.5 的：取消后台协程 → 等它们退出 → 等在途的写完（拿到锁）→ 关库。可以重复调
func (w *writer) close() error {
	if !w.m.stopVectors() {
		return nil
	}
	w.sem <- struct{}{}
	w.closed = true
	err := w.db.Close()
	<-w.sem
	return err
}

// ───────────── 写 ─────────────

var factKinds = []string{"fact", "promise", "preference", "boundary"}

func validate(b Batch) error {
	if b.By == "" {
		return errors.New("Batch.By 没写")
	}
	for i, op := range b.Ops {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("第 %d 个动作不合规："+format, append([]any{i + 1}, args...)...)
		}
		switch op := op.(type) {
		case FactOp:
			switch {
			case !slices.Contains(factKinds, op.Kind):
				return fail("kind %q 不认", op.Kind)
			case op.Status != "active" && op.Status != "candidate":
				return fail("status %q 不认（只有 active、candidate）", op.Status)
			case op.ConfirmedBy != "" && op.ConfirmedBy != "ai" && op.ConfirmedBy != "owner" && op.ConfirmedBy != ConfirmedOwnerSaid:
				return fail("confirmed_by %q 不认", op.ConfirmedBy)
			case b.By == ByRememberAI && op.ConfirmedBy != ConfirmedOwnerSaid:
				return fail("模型记忆不得自报主人确认，只能标 %s", ConfirmedOwnerSaid)
			case b.By != ByRememberAI && op.ConfirmedBy == ConfirmedOwnerSaid:
				return fail("%s 只有绑定了主人原话的 remember 能写", ConfirmedOwnerSaid)
			case strings.TrimSpace(op.Text) == "":
				return fail("正文是空的")
			case op.Supersedes != "" && op.SupersedesCode != "":
				return fail("要取代的编号和短码只能给一个")
			}
			if err := validScopes(op.Scopes); err != nil {
				return fail("%v", err)
			}
			if ev := op.Evidence; ev != nil && ev.Conversation == "" {
				return fail("证据没写会话")
			}
		case RevokeOp:
			switch {
			case op.ID == "" && op.Code == "":
				return fail("撤销没写编号")
			case op.ID != "" && op.Code != "":
				return fail("要撤销的编号和短码只能给一个")
			}
		case EvidenceOp:
			switch {
			case b.By != BySummary:
				return fail("证据动作只有整理能给")
			case op.FactID == "":
				return fail("证据没写事实编号")
			case op.Evidence.Conversation == "":
				return fail("证据没写会话")
			}
		case ConfirmOp:
			switch {
			case b.By != BySummary:
				return fail("提升动作只有整理能给")
			case op.FactID == "":
				return fail("提升没写事实编号")
			}
		case EpisodeOp:
			if strings.TrimSpace(op.Text) == "" || op.Conversation == "" {
				return fail("片段的正文或会话是空的")
			}
			if err := validScopes(op.Scopes); err != nil {
				return fail("%v", err)
			}
		default:
			return fail("不认识的动作 %T", op)
		}
	}
	if j := b.Job; j != nil {
		switch {
		case j.Conversation == "":
			return errors.New("Job 没写会话")
		case j.From > j.To:
			return fmt.Errorf("Job 的起 %d 大于止 %d", j.From, j.To)
		case j.Prev != nil && *j.Prev >= j.To:
			return fmt.Errorf("Job 读批时的游标 %d 不在止 %d 之前", *j.Prev, j.To)
		}
	}
	return nil
}

func validScopes(scopes []string) error {
	if len(scopes) == 0 {
		return errors.New("没有范围")
	}
	for _, s := range scopes {
		if strings.TrimSpace(s) == "" {
			return errors.New("范围里有空的")
		}
	}
	return nil
}

// apply 把一批动作写进角色库：一个事务，要么全写要么全不写（B1）。
// 先拿写锁（最多等 Batch.Wait），事务里重新查状态，提交后在锁内刷新内存里的条目，才放锁；新写的条目的向量放锁以后异步算（B18）
func (m *Memory) apply(ctx context.Context, b Batch) (Applied, error) {
	w, err := m.writer()
	if err != nil {
		return Applied{}, err
	}
	return w.apply(ctx, b)
}

func (w *writer) apply(ctx context.Context, b Batch) (Applied, error) {
	if w.m.opt.Shared != nil {
		w.m.shareMu.Lock()
		defer w.m.shareMu.Unlock()
		if _, err := w.m.flushShared(ctx); err != nil {
			return Applied{}, err
		}
	}
	if err := validate(b); err != nil {
		return Applied{}, err
	}
	start := w.now()
	unlock, err := w.lock(ctx, b.Wait)
	if err != nil {
		return Applied{}, err
	}
	waited := w.now().Sub(start)
	res, changed, err := func() (Applied, bool, error) {
		// 写到一半 panic（库驱动、替身……）也要放锁：上层把 panic 接住以后，锁还扣着，后面所有的写和 Close 都会永远等下去
		defer unlock()
		// 锁顺序始终角色 -> 共用池；检查与本次专属写入之间不能插入共用撤销。
		if b.By == ByRememberAI && w.m.opt.Shared != nil {
			pool := w.m.opt.Shared.w
			if pool == nil {
				return Applied{}, false, ErrNotWritable
			}
			release, err := pool.lock(ctx, b.Wait)
			if err != nil {
				return Applied{}, false, err
			}
			defer release()
			for _, action := range b.Ops {
				op, ok := action.(FactOp)
				if !ok || op.Subject != "owner" {
					continue
				}
				rows, err := pool.db.QueryContext(ctx, `SELECT text FROM facts WHERE subject = ? AND status IN ('revoked','rejected')`, op.Subject)
				if err != nil {
					return Applied{}, false, err
				}
				blocked := false
				for rows.Next() {
					var text string
					if err = rows.Scan(&text); err != nil {
						break
					}
					if squash(text) == squash(op.Text) {
						blocked = true
						break
					}
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
				if err != nil {
					return Applied{}, false, err
				}
				if blocked {
					return Applied{}, false, ErrRememberRevoked
				}
			}
		}
		return w.applyLocked(ctx, b)
	}()
	if err != nil {
		return Applied{}, err
	}
	res.Waited = waited
	if changed && len(res.Added) > 0 {
		w.m.kickVectors(false)
	}
	if w.m.opt.Shared != nil {
		done, err := w.m.flushShared(ctx)
		if err != nil {
			return Applied{}, err
		}
		for i := range res.Outcomes {
			if o, ok := done[res.Outcomes[i].Ref]; ok {
				res.Outcomes[i] = o
			}
		}
	}
	return res, nil
}

// applyLocked 在持有写锁时跑；changed 为假说明库里没有变化（幂等键已有），不用刷新
func (w *writer) applyLocked(ctx context.Context, b Batch) (res Applied, changed bool, err error) {
	if w.needReload {
		if err := w.reload(); err != nil {
			return Applied{}, false, fmt.Errorf("上一次写好以后条目没刷新成，这次先补刷新也没成：%w", err)
		}
		w.needReload = false
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return Applied{}, false, err
	}
	defer tx.Rollback()
	t := &txRun{w: w, tx: tx, b: b, now: w.now().UnixMilli(), counts: map[string]int{}, dups: map[string]map[string][]*dupRow{}}
	res, err = t.run(ctx)
	if err != nil {
		return Applied{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Applied{}, false, fmt.Errorf("提交：%w", err)
	}
	if res.Duplicate {
		return res, false, nil
	}
	// 库已经写好了：内存里没刷新成不算写失败（调用方重试只会重复），记一笔，下一次拿到锁先补
	if err := w.reload(); err != nil {
		w.needReload = true
		w.logf("记忆[%s]：写好了，条目没刷新成（下次写入时补）", w.m.opt.Character)
	}
	return res, true, nil
}

// reload 重读角色库的条目、重建全文表，再整个换到 Memory 上。在写锁里调
func (w *writer) reload() error {
	items, err := loadItems(w.db)
	if err != nil {
		return err
	}
	if err := w.m.index.SyncText(items); err != nil {
		return err
	}
	byRef := refIndex(items)
	w.m.mu.Lock()
	old := w.m.byRef
	w.m.items, w.m.byRef = items, byRef
	w.m.ver++
	w.m.mu.Unlock()
	// 删了、改了的条目告诉宿主（参考信息块是记忆的派生副本，不能比记忆活得久；宿主删掉引用它们的块）
	if f := w.m.opt.OnChanged; f != nil {
		if changed := changedRefs(old, byRef); len(changed) > 0 {
			f(changed)
		}
	}
	return nil
}

// dupRow 是同文查重时一条已有事实的样子
type dupRow struct{ id, status string }

// txRun 是一个事务里的状态：同文查重的缓存、数过的东西
type txRun struct {
	w      *writer
	tx     *sql.Tx
	b      Batch
	now    int64
	counts map[string]int
	added  []string
	// dups[主体][squash(正文)] = 已有的同文事实，包括这个事务里刚写的；每个主体第一次用到才查
	dups map[string]map[string][]*dupRow
	// once：同一个事务里同一条事实同一个计数只数一次（would_confirm、confirm_blocked_protected，write_confirm.go 的 countOnce）
	once        map[string]bool
	sharedBatch string // 同一角色事务的共享动作保持一个发布批次。
}

func (t *txRun) count(key string) { t.counts[key]++ }

func (t *txRun) ownerActs() bool { return t.b.By == ByRemember }

func (t *txRun) run(ctx context.Context) (Applied, error) {
	b := t.b
	if b.Generation != nil {
		cur, err := t.generation(ctx)
		if err != nil {
			return Applied{}, err
		}
		if cur != *b.Generation {
			return Applied{}, ErrGenerationChanged
		}
	}
	if j := b.Job; j != nil {
		var one int
		err := t.tx.QueryRowContext(ctx, `SELECT 1 FROM memory_jobs WHERE conversation = ? AND from_seq = ? AND to_seq = ? AND version = ?`,
			j.Conversation, j.From, j.To, j.Version).Scan(&one)
		switch {
		case err == nil:
			// 这一批整理过：不写记忆，只保证游标不落在它后面（删了游标重跑也走这里，B2）
			if err := t.moveCursor(ctx, j, true); err != nil {
				return Applied{}, err
			}
			return Applied{Duplicate: true, Counts: map[string]int{CountJobDuplicate: 1}}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return Applied{}, err
		}
		if err := t.checkCursor(ctx, j); err != nil {
			return Applied{}, err
		}
	}
	out := make([]Outcome, 0, len(b.Ops))
	for _, op := range b.Ops {
		shared, publish, queueErr := t.sharedAction(ctx, op)
		if queueErr != nil {
			return Applied{}, queueErr
		}
		if publish {
			o, err := t.queueShared(ctx, shared)
			if err != nil {
				return Applied{}, err
			}
			out = append(out, o)
			continue
		}
		var (
			o   Outcome
			err error
		)
		switch op := op.(type) {
		case FactOp:
			o, err = t.addFact(ctx, op)
		case RevokeOp:
			o, err = t.revoke(ctx, op)
		case EpisodeOp:
			o, err = t.addEpisode(ctx, op)
		case EvidenceOp:
			o, err = t.addEvidence(ctx, op)
		case ConfirmOp:
			o, err = t.confirmOp(ctx, op)
		default:
			// validate 认的动作这里必须有分支：少一个分支就是这个动作被静默忽略，批次照样"成功"
			return Applied{}, fmt.Errorf("不认识的动作 %T", op)
		}
		if err != nil {
			return Applied{}, err
		}
		out = append(out, o)
	}
	if j := b.Job; j != nil {
		merged := map[string]int{}
		for k, v := range j.Counts {
			merged[k] += v
		}
		for k, v := range t.counts {
			merged[k] += v
		}
		raw, _ := json.Marshal(merged)
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO memory_jobs (conversation, from_seq, to_seq, version, done_at, counts_json) VALUES (?, ?, ?, ?, ?, ?)`,
			j.Conversation, j.From, j.To, j.Version, t.now, string(raw)); err != nil {
			return Applied{}, err
		}
		if err := t.moveCursor(ctx, j, false); err != nil {
			return Applied{}, err
		}
	}
	return Applied{Outcomes: out, Counts: t.counts, Added: t.added}, nil
}

// checkCursor：库里的游标还是读这一批时的那个值，才让写（compare-and-set）
func (t *txRun) checkCursor(ctx context.Context, j *Job) error {
	var cur int64
	err := t.tx.QueryRowContext(ctx, `SELECT last_seq FROM summary_state WHERE conversation = ?`, j.Conversation).Scan(&cur)
	has := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (j.Prev == nil && has) || (j.Prev != nil && (!has || cur != *j.Prev)) {
		return ErrCursorMoved
	}
	return nil
}

// moveCursor 把游标挪到 Job.To，退避清零（成功了就不退避，13.2.5）。monotonic：只往前，不往回拉
func (t *txRun) moveCursor(ctx context.Context, j *Job, monotonic bool) error {
	set := `last_seq = excluded.last_seq`
	if monotonic {
		set = `last_seq = max(summary_state.last_seq, excluded.last_seq)`
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO summary_state (conversation, last_seq, updated_at, failures, retry_at) VALUES (?, ?, ?, 0, NULL)
		ON CONFLICT(conversation) DO UPDATE SET `+set+`, updated_at = excluded.updated_at, failures = 0, retry_at = NULL`,
		j.Conversation, j.To, t.now)
	return err
}

func (t *txRun) generation(ctx context.Context) (int64, error) {
	var g int64
	err := t.tx.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = 'generation'`).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

// revise 记一条修订：谁在什么时候把什么从什么改成什么（只增不改）。before、after 是小 JSON
func (t *txRun) revise(ctx context.Context, factID, op string, before, after map[string]any, reason string) error {
	enc := func(v map[string]any) any {
		if v == nil {
			return nil
		}
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	var why any
	if reason != "" {
		why = reason
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO fact_revisions (fact_id, op, at, by, reason, before_json, after_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		factID, op, t.now, t.b.By, why, enc(before), enc(after))
	return err
}

// ───────────── 事实 ─────────────

// duplicates 返回这个主体下已有的同文事实表（有效、待确认、撤销、AI 没通过的；被取代、历史的、有争议的不算），第一次用到才查
func (t *txRun) duplicates(ctx context.Context, subject string) (map[string][]*dupRow, error) {
	if m, ok := t.dups[subject]; ok {
		return m, nil
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT id, text, status FROM facts WHERE coalesce(subject, '') = ? AND status IN ('active', 'candidate', 'revoked', 'rejected') ORDER BY id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][]*dupRow{}
	for rows.Next() {
		var r dupRow
		var text string
		if err := rows.Scan(&r.id, &text, &r.status); err != nil {
			return nil, err
		}
		if k := squash(text); k != "" {
			m[k] = append(m[k], &r)
		}
	}
	t.dups[subject] = m
	return m, rows.Err()
}

// findDup：live 是同文的有效或待确认的那一条（有效的优先），dead 是同文的撤销或 AI 没通过的那一条
func (t *txRun) findDup(ctx context.Context, subject, text string) (live, dead *dupRow, err error) {
	key := squash(text)
	if key == "" {
		return nil, nil, nil
	}
	m, err := t.duplicates(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range m[key] {
		switch r.status {
		case "active":
			return r, nil, nil
		case "candidate":
			if live == nil {
				live = r
			}
		default:
			if dead == nil {
				dead = r
			}
		}
	}
	return live, dead, nil
}

func (t *txRun) addFact(ctx context.Context, op FactOp) (Outcome, error) {
	if op.SupersedesCode != "" {
		id, err := t.resolveCode(ctx, op.SupersedesCode)
		if err != nil {
			return Outcome{}, err
		}
		op.Supersedes = id
	}
	if t.b.By == ByRememberAI && op.Supersedes != "" {
		subject, found, err := t.factSubject(ctx, op.Supersedes)
		if err != nil {
			return Outcome{}, err
		}
		if !found || subject != op.Subject {
			return Outcome{}, ErrReplaceGone
		}
	}
	live, dead, err := t.findDup(ctx, op.Subject, op.Text)
	if err != nil {
		return Outcome{}, err
	}
	switch {
	case live != nil && t.b.OnDup == DupReport:
		return t.reportDup(ctx, live, op)
	case live != nil:
		return t.merge(ctx, live, op)
	case dead != nil && (t.b.OnDup == DupMerge || t.b.By == ByRememberAI):
		// 撤销是主人的决定，AI 没通过的要主人放行：整理里再说起同一句，不新建、不复活（13.2.4 第 8(b) 步，我定的，默认收紧）
		t.count(CountDupRevoked)
		return Outcome{Result: ResultRefused, Ref: "f:" + dead.id, Status: dead.status}, nil
	}
	return t.insertFact(ctx, op)
}

// resolveCode 把 remember 的 6 位短码（编号的末 6 位）找成全长编号：只在有效的事实里找，恰好一条才行。
// 在事务里查（写锁下），所以查到的这一条接着就在同一个事务里被取代，中间换不掉（13.2.3）
func (t *txRun) resolveCode(ctx context.Context, code string) (string, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id FROM facts WHERE status = 'active' AND upper(substr(id, -6)) = ? ORDER BY id LIMIT 2`, code)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("%w：%s", ErrReplaceGone, code)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%w：%s", ErrReplaceAmbiguous, code)
}

// reportDup：同文不新建。remember-ai 可把候选提为 ai，不能把有效事实升级成 owner。
// 更正仍在同一事务内过主体、受保护状态检查，失败连提升一起回滚。
func (t *txRun) reportDup(ctx context.Context, live *dupRow, op FactOp) (Outcome, error) {
	t.count(CountExists)
	out := Outcome{Result: ResultExists, Ref: "f:" + live.id, Status: live.status}
	// 先用更晚的本人原话刷新期限，再看是否仍过期：主人后来又说一遍，旧条目重新算现状是有意的；
	// 刷新后还过期（原话更早、或期限本身已过）才报 ErrRememberExpired
	if err := t.refreshLifetime(ctx, live, op); err != nil {
		return Outcome{}, err
	}
	if t.b.By == ByRememberAI {
		var item Item
		var until sql.NullInt64
		if err := t.tx.QueryRowContext(ctx, `SELECT coalesce(temporal,''),coalesce(learned_at,0),created_at,valid_to FROM facts WHERE id=?`, live.id).Scan(&item.Temporal, &item.LearnedAt, &item.CreatedAt, &until); err != nil {
			return Outcome{}, err
		}
		if until.Valid {
			item.ValidTo = &until.Int64
		}
		if item.Expired(time.UnixMilli(t.now)) {
			return Outcome{}, ErrRememberExpired
		}
	}
	switch {
	case live.status == "candidate":
		if err := t.promote(ctx, live, op); err != nil {
			return Outcome{}, err
		}
		out.Status, out.Promoted = "active", true
	case live.status == "active" && t.ownerActs() && op.OwnerSaid:
		if err := t.ownerConfirmActive(ctx, live.id); err != nil {
			return Outcome{}, err
		}
	case live.status == "active" && t.b.By == ByRememberAI && op.ConfirmedBy == ConfirmedOwnerSaid:
		// 到这里 remember 已经绑定了主人本人的原话（没有来源不会走到写入层）：同文的 ai 事实升成 owner_said，整理以后动不了
		if err := t.ownerSaidUpgrade(ctx, live.id); err != nil {
			return Outcome{}, err
		}
	}
	if op.Supersedes == "" || op.Supersedes == live.id {
		return out, nil
	}
	text, res, err := t.supersede(ctx, op.Supersedes, live.id)
	if err != nil {
		return Outcome{}, err
	}
	if res == supersedeProtected {
		return Outcome{}, ErrReplaceProtected
	}
	if res != supersedeDone {
		return Outcome{}, fmt.Errorf("%w：%s", ErrReplaceGone, op.Supersedes)
	}
	t.count(CountSuperseded)
	out.Replaced = &Replaced{ID: op.Supersedes, Text: text}
	return out, nil
}

// promote 按可信调用来源提升候选；模型路径只会是 ai，修订同时保留期限。
func (t *txRun) promote(ctx context.Context, live *dupRow, op FactOp) error {
	add, confirmed := op.Scopes, op.ConfirmedBy
	switch {
	case confirmed == "owner" && t.ownerActs():
	case confirmed == ConfirmedOwnerSaid && t.b.By == ByRememberAI:
		// remember 的来源已绑定主人本人的原话：候选被它放行，和新写的同一个标记，整理以后动不了
	default:
		confirmed = "ai"
	}
	var kind string
	if err := t.tx.QueryRowContext(ctx, `SELECT kind FROM facts WHERE id = ? AND status = 'candidate'`, live.id).Scan(&kind); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE facts SET status = 'active', confirmed_by = ?, speech_act = ? WHERE id = ?`, confirmed, nullIfEmpty(speechActs[kind]), live.id); err != nil {
		return err
	}
	ref := "f:" + live.id
	have, err := t.scopesOf(ctx, ref)
	if err != nil {
		return err
	}
	after := slices.Clone(have)
	for _, s := range add {
		if !slices.Contains(after, s) {
			after = append(after, s)
		}
	}
	if err := t.insertScopes(ctx, ref, after); err != nil {
		return err
	}
	beforeJSON := map[string]any{"status": "candidate", "scopes": have}
	afterJSON := map[string]any{"status": "active", "confirmed_by": confirmed, "scopes": after}
	if err := t.revise(ctx, live.id, "confirm", beforeJSON, afterJSON, "按写入来源更新确认状态"); err != nil {
		return err
	}
	t.markDup(live.id, "active")
	t.count(CountPromoted)
	return nil
}

// ownerConfirmActive：有效的事实被主人本人又说了一遍，confirmed_by 提成 owner，记一条 confirm 修订。范围一个字不动
// （和 promote 的区别：promote 是待确认变有效、要并范围；这里本来就有效，放行只改"谁放行的"）。
// 事务里重新读 confirmed_by：已经是 owner 就什么都不做，也不记修订
func (t *txRun) ownerConfirmActive(ctx context.Context, id string) error {
	var cur sql.NullString
	err := t.tx.QueryRowContext(ctx, `SELECT confirmed_by FROM facts WHERE id = ? AND status = 'active'`, id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && cur.String == "owner") {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE facts SET confirmed_by = 'owner' WHERE id = ? AND status = 'active'`, id); err != nil {
		return err
	}
	before := map[string]any{"status": "active", "confirmed_by": nil}
	if cur.Valid {
		before["confirmed_by"] = cur.String
	}
	if err := t.revise(ctx, id, "confirm", before, map[string]any{"status": "active", "confirmed_by": "owner"}, "主人本人又说了一遍，放行"); err != nil {
		return err
	}
	t.count(CountOwnerConfirmed)
	return nil
}

// ownerSaidUpgrade：remember 遇到同文的有效事实，只把 confirmed_by = ai 的升成 owner_said，记一条 confirm 修订、计数；范围、期限一个字不动。
// owner、导入的、已 owner_said 的都不碰（事务里重新读 confirmed_by，条件写在 UPDATE 里，被别处改了就不升）
func (t *txRun) ownerSaidUpgrade(ctx context.Context, id string) error {
	r, err := t.tx.ExecContext(ctx, `UPDATE facts SET confirmed_by = ? WHERE id = ? AND status = 'active' AND confirmed_by = 'ai'`, ConfirmedOwnerSaid, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return nil
	}
	if err := t.revise(ctx, id, "confirm", map[string]any{"status": "active", "confirmed_by": "ai"}, map[string]any{"status": "active", "confirmed_by": ConfirmedOwnerSaid}, "主人本人又说了一遍，来源绑定原话，升成 owner_said"); err != nil {
		return err
	}
	t.count(CountOwnerSaidUpgraded)
	return nil
}

// mayRecord：这次证据记不记。本人说的一律记（那是他自己的话）；别人转述的，要这条事实的范围对说话的会话看得见才记（evidence_hidden，W3b）。
// 范围在事务里现读 item_scopes，不用内存里的快照
func (t *txRun) mayRecord(ctx context.Context, factID string, ev *Evidence) (bool, error) {
	if ev.BySubject {
		return true, nil
	}
	scopes, err := t.scopesOf(ctx, "f:"+factID)
	if err != nil {
		return false, err
	}
	a := Audience{Channel: ChannelOf(ev.Conversation), Conversation: ev.Conversation}
	if t.b.Audience != nil {
		a = *t.b.Audience
	}
	if CanSee(scopes, DisclosureScopes(a)) {
		return true, nil
	}
	t.count(CountEvidenceHidden)
	return false, nil
}

// merge 把同文的这一次说法并进已有的那条：证据记一次；本人说的把范围并上去，别人转述的范围不动。
// 待确认的那条，别人转述的证据要说话的会话看得见它才记（mayRecord）：有效的不靠证据变化，照旧都记
func (t *txRun) merge(ctx context.Context, live *dupRow, op FactOp) (Outcome, error) {
	ref := "f:" + live.id
	t.count(CountMerged)
	if ev := op.Evidence; ev != nil {
		if live.status == "candidate" {
			ok, err := t.mayRecord(ctx, live.id, ev)
			if err != nil {
				return Outcome{}, err
			}
			if !ok {
				return Outcome{Result: ResultMerged, Ref: ref, Status: live.status}, nil
			}
		}
		if ev.BySubject {
			if err := t.refreshLifetime(ctx, live, op); err != nil {
				return Outcome{}, err
			}
		}
		changed, err := t.recordEvidence(ctx, live.id, ev)
		if err != nil {
			return Outcome{}, err
		}
		have, err := t.scopesOf(ctx, ref)
		if err != nil {
			return Outcome{}, err
		}
		var add []string
		for _, s := range op.Scopes {
			if !slices.Contains(have, s) && !slices.Contains(add, s) {
				add = append(add, s)
			}
		}
		switch {
		case len(add) == 0:
		case !ev.BySubject:
			t.count(CountScopesKept)
		default:
			for _, s := range add {
				if _, err := t.tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_scopes (ref, scope) VALUES (?, ?)`, ref, s); err != nil {
					return Outcome{}, err
				}
			}
			after := append(slices.Clone(have), add...)
			if err := t.revise(ctx, live.id, "scopes", map[string]any{"scopes": have}, map[string]any{"scopes": after}, "本人又说了一遍，范围并上"); err != nil {
				return Outcome{}, err
			}
			t.count(CountScopesWidened)
		}
		// 记完证据（和本人说的并上的范围）以后，同一个事务里看这条候选攒够没有
		if changed && live.status == "candidate" {
			confirmed, err := t.confirmByEvidence(ctx, live.id)
			if err != nil {
				return Outcome{}, err
			}
			return Outcome{Result: ResultMerged, Ref: ref, Status: live.status, Confirmed: confirmed}, nil
		}
	}
	return Outcome{Result: ResultMerged, Ref: ref, Status: live.status}, nil
}

func (t *txRun) scopesOf(ctx context.Context, ref string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT scope FROM item_scopes WHERE ref = ? ORDER BY scope`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// recordEvidence 记一次证据。同一天同一会话已经有了：还是一次，只是这回如果是本人说的，把"本人说过"补上。
// changed：证据行有变（新记了一行，或补上了"本人说过"）；提升只在有变的时候重新算
func (t *txRun) recordEvidence(ctx context.Context, factID string, ev *Evidence) (changed bool, err error) {
	at := ev.At
	if at == 0 {
		at = t.now
	}
	day := time.UnixMilli(at).In(t.w.loc).Format("2006-01-02")
	var by int
	err = t.tx.QueryRowContext(ctx, `SELECT by_subject FROM fact_evidence WHERE fact_id = ? AND day = ? AND conversation = ?`, factID, day, ev.Conversation).Scan(&by)
	var seq any
	if ev.Seq != 0 {
		seq = ev.Seq
	}
	self := 0
	if ev.BySubject {
		self = 1
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			factID, day, ev.Conversation, seq, nullIfEmpty(ev.Speaker), self, t.now); err != nil {
			return false, err
		}
		t.count(CountEvidenceAdded)
		if self == 1 {
			t.count(CountEvidenceBySelf)
		}
		return true, nil
	case err != nil:
		return false, err
	case by == 0 && self == 1:
		if _, err := t.tx.ExecContext(ctx, `UPDATE fact_evidence SET by_subject = 1, seq = ?, speaker = ? WHERE fact_id = ? AND day = ? AND conversation = ?`,
			seq, nullIfEmpty(ev.Speaker), factID, day, ev.Conversation); err != nil {
			return false, err
		}
		t.count(CountEvidenceRepeat)
		t.count(CountEvidenceBySelf)
		return true, nil
	default:
		t.count(CountEvidenceRepeat)
		return false, nil
	}
}

// addEvidence 给一条待确认的事实记一次证据（整理的 evidence 动作）。事务里重读状态：只有还是 candidate 才记，
// 提示词给出之后它被主人放行、撤销、取代了，或编号根本不存在，都不记、数 evidence_gone；转述的证据说话的会话看不见这条事实也不记、数 evidence_hidden
// （整理的 plan 只放行候选块里的编号，正常到不了这里，写入层不信调用方）。只写 fact_evidence 一行，状态、范围、修订不动
func (t *txRun) addEvidence(ctx context.Context, op EvidenceOp) (Outcome, error) {
	var status string
	err := t.tx.QueryRowContext(ctx, `SELECT status FROM facts WHERE id = ?`, op.FactID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "candidate") {
		t.count(CountEvidenceGone)
		return Outcome{Result: ResultNoop, Ref: "f:" + op.FactID, Status: status}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	ev := op.Evidence
	ok, err := t.mayRecord(ctx, op.FactID, &ev)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Result: ResultNoop, Ref: "f:" + op.FactID, Status: status}, nil
	}
	changed, err := t.recordEvidence(ctx, op.FactID, &ev)
	if err != nil {
		return Outcome{}, err
	}
	if changed {
		confirmed, err := t.confirmByEvidence(ctx, op.FactID)
		if err != nil {
			return Outcome{}, err
		}
		if confirmed {
			return Outcome{Result: ResultEvidence, Ref: "f:" + op.FactID, Status: "active", Confirmed: true}, nil
		}
	}
	return Outcome{Result: ResultEvidence, Ref: "f:" + op.FactID, Status: status}, nil
}

var speechActs = map[string]string{"fact": "assert", "preference": "assert", "promise": "promise", "boundary": "boundary"}

// legacyVisibility：facts.visibility 是 Bun 版留下的 NOT NULL 列，范围已经不看它（只看 item_scopes）。按第一个范围反推，和 memory.ts 的 scopeOf 反过来
func legacyVisibility(scopes []string) string {
	switch first := scopes[0]; {
	case strings.HasPrefix(first, "person:"):
		return "person"
	case strings.HasPrefix(first, "conv:"), first == "public":
		return "public"
	}
	return "private"
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (t *txRun) insertFact(ctx context.Context, op FactOp) (Outcome, error) {
	id := t.w.newID()
	status, confirmed := op.Status, op.ConfirmedBy
	var replaced *Replaced
	reason, wantsSupersede := "", ""
	if op.Supersedes != "" {
		if !t.ownerActs() && t.b.By != ByRememberAI {
			// 整理：写入时不取代任何有效事实（W2）。想取代谁记在这条的 add 修订里（wants_supersede），真正的取代发生在提升它的那个事务里（W3b），
			// 到时还要过白名单。不然模型一次提到"茶"，就能把攒了三次证据的"咖啡"挤出有效集，只剩一条"听说"——升级要三次，被顶掉只要一次。
			// 这条本身一律待确认：整理带 supersedes 写有效的，旧的没被取代、两条互相矛盾的就同时有效了。
			// 旧的讲的是另一个人（主体对不上）：不记想取代谁，数 supersede_subject_mismatch，候选照写
			status, confirmed = "candidate", ""
			oldSubject, found, err := t.factSubject(ctx, op.Supersedes)
			if err != nil {
				return Outcome{}, err
			}
			if found && oldSubject != op.Subject {
				t.count(CountSupersedeSubjectMismatch)
			} else {
				wantsSupersede = op.Supersedes
				reason = "想替换 " + op.Supersedes + "，等提升时再定"
				t.count(CountSupersedeDeferred)
			}
		} else {
			old, res, err := t.supersede(ctx, op.Supersedes, id)
			if err != nil {
				return Outcome{}, err
			}
			if res == supersedeProtected {
				return Outcome{}, ErrReplaceProtected
			}
			if res != supersedeDone {
				// 主人自己的话（remember）：要取代的那条已经不是有效的了，这次什么都不写，整批回滚。
				// 成功文字说"原来那条不再用了"，写下去它就在说谎（规格-记忆-v2 13.2.3：查找和替换同一个事务，0 条就是"这次没有记"）
				return Outcome{}, fmt.Errorf("%w：%s", ErrReplaceGone, op.Supersedes)
			}
			replaced = &Replaced{ID: op.Supersedes, Text: old}
			reason = "替换 " + op.Supersedes
			t.count(CountSuperseded)
		}
	}
	seqs, _ := json.Marshal(append([]int64{}, op.SourceSeqs...))
	var speech any
	if status != "candidate" {
		speech = nullIfEmpty(speechActs[op.Kind])
	}
	var eventAt any
	if op.EventAt != 0 {
		eventAt = op.EventAt
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, origin, conversation, audience_at_origin,
			speech_act, attributed_to, scope, event_at, learned_at, temporal, confirmed_by, only_her, learned_by, valid_to, whisper_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, op.Kind, op.Text, nullIfEmpty(op.Subject), legacyVisibility(op.Scopes), string(seqs), status, t.now, nullIfEmpty(op.Origin), nullIfEmpty(op.Conversation),
		nullIfEmpty(op.AudienceAtOrigin), speech, nullIfEmpty(op.AttributedTo), op.Scopes[0], eventAt, t.now, nullIfEmpty(op.Temporal), nullIfEmpty(confirmed), op.LearnedBy == "", nullIfEmpty(op.LearnedBy), op.ValidTo, whisperJSON(op.Whisper)); err != nil {
		return Outcome{}, fmt.Errorf("写事实：%w", err)
	}
	ref := "f:" + id
	if err := t.insertScopes(ctx, ref, op.Scopes); err != nil {
		return Outcome{}, err
	}
	after := map[string]any{"status": status, "scopes": op.Scopes, "temporal": op.Temporal, "valid_to": op.ValidTo}
	if confirmed != "" {
		after["confirmed_by"] = confirmed
	}
	if wantsSupersede != "" {
		after["wants_supersede"] = wantsSupersede // 只在新候选落库时写这一次，提升时读一次；修订只增不改，不当可变状态用
	}
	if err := t.revise(ctx, id, "add", nil, after, reason); err != nil {
		return Outcome{}, err
	}
	if op.Evidence != nil {
		// 新写的这条自带的一次证据不触发提升：一次永远不够（confirmOccasions 至少 2），也不该让一条刚写下的候选在创建它的这一批里变有效
		if _, err := t.recordEvidence(ctx, id, op.Evidence); err != nil {
			return Outcome{}, err
		}
	}
	// 同一个事务里后面的动作也要能撞上这一条
	if k := squash(op.Text); k != "" {
		m, err := t.duplicates(ctx, op.Subject)
		if err != nil {
			return Outcome{}, err
		}
		m[k] = append(m[k], &dupRow{id: id, status: status})
	}
	t.added = append(t.added, ref)
	if status == "candidate" {
		t.count(CountAddedCandidate)
	} else {
		t.count(CountAddedActive)
	}
	return Outcome{Result: ResultAdded, Ref: ref, Status: status, Replaced: replaced}, nil
}

func (t *txRun) insertScopes(ctx context.Context, ref string, scopes []string) error {
	for _, s := range scopes {
		if _, err := t.tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_scopes (ref, scope) VALUES (?, ?)`, ref, s); err != nil {
			return fmt.Errorf("写范围：%w", err)
		}
	}
	return nil
}

// protectedFrom：这一批（by 是 Batch.By）动不动得了这条有效事实，白名单写法（W2）。
//   - 主人本人（ByRemember）：什么都动得了。
//   - remember（ByRememberAI，来源绑定了主人本人的原话）：ai 和 owner_said 动得了；owner（主人放行过）、导入的、不认识的值受保护。
//   - 其余（整理，以及将来任何没登记的 By）：只有 ai（AI 自己攒够证据提升的，W3b）动得了；
//     NULL（Bun 导入的，迁移 0001 只加列、没回填）、空串、owner、owner_said、不认识的值，一律受保护——以后加了新的值忘了改这里，也是受保护，不是放行。
//
// 取代和撤销都调它，不各写一份比较；在事务里用现读的 confirmed_by 判断，不用快照里的旧值
func protectedFrom(by, confirmedBy string) bool {
	switch by {
	case ByRemember:
		return false
	case ByRememberAI:
		return confirmedBy != "ai" && confirmedBy != ConfirmedOwnerSaid
	}
	return confirmedBy != "ai"
}

// supersedeResult：supersede 的结局。调用方要分得清"没有可取代的"和"有，但受保护"：前者提升候选时照常提升，后者这条候选不提升（W3b）
type supersedeResult int

const (
	supersedeDone      supersedeResult = iota // 取代了
	supersedeGone                             // 编号不存在，或不是有效的了（被别处取代、撤销、本来就是待确认的）
	supersedeProtected                        // 是有效的，但 AI 动不了（confirmed_by 不是 ai）
)

// shortRef：日志里指一条事实用的短码（编号末 6 位，大写，和 memory_search 给她看的 # 编号同一个）。日志只写 ref 和原因码，不写正文
func shortRef(id string) string {
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return "f:" + strings.ToUpper(id)
}

// factSubject 读一条事实的主体（NULL 当空串）。found 为假是没有这个编号
func (t *txRun) factSubject(ctx context.Context, id string) (subject string, found bool, err error) {
	err = t.tx.QueryRowContext(ctx, `SELECT coalesce(subject, '') FROM facts WHERE id = ?`, id).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return subject, err == nil, err
}

// supersedeTarget 只读：oldID 此刻能不能被这一批的动作取代，不改库、不计数。事务里现查状态，不用快照：
// 只取代有效的；整理（AI）还要旧的是 confirmed_by = 'ai' 的（protectedFromAI），主人自己（remember）可以取代任何有效的。
// supersedeDone 在这里的意思是"可以取代"。记账模式的提升判断（W3b）用它看一眼，不真去取代
func (t *txRun) supersedeTarget(ctx context.Context, oldID string) (text string, res supersedeResult, err error) {
	var status string
	var confirmed sql.NullString
	err = t.tx.QueryRowContext(ctx, `SELECT text, status, confirmed_by FROM facts WHERE id = ?`, oldID).Scan(&text, &status, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", supersedeGone, nil
	}
	if err != nil {
		return "", supersedeGone, err
	}
	if status != "active" {
		return "", supersedeGone, nil
	}
	if protectedFrom(t.b.By, confirmed.String) {
		return "", supersedeProtected, nil
	}
	return text, supersedeDone, nil
}

// supersede 把 oldID 标成被 newID 取代（判断见 supersedeTarget）。没改到任何行不记修订（Bun 无条件记，拆解 12.1）；受保护的计 supersede_protected、日志一行
func (t *txRun) supersede(ctx context.Context, oldID, newID string) (text string, res supersedeResult, err error) {
	text, res, err = t.supersedeTarget(ctx, oldID)
	if err != nil || res == supersedeGone {
		return "", supersedeGone, err
	}
	if res == supersedeProtected {
		t.count(CountSupersedeProtected)
		t.w.logf("记忆[%s]：整理想取代 %s 被保护挡下（原因 protected）", t.w.m.opt.Character, shortRef(oldID))
		return "", supersedeProtected, nil
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE facts SET status = 'superseded', superseded_by = ? WHERE id = ?`, newID, oldID); err != nil {
		return "", supersedeGone, err
	}
	if err := t.revise(ctx, oldID, "supersede", map[string]any{"status": "active"}, map[string]any{"status": "superseded", "superseded_by": newID}, "被 "+newID+" 替换"); err != nil {
		return "", supersedeGone, err
	}
	t.forget(oldID)
	return text, supersedeDone, nil
}

// markDup：这条的状态变了，同文查重的缓存里同步改。撤销用它：撤销过的仍然要挡同文
func (t *txRun) markDup(id, status string) {
	for _, m := range t.dups {
		for _, rows := range m {
			for _, r := range rows {
				if r.id == id {
					r.status = status
				}
			}
		}
	}
}

// forget：这条不再是有效或待确认的了，同文查重的缓存里也拿掉。被取代的用它（被取代的不算同文，「又搬回北京」是正常的）
func (t *txRun) forget(id string) {
	for _, m := range t.dups {
		for k, rows := range m {
			m[k] = slices.DeleteFunc(rows, func(r *dupRow) bool { return r.id == id })
		}
	}
}

// revoke 撤销一条有效的事实：只动有效的（B14、4.4）；整理（AI）还要它是 confirmed_by = 'ai' 的（protectedFromAI，W2），主人自己（remember）不受这条限制。
// 已经撤过、被取代、编号不存在的计 revoke_noop；有效但受保护的计 revoke_protected、日志一行。两种都不动、不记修订
func (t *txRun) revoke(ctx context.Context, op RevokeOp) (Outcome, error) {
	if op.Code != "" {
		id, err := t.resolveCode(ctx, op.Code)
		if err != nil {
			return Outcome{}, err
		}
		op.ID = id
	}
	var status string
	var confirmed sql.NullString
	err := t.tx.QueryRowContext(ctx, `SELECT status, confirmed_by FROM facts WHERE id = ?`, op.ID).Scan(&status, &confirmed)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
		t.count(CountRevokeNoop)
		return Outcome{Result: ResultNoop, Ref: "f:" + op.ID, Status: status}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if protectedFrom(t.b.By, confirmed.String) {
		t.count(CountRevokeProtected)
		t.w.logf("记忆[%s]：整理想撤销 %s 被保护挡下（原因 protected）", t.w.m.opt.Character, shortRef(op.ID))
		return Outcome{Result: ResultNoop, Ref: "f:" + op.ID, Status: status}, nil
	}
	if t.b.By == ByRememberAI {
		// 和 remember 的 replaces 同一道门槛：新写的只能是 owner、self，所以能取代的也只有这两种主体；别人的事实它动不了
		subject, _, err := t.factSubject(ctx, op.ID)
		if err != nil {
			return Outcome{}, err
		}
		if subject != "owner" && subject != "self" {
			t.count(CountRevokeWrongSubject)
			return Outcome{Result: ResultNoop, Ref: "f:" + op.ID, Status: status}, nil
		}
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE facts SET status = 'revoked' WHERE id = ?`, op.ID); err != nil {
		return Outcome{}, err
	}
	if err := t.revise(ctx, op.ID, "revoke", map[string]any{"status": "active"}, map[string]any{"status": "revoked"}, op.Reason); err != nil {
		return Outcome{}, err
	}
	t.markDup(op.ID, "revoked") // 缓存里留着这一行：同一批里后面再说同一句，还是撞得上"撤销过的"，不能新建（13.2.4 第 8(b) 步）
	t.count(CountRevoked)
	return Outcome{Result: ResultRevoked, Ref: "f:" + op.ID, Status: "revoked"}, nil
}

// ───────────── 片段 ─────────────

func (t *txRun) addEpisode(ctx context.Context, op EpisodeOp) (Outcome, error) {
	id := t.w.newID()
	var eventAt any
	if op.EventAt != 0 {
		eventAt = op.EventAt
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope, origin, audience_at_origin, event_at, learned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, op.Conversation, op.Text, legacyVisibility(op.Scopes), op.FromSeq, op.ToSeq, t.now, op.Scopes[0], nullIfEmpty(op.Origin), nullIfEmpty(op.AudienceAtOrigin), eventAt, t.now); err != nil {
		return Outcome{}, fmt.Errorf("写片段：%w", err)
	}
	ref := "e:" + id
	if err := t.insertScopes(ctx, ref, op.Scopes); err != nil {
		return Outcome{}, err
	}
	t.added = append(t.added, ref)
	t.count(CountAddedEpisode)
	return Outcome{Result: ResultAdded, Ref: ref, Status: "active"}, nil
}

// ───────────── 游标、幂等键、世代号 ─────────────

// SummaryState 是 summary_state 的一行：整理游标和退避（13.2.5）
type SummaryState struct {
	Conversation string
	LastSeq      int64
	Failures     int   // 连着失败几次
	RetryAt      int64 // 毫秒，0 = 没有
	UpdatedAt    int64
}

// SummaryState 读一个会话的游标。没有这一行（新会话）ok 为假
func (m *Memory) SummaryState(ctx context.Context, conv string) (s SummaryState, ok bool, err error) {
	w, err := m.writer()
	if err != nil {
		return s, false, err
	}
	var retry sql.NullInt64
	err = w.db.QueryRowContext(ctx, `SELECT conversation, last_seq, failures, retry_at, updated_at FROM summary_state WHERE conversation = ?`, conv).
		Scan(&s.Conversation, &s.LastSeq, &s.Failures, &retry, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryState{}, false, nil
	}
	s.RetryAt = retry.Int64
	return s, err == nil, err
}

// JobDone：这一批（会话、起止 seq、版本）整理过没有。整理在调模型之前先问，整理过就只挪游标（B2）
func (m *Memory) JobDone(ctx context.Context, conv string, from, to int64, version int) (bool, error) {
	w, err := m.writer()
	if err != nil {
		return false, err
	}
	var one int
	err = w.db.QueryRowContext(ctx, `SELECT 1 FROM memory_jobs WHERE conversation = ? AND from_seq = ? AND to_seq = ? AND version = ?`, conv, from, to, version).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// InitCursor 给新会话立游标：已经有就不动。新会话从启动时刻的最新 seq 开始，不回填历史（13.2.4 第 1 步）
func (m *Memory) InitCursor(ctx context.Context, conv string, seq int64) (created bool, err error) {
	w, err := m.writer()
	if err != nil {
		return false, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return false, err
	}
	defer unlock()
	r, err := w.db.ExecContext(ctx, `INSERT OR IGNORE INTO summary_state (conversation, last_seq, updated_at, failures, retry_at) VALUES (?, ?, ?, 0, NULL)`, conv, seq, w.now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// Generation 是库的世代号。整理在调模型之前读一次，写的时候放进 Batch.Generation；期间有人忘掉、真删过就对不上，整批作废
func (m *Memory) Generation(ctx context.Context) (int64, error) {
	w, err := m.writer()
	if err != nil {
		return 0, err
	}
	var g int64
	err = w.db.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = 'generation'`).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

// BumpGeneration 世代号加一。忘掉、真删前调（也要拿写锁，和整理的写互斥）：此刻在途的整理结果落不了库
func (m *Memory) BumpGeneration(ctx context.Context) (int64, error) {
	w, err := m.writer()
	if err != nil {
		return 0, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer unlock()
	var g int64
	err = w.db.QueryRowContext(ctx, `INSERT INTO memory_meta (key, value) VALUES ('generation', 1) ON CONFLICT(key) DO UPDATE SET value = value + 1 RETURNING value`).Scan(&g)
	return g, err
}
