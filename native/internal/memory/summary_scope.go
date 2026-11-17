package memory

import (
	"strings"
	"time"
)

// 整理的输出读完以后，落成能写进库的动作（规格-记忆-v2 13.2.6，W4）。模型只说"这是谁的事"（subject）和"要不要收着"（visibility，只许收紧），
// 范围、有效还是待确认、证据，都由程序按这一批的来源定，不相信模型填的。
//
// 这一版整理写进去的事实一律是待确认（candidate）：有效要攒够次数或主人放行（ADR-160 第 1 条、4.4），晋升的逻辑归第 3 步。
// 所以 upsert_fact 和 candidate 的区别只剩 visibility 能收紧范围、supersedes 能记下想取代谁：写入时不取代任何有效事实，
// 只记在这条候选 add 修订的 wants_supersede 里，提升它的那个事务里才取代，而且旧的要是 confirmed_by = 'ai' 的才行（write.go 的 protectedFromAI，W2）。
//
// evidence 动作（W3a）给已有的候选补一次证据，不新写事实、不改状态和范围：plan.evidence 查完 id、src、说话人身份，只出一个 EvidenceOp。

// sayLine 是交给模型的聊天记录里的一行。src 写的编号就是 seq；不在这张表里的编号不是这一批给它看的
type sayLine struct {
	seq    int64
	at     int64  // 毫秒
	kind   string // ActorKind
	id     string // ActorID
	origin string
	text   string // 可信原话，独立核对显式专属限制，不用模型重写后的事实正文。
}

// 只有人说的话（origin 是 owner 或 peer）能当证据；她自己的、别的角色的、她的号上别人发的（human、external）都不能，
// 它们还是留在聊天记录里让模型看懂上下文（13.2.4 第 2 步，B8）
func (l sayLine) evidence() bool { return l.origin == "owner" || l.origin == "peer" }

// 撤销的依据只能是主人自己说的话（我定的，默认收紧）
func (l sayLine) ownerSaid() bool { return l.kind == "owner" && l.origin == "owner" }

// column 是 4.2 那张表的三列：主人自己的事、她自己的事、别人自己的事
type column int

const (
	colOwner column = iota
	colSelf
	colOther
)

// candInfo：一条给模型看过的候选是讲谁的事（库里的 subject 和它落在 4.2 的哪一列）
type candInfo struct {
	subject string
	col     column
}

// candColumn：库里的 subject 落在哪一列。owner、self 照写，其余（某人的号，或者没认出是谁的空串）是别人的事
func candColumn(subject string) column {
	switch subject {
	case "owner":
		return colOwner
	case "self":
		return colSelf
	}
	return colOther
}

// plan 是一批整理要落成动作所需要知道的：在哪个场合、聊天记录里有谁、给模型看了哪些已知事实
type plan struct {
	now   time.Time
	conv  string
	place Place
	// 平台私聊：这是不是主人的私聊（对方是主人）；不是的话对方是谁。会话里从没出现过主人的行就按"某人"算（收紧）
	ownerPrivate bool
	partner      string // 平台私聊（某人）里对方的 ActorID；不知道是空
	lines        map[int64]sayLine
	people       map[string]bool // 说话人表里能写的 ActorID（不含主人和她自己）
	known        map[string]bool // 这一批给模型看过的已知事实的编号（不带 f:）
	// cands 是这一批给模型看过的"听说过、还没确认的"候选（编号不带 f:），带着程序要用的主体和列：evidence 只认这里面的编号，
	// BySubject 按候选主体和真实发言身份算；跨场合候选也会列出，写入层另判转述能否记账。
	cands map[string]candInfo
	// noEvidence：整理提示词里没有候选块的占位符（prompt_stale），这一批不认 evidence
	noEvidence  bool
	hasOwner    bool   // 聊天记录里有主人的行
	firstSeq    int64  // 这一批读到的第一条，片段的 from_seq
	lastSeq     int64  // 这一批读到的最后一条，片段的 to_seq
	firstAt     int64  // 留下的行里最早的时间，片段的发生时间
	anyPeer     bool   // 聊天记录里有群友（peer）的行：谁在场，audience_at_origin 用
	firstOrigin string // 留下的行里第一个不是她自己说的行的 origin，片段的来源
}

// subject 把模型写的 subject 对成号。owner、self 照写；说话人表里的编号（qq:…）就是那个人；没写当"别人的事"，主体留空；
// 其余（编出来的、名字、不在这一批的）对不上，丢这一条
func (p *plan) subject(raw string) (subject string, col column, ok bool) {
	switch strings.ToLower(raw) {
	case "owner":
		return "owner", colOwner, true
	case "self":
		return "self", colSelf, true
	case "":
		return "", colOther, true
	}
	if p.people[raw] {
		return raw, colOther, true
	}
	return "", 0, false
}

// bySubject：说这句话的就是这条事实讲的那个人（主人的事 = 主人说的；某人的事 = 他本人说的）。她自己的事没有"本人说"这回事（她的话不当证据）
func (l sayLine) bySubject(subject string, col column) bool {
	switch col {
	case colOwner:
		return l.kind == "owner"
	case colOther:
		return subject != "" && l.id == subject
	}
	return false
}

// factScopes 是新事实的范围（4.2 那张表，范围是一组）。visibility 只许收紧：private（没写也按 private，Bun 的默认值）去掉组里的 conv:*；
// 去光了就保持表里的范围不动，只记一笔。不退到 person:他 或 owner：那不是收紧而是换了一个不相干的范围（"不是包含关系"，和 person 不用是同一个道理），
// 她的事、群友的事会因此跑出这个群（别的群里主人一问就想得起来，或者只在他的私聊里想得起来）。person、public 不改，范围照表
func (p *plan) factScopes(col column, subject string, tighten bool, c counter) []string {
	var scopes []string
	switch p.place {
	case LocalPrivate, LocalGroup:
		scopes = []string{"owner"}
	case PlatformPrivate:
		switch {
		case p.ownerPrivate || col == colOwner:
			// 主人的私聊三列都是 owner；某人的私聊里提到主人的事也是 owner（一律待确认，attributed_to 写说的那个人）
			scopes = []string{"owner"}
		case col == colOther && subject != "":
			scopes = []string{"person:" + subject}
		case p.partner != "":
			scopes = []string{"person:" + p.partner}
		default:
			// 不知道对方是谁：退到这个会话本身，它在这个私聊里照样想得起来
			scopes = []string{"conv:" + p.conv}
		}
	default: // PlatformGroup
		scopes = []string{"conv:" + p.conv}
		if col == colOwner {
			scopes = []string{"owner", "conv:" + p.conv}
		}
	}
	if !tighten {
		return scopes
	}
	var kept []string
	for _, s := range scopes {
		if !strings.HasPrefix(s, "conv:") {
			kept = append(kept, s)
		}
	}
	if len(kept) > 0 {
		return kept
	}
	c.add(CountScopeNotNarrowed)
	return scopes
}

// episodeScopes 是片段的范围：整段对话的摘要，装着这一批里每个人说的话。本地和主人的私聊都是 owner；某人的私聊只给那个人；
// 平台群聊是这个群，主人在这一批里说过话就再加 owner（话是在群里说的，群里的人都看到了，ADR-159 第 4 条的一组范围）
func (p *plan) episodeScopes() []string {
	switch p.place {
	case LocalPrivate, LocalGroup:
		return []string{"owner"}
	case PlatformPrivate:
		switch {
		case p.ownerPrivate:
			return []string{"owner"}
		case p.partner != "":
			return []string{"person:" + p.partner}
		}
		return []string{"conv:" + p.conv}
	}
	if p.hasOwner {
		return []string{"owner", "conv:" + p.conv}
	}
	return []string{"conv:" + p.conv}
}

// audienceAt 是 audience_at_origin：说这话的时候谁在场。照 Bun 的 sourceMeta：原话里有群友（peer）说的就是 members，否则只有主人
func audienceAt(anyPeer bool) string {
	if anyPeer {
		return "members"
	}
	return "owner"
}

// source 检查一条动作的 src 并挑出证据。src 里只要有一个编号不是这一批给模型看过的，整个 src 当没有（越界按"没有 src"算，不退回整批原话：
// 退回会让批里任一句的归属扩大，13.2.6）。证据只用人说的话（owner / peer）。
//
// 只有 src 恰好引用一行、并且那一行是当事人说的，这条才算"本人说的"（ADR-231，主人定）：written 是 src 去重之前写了几个序号，
// [5,5] 是两行，不去重（收紧）；两行以上一律不是，哪怕每一行都是当事人说的。
// 多行时挑第一句人话当证据（用来记 attributed_to、origin、发生时间），不再"本人说的优先"：本人那一行不能给整条背书，
// 不然模型把一句无关的当事人的话夹进 src，别人转述的事实就变成本人说的，再借它参与提升、并进新的可见范围
func (p *plan) source(src []int64, written int, subject string, col column, c counter) (seqs []int64, pick *sayLine, by bool) {
	if len(src) == 0 {
		return nil, nil, false
	}
	var ls []sayLine
	for _, n := range src {
		l, ok := p.lines[n]
		if !ok {
			c.add(CountSrcOutOfBatch)
			return nil, nil, false
		}
		ls = append(ls, l)
	}
	for i := range ls {
		if ls[i].evidence() {
			pick = &ls[i]
			break
		}
	}
	if pick == nil {
		c.add(CountSrcNoEvidence)
		return src, nil, false
	}
	// written 比 len(src) 小只会是调用方没传对，按多的算（收紧）
	return src, pick, max(written, len(src)) == 1 && pick.bySubject(subject, col)
}

// ops 把读完的输出落成动作：先片段，再模型给的动作（按它写的顺序）。丢掉的和降级的都记进 c
func (p *plan) ops(out *summaryOut, c counter) []Op {
	var ops []Op
	if out.episode != "" && secretShaped(out.episode) {
		c.add(CountSecretDropped)
	} else if out.episode != "" {
		ops = append(ops, EpisodeOp{
			Conversation: p.conv, Text: out.episode, FromSeq: p.firstSeq, ToSeq: p.lastSeq, Scopes: p.episodeScopes(),
			Origin: p.firstOrigin, AudienceAtOrigin: audienceAt(p.anyPeer), EventAt: p.firstAt,
		})
	}
	seenEvidence := map[string]bool{}
	for _, o := range out.ops {
		switch o.op {
		case "revoke":
			if op, ok := p.revoke(o, c); ok {
				ops = append(ops, op)
			}
		case "evidence":
			if p.noEvidence {
				c.add(CountOpUnknown)
				continue
			}
			if op, ok := p.evidence(o, c, seenEvidence); ok {
				ops = append(ops, op)
			}
		default:
			if op, ok := p.fact(o, c); ok {
				ops = append(ops, op)
			}
		}
	}
	return ops
}

// revoke：撤销只认这一批给模型看过的编号（B9，不在里面的丢掉），并且 src 要恰好是一个序号、那一行是主人自己说的话（13.2.6，我定的，默认收紧）：
// 该事实本人说的"这件事不对"也不撤销；src 里夹别的行（不管是不是主人的话）也不撤销，免得一句无关的主人的话替别人的话作证。
// 模型不带 src 的撤销会被拒（CountRevokeRefused）；summarize-v2.md 有这一栏（W3a）。重复的序号去重以后算一个。
// 过了这里到了写入层还要过白名单：只有 confirmed_by = 'ai' 的有效事实撤得动，导入的、主人放行过的撤销计 revoke_protected（write.go 的 revoke，W2）
func (p *plan) revoke(o sumOp, c counter) (Op, bool) {
	if !p.known[o.id] {
		c.add(CountRevokeUnknown)
		return nil, false
	}
	if len(o.src) != 1 {
		c.add(CountRevokeRefused)
		return nil, false
	}
	l, ok := p.lines[o.src[0]]
	if !ok {
		c.add(CountSrcOutOfBatch)
		c.add(CountRevokeRefused)
		return nil, false
	}
	if !l.ownerSaid() {
		c.add(CountRevokeRefused)
		return nil, false
	}
	c.add(CountOpRevoke)
	return RevokeOp{ID: o.id}, true
}

// evidence：给一条这一批给模型看过的候选补一次证据（W3a）。程序按这几步检查，模型写的别的字段都不读：
//  1. id 必须是这一批候选块里的编号（编的、没给它看的、已知事实的编号都丢，evidence_unknown）；
//  2. src 恰好一个序号（evidence_bad_src），那一行必须在这一批的聊天记录里（src_out_of_batch；补录、自检、automation 的行被选材丢了，在这里也是"不在批里"）；
//  3. 那一行必须是真人的话（origin 是 owner 或 peer，evidence_not_human）：她自己的、别的角色的、她的号上别人发的都不算；
//  4. BySubject 由程序按那一行的说话人身份是不是候选讲的那个人算，不比名字；会话、时间（定"哪一天"）、说话人都取那一行本身；
//  5. 同一批里对同一条候选只认第一次（evidence_dup）。同一天同一会话还是一次，写入层的主键已经保证。
//
// 模型想让候选变有效、改范围，都没有字段可写：这里只出一个 EvidenceOp，写入层只记 fact_evidence 一行
func (p *plan) evidence(o sumOp, c counter, seen map[string]bool) (Op, bool) {
	cand, ok := p.cands[o.id]
	if !ok {
		c.add(CountEvidenceUnknown)
		return nil, false
	}
	if len(o.src) != 1 {
		c.add(CountEvidenceBadSrc)
		return nil, false
	}
	l, ok := p.lines[o.src[0]]
	if !ok {
		c.add(CountSrcOutOfBatch)
		return nil, false
	}
	if !l.evidence() {
		c.add(CountEvidenceNotHuman)
		return nil, false
	}
	if seen[o.id] {
		c.add(CountEvidenceDup)
		return nil, false
	}
	seen[o.id] = true
	c.add(CountOpEvidence)
	return EvidenceOp{FactID: o.id, Evidence: Evidence{
		Conversation: p.conv, At: l.at, Seq: l.seq, Speaker: l.id, BySubject: l.bySubject(cand.subject, cand.col),
	}}, true
}

// fact：一律写待确认（4.4）。范围、证据、归属都由程序定
func (p *plan) fact(o sumOp, c counter) (Op, bool) {
	if secretShaped(o.text) {
		c.add(CountSecretDropped)
		return nil, false
	}
	subject, col, ok := p.subject(o.subject)
	if !ok {
		c.add(CountSubjectUnknown)
		return nil, false
	}
	supersedes := o.supersedes
	if supersedes != "" && !p.known[supersedes] {
		// 替换的旧编号不在这一批给它看的范围里：去掉 supersedes，新事实照记（B9）
		c.add(CountSupersedesUnknown)
		supersedes = ""
	}
	if o.op == "upsert_fact" {
		c.add(CountOpUpsert)
	} else {
		c.add(CountOpCandidate)
	}
	seqs, pick, by := p.source(o.src, o.srcN, subject, col, c)
	temporal, until, err := memoryLifetime(o.kind, o.temporal, o.validUntil, p.now)
	if err != nil {
		c.add("invalid_lifetime")
		return nil, false
	}
	fact := FactOp{
		Kind: o.kind, Text: o.text, Subject: subject, Status: "candidate",
		Scopes:     p.factScopes(col, subject, o.op == "upsert_fact" && (o.visibility == "" || o.visibility == "private"), c),
		Supersedes: supersedes, Temporal: temporal, ValidTo: until, SourceSeqs: seqs, Conversation: p.conv,
	}
	if pick != nil {
		fact.Origin, fact.EventAt = pick.origin, pick.at
		fact.AudienceAtOrigin = audienceAt(pick.origin == "peer" || p.srcHasPeer(seqs))
		fact.Evidence = &Evidence{Conversation: p.conv, At: pick.at, Seq: pick.seq, Speaker: pick.id, BySubject: by}
		fact.Share = shareableFact(o.kind, subject, o.onlyHer, by && pick.ownerSaid(), pick.text)
		if !by {
			// 别人转述的（"据 A 说 B"）：记下是谁说的，算转述，不算本人说的（4.4 第 1 条）
			fact.AttributedTo = pick.id
		}
	} else {
		fact.AudienceAtOrigin = audienceAt(p.srcHasPeer(seqs) || (len(seqs) == 0 && p.anyPeer))
	}
	return fact, true
}

func (p *plan) srcHasPeer(seqs []int64) bool {
	for _, n := range seqs {
		if p.lines[n].origin == "peer" {
			return true
		}
	}
	return false
}
