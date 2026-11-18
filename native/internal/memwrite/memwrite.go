// Package memwrite 决定一个角色的记忆开不开写入、开哪一档（规格-记忆写入上线 W5）。
//
// 两档：memory.write.characters 默认 ["*"]（所有角色，主人在私聊里让她记的 remember 可写）；其中再列进 memory.write.summarize 的，才做后台整理（默认空，不整理）。
// 两档的前提不一样。两档都不开（保持只读，写一行提醒和原因码）：不在名单里、库不是 Go 的副本、没迁移、开写前的备份做不成；
// 以可写方式打开失败、开写时间记不进库（含清 catchUp 记录失败）也退回只读。只关整理、remember 照开：前提 5（配了后台模型、catchUp 已选）不满足、
// 开整理前的备份做不成、catchUp=latest 推游标出错、开整理时间记不进库。不存在"退到半开"：
// 前提不满足时不会只开一半，用户明确配置的两档才是仅有的两种开法。
//
// 开写的第一步都发生在 memory.Open 之前（只读检查和备份），库在备份成功之前不会以可写方式打开；Open 之后才把开写时间记进库，
// 记不进去就退回只读。检查只读真实路径和文件，不迁移、不改库（备份只读源库）。
package memwrite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
)

// 原因码：日志和提醒只写它们，不写错误原文（里面可能有路径）
const (
	ReasonNotListed         = "not_listed"              // 别的角色在 memory.write 的名单里、这个没列（名单全空或 "*" 都是主人明确的意思，不记）
	ReasonSummarizeNoWrite  = "summarize_without_write" // 在 summarize 里却不在 characters 里：整理需要先开写
	ReasonNoMemoryDB        = "no_memory_db"            // 配了要写，但这个角色没有记忆库
	ReasonNotGoCopy         = "path_not_go_copy"        // 库不在角色记忆目录下，或路径里有链接
	ReasonPathError         = "path_error"              // 看不了库的路径
	ReasonNotMigrated       = "schema_not_migrated"     // 库没迁移（不自动迁移，请运行 memory-migrate）
	ReasonSchemaError       = "schema_error"            // 库的表对不上
	ReasonPrecheckTimeout   = "precheck_timeout"        // 检查超过 10 秒
	ReasonBackupFailed      = "backup_failed"           // 开写前的备份没做成
	ReasonOpenFailed        = "open_writable_failed"    // 以可写方式打开失败
	ReasonMetaFailed        = "meta_write_failed"       // 开写时间记不进库
	ReasonNoBackgroundModel = "no_background_model"     // 没配后台模型：整理会静默跳过，开了等于没开
	ReasonCatchUpNotChosen  = "catchup_not_chosen"      // catchUp 没选：悄悄接着旧游标整理会花钱
	ReasonSummarizeBackup   = "backup_summarize_failed" // 开整理前的备份没做成
	ReasonCatchUpFailed     = "catchup_failed"          // catchUp=latest 推游标出错
	ReasonSummarizeMeta     = "summarize_meta_failed"   // 开整理时间记不进库（catchUp 本身没出错）
	ReasonSummarizeOpen     = "open_summarize_failed"   // 以整理打开后的收尾出错
)

// 前提检查整体时限（只管看路径、查表、读 memory_meta，不含备份）；备份有自己的时限
const (
	PrecheckTimeout = 10 * time.Second
	BackupTimeout   = 10 * time.Minute
)

// Input 是给一个角色决定要用的东西，都由装配处填。Data 是数据目录，DB 是 paths.Paths.MemoryDB 给的库路径
type Input struct {
	Data, DB, Char string
	Cfg            config.MemoryWrite
	BackupDir      string // 备份目录：装配处给 backup.Service.Dir() 的结果（和每天备份是同一个位置）；空才用 BackupFallback
	BackupFallback string // paths.BackupRoot()
	BackupDirErr   error  // 备份目录算不出来（写进了数据目录里、是盘符根之类）：开写前的备份没地方放，不开写（和备份做不成一样），原因记日志
	HasModel       bool   // 这个角色配了后台模型
	Now            func() time.Time
	Logf           func(format string, args ...any)
	// 测试里调短
	Precheck, Backup time.Duration
	// precheckStarted 只给测试用：前提检查的时限一开始就调。测"检查超时"时在这里等到时限真的到了再往下走，
	// 不靠一个极短的时限去赌计时器先触发（机器忙时检查会比计时器先做完）
	precheckStarted func(context.Context)
}

// Settings 是要交给 memory.Options 的几个字段，装配处原样抄过去
type Settings struct {
	Writable, SummarizeEnabled bool
	WritePlaces                []string
	AutoConfirm                bool
	ConfirmOccasions           int
	ConfirmKinds               []string
	// QuarantineSince：止血的隔离时间（毫秒，0 = 不隔离）。只读打开的角色也带着：关了开关以后已经写进库的东西照样要能被藏起来
	QuarantineSince int64
}

// Apply 把设置抄进 memory.Options
func (s Settings) Apply(o *memory.Options) {
	o.Writable, o.SummarizeEnabled, o.WritePlaces = s.Writable, s.SummarizeEnabled, s.WritePlaces
	o.AutoConfirm, o.ConfirmOccasions, o.ConfirmKinds = s.AutoConfirm, s.ConfirmOccasions, s.ConfirmKinds
	o.QuarantineSince = s.QuarantineSince
}

// quarantineOf：配置里的隔离时间（毫秒数字串）。空或不是正整数都按不隔离——配置检查已经拒绝了写坏的，这里只是不 panic
func quarantineOf(c config.MemoryWrite) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(c.QuarantineSince), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// QuarantineSince 是配置里的隔离时间（毫秒，0 = 不隔离），和打开记忆库时用的是同一个解析；记忆页的读服务也用它，口径只有这一份
func QuarantineSince(c config.MemoryWrite) int64 { return quarantineOf(c) }

// Wanted：配置里有没有要这个角色开写（characters 或 summarize 认它，名单里的 "*" 也认）。没有的角色不检查、不记日志，和以前一样只读
func (in Input) Wanted() bool {
	return in.Cfg.Remembers(in.Char) || in.Cfg.Summarizes(in.Char)
}

func (in Input) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in Input) logf(format string, args ...any) {
	if in.Logf != nil {
		in.Logf(format, args...)
	}
}

func (in Input) backupDir() string {
	if d := strings.TrimSpace(in.BackupDir); d != "" {
		return d
	}
	return in.BackupFallback
}

// plan 是决定的结果：开哪几档、没开的原因、备份做了什么
type plan struct {
	remember, summarize   bool
	reason                string // 两档都没开的原因
	summarizeReason       string // remember 开了、整理没开的原因（整理没被要求时为空）
	backupWrite, backupSu string // 备份文件名，或"已有"、"以前做过"；没做是空
	firstWrite, firstSu   bool   // 这次是首次开写 / 首次开整理（memory_meta 里还没有）
}

func readOnly(reason string) plan { return plan{reason: reason} }

// Open 决定并打开这个角色的记忆。open 是装配处给的、用 memory.Open 打开的函数（带上所有别的选项），这里只管写入那几个字段（Settings.Apply）。
// 角色没被要求开写：原样以只读打开，不检查、不记日志。要求了：按上面的前提检查，两档都不开的情形以只读打开并记原因，只关整理的情形 remember 照开（见文件头）。
// 返回的错误只来自只读打开也失败（和没有写入功能时一样）。
// 配置了止血隔离（quarantineSince）的话，不论开没开写，打开成功后都记一行回执：线、藏起了多少条（logQuarantine）
func Open(ctx context.Context, in Input, open func(Settings) (*memory.Memory, error)) (*memory.Memory, error) {
	m, err := openDecided(ctx, in, open)
	if err == nil && m != nil {
		in.logQuarantine(ctx, m)
	}
	return m, err
}

// logQuarantine：止血隔离的回执，应急开关得让人看得出管没管用。只写计数和线，不写正文、不写路径。
// 一条都没藏：线可能填晚了（事故之后的时间）；线早于开写时间：导入的旧记忆里不是主人放行的也一起藏了（填成了更早的时间）
func (in Input) logQuarantine(ctx context.Context, m *memory.Memory) {
	r := m.QuarantineReport()
	if !r.Active {
		return
	}
	text := fmt.Sprintf("记忆[%s]：止血隔离已生效（线 %d）：藏起 %d 条事实、%d 条待确认、%d 条片段", in.Char, r.Since, r.Facts, r.Candidates, r.Episodes)
	if r.Facts+r.Candidates+r.Episodes == 0 {
		text += "；一条都没藏，线可能填晚了（应该填整理开始的时间，见“写入已开”那一行）"
	}
	if v, ok, err := memory.MetaValue(ctx, in.DB, memory.MetaWriteEnabledAt); err == nil && ok && r.Since < v {
		text += "；线早于开写时间，导入的旧记忆里不是你亲口放行的也会被藏"
	}
	in.logf("%s", text)
}

func openDecided(ctx context.Context, in Input, open func(Settings) (*memory.Memory, error)) (*memory.Memory, error) {
	ro := Settings{WritePlaces: placesOf(in.Cfg), QuarantineSince: quarantineOf(in.Cfg)}
	if !in.Wanted() {
		// 显式列了别的角色、这个没列：说一声为什么它没开。名单全空（全只读）或 "*"（全开）都不吵
		if len(in.Cfg.Characters)+len(in.Cfg.Summarize) > 0 {
			in.logf("记忆[%s]：写入保持只读（原因 %s）", in.Char, ReasonNotListed)
		}
		return open(ro)
	}
	p := in.decide(ctx)
	for {
		s := in.settings(p)
		m, err := open(s)
		if err != nil {
			if !s.Writable {
				if p.reason != "" {
					in.logf("记忆[%s]：写入保持只读（原因 %s）", in.Char, p.reason)
				}
				return nil, err
			}
			// 以可写方式打开失败（别的程序占着库、文件坏了……）：退回只读，说原因。备份已经做了，库没改
			in.logf("记忆[%s]：写入保持只读（原因 %s）", in.Char, ReasonOpenFailed)
			p = readOnly(ReasonOpenFailed)
			continue
		}
		if !s.Writable {
			in.logf("记忆[%s]：写入保持只读（原因 %s）", in.Char, p.reason)
			return m, nil
		}
		stage, err := in.finish(ctx, m, &p)
		if err == nil {
			in.logf("记忆[%s]：写入已开（库 go/characters/%s/memory.db，备份 %s，整理 %s，场合 %v%s）", in.Char, in.Char, p.backupText(), p.summarizeText(), s.WritePlaces, in.timesText(ctx, m))
			return m, nil
		}
		// 开写后的收尾出错：关掉这次打开，按出错的那一档往下退一档再开一次
		_ = m.Close()
		switch stage {
		case "summarize":
			in.logf("记忆[%s]：后台整理没能开（原因 %s），remember 照开", in.Char, ReasonCatchUpFailed)
			p.summarize, p.summarizeReason = false, ReasonCatchUpFailed
		case "summarize_meta":
			in.logf("记忆[%s]：后台整理没能开（原因 %s），remember 照开", in.Char, ReasonSummarizeMeta)
			p.summarize, p.summarizeReason = false, ReasonSummarizeMeta
		default:
			in.logf("记忆[%s]：写入保持只读（原因 %s）", in.Char, ReasonMetaFailed)
			p = readOnly(ReasonMetaFailed)
		}
	}
}

func placesOf(c config.MemoryWrite) []string {
	if c.Places == nil {
		return []string{}
	}
	return slices.Clone(c.Places)
}

func (in Input) settings(p plan) Settings {
	s := Settings{Writable: p.remember, SummarizeEnabled: p.remember && p.summarize, WritePlaces: placesOf(in.Cfg),
		ConfirmOccasions: in.Cfg.ConfirmOccasions, ConfirmKinds: in.Cfg.ConfirmKinds, QuarantineSince: quarantineOf(in.Cfg)}
	s.AutoConfirm = s.SummarizeEnabled && in.Cfg.AutoConfirm
	return s
}

func (p plan) backupText() string {
	switch {
	case p.backupWrite != "" && p.backupSu != "" && p.backupWrite != p.backupSu:
		return p.backupWrite + "、" + p.backupSu
	case p.backupWrite != "":
		return p.backupWrite
	case p.backupSu != "":
		return p.backupSu
	}
	return "以前做过"
}

func (p plan) summarizeText() string {
	switch {
	case p.summarize:
		return "开"
	case p.summarizeReason != "":
		return "关（原因 " + p.summarizeReason + "）"
	}
	return "关"
}

// timesText 把库里记的两个开写时间（毫秒）读回来，接在"写入已开"那一行后面。止血的 quarantineSince 照这一行填：
// 整理开过填 summarize_enabled_at，只开过 remember 填 write_enabled_at。读不出来就不写，不影响开写
func (in Input) timesText(ctx context.Context, m *memory.Memory) string {
	var text string
	for _, k := range []string{memory.MetaWriteEnabledAt, memory.MetaSummarizeEnabledAt} {
		if v, ok, err := m.Meta(ctx, k); err == nil && ok {
			text += fmt.Sprintf("，%s=%d", k, v)
		}
	}
	return text
}

// finish 在以可写方式打开之后收尾：记开写时间、整理的 catchUp=latest（整理没开就清空 catchUp 的记录）。
// 出错时 stage 说是哪一步出的：write = 开写这一档（退回只读）；summarize = catchUp 推游标；summarize_meta = 开整理时间记不进库（后两个只关整理）
func (in Input) finish(ctx context.Context, m *memory.Memory, p *plan) (stage string, err error) {
	now := in.now().UnixMilli()
	if _, err := m.SetMetaIfAbsent(ctx, memory.MetaWriteEnabledAt, now); err != nil {
		return "write", err
	}
	if !p.summarize {
		// 整理这次没开：把 catchUp=latest 记下的"已处理的取值"清空，以后再开整理像第一次一样再推一遍，
		// 不会把停整理期间的聊天接着整理（memory.ResetCatchUp）。清不掉就退回只读，和开写时间记不进库一样处理
		if err := m.ResetCatchUp(ctx); err != nil {
			return "write", err
		}
		return "", nil
	}
	// catchUp 成功之后才记开整理时间：它是止血隔离线，要记整理真正打开的时刻；catchUp 出错整理没开，不留这个键
	if in.Cfg.CatchUp == "latest" {
		if _, err := m.CatchUpToLatest(ctx); err != nil {
			return "summarize", err
		}
	}
	if _, err := m.SetMetaIfAbsent(ctx, memory.MetaSummarizeEnabledAt, now); err != nil {
		return "summarize_meta", err
	}
	return "", nil
}

// decide 做前提检查和备份。返回的 plan 里 remember / summarize 是这次真的开几档
func (in Input) decide(ctx context.Context) plan {
	cfg := in.Cfg
	if !cfg.Remembers(in.Char) {
		if cfg.Summarizes(in.Char) {
			return readOnly(ReasonSummarizeNoWrite)
		}
		return readOnly(ReasonNotListed)
	}
	pre := in.Precheck
	if pre <= 0 {
		pre = PrecheckTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, pre)
	defer cancel()
	if in.precheckStarted != nil {
		in.precheckStarted(pctx)
	}

	// 前提 2：库在角色记忆目录下且没有链接
	if err := paths.CheckMemoryPath(in.Data, in.DB); err != nil {
		return readOnly(checkReason(pctx, err, ReasonPathError, ReasonNotGoCopy))
	}
	// 前提 3：已迁移（只读查，不迁移）
	if err := memory.CheckSchema(pctx, in.DB); err != nil {
		if errors.Is(err, memory.ErrNeedsMigration) {
			return readOnly(ReasonNotMigrated)
		}
		return readOnly(checkReason(pctx, err, ReasonSchemaError, ""))
	}
	// 首次开写 / 首次开整理：memory_meta 里有没有记过
	_, wrote, err := memory.MetaValue(pctx, in.DB, memory.MetaWriteEnabledAt)
	if err != nil {
		return readOnly(checkReason(pctx, err, ReasonSchemaError, ""))
	}
	_, summarized, err := memory.MetaValue(pctx, in.DB, memory.MetaSummarizeEnabledAt)
	if err != nil {
		return readOnly(checkReason(pctx, err, ReasonSchemaError, ""))
	}
	p := plan{remember: true, firstWrite: !wrote, firstSu: !summarized}

	// 前提 5（只管整理）先看，免得不开整理还白做一份备份
	wantSu := cfg.Summarizes(in.Char)
	switch {
	case !wantSu:
	case !in.HasModel:
		p.summarizeReason = ReasonNoBackgroundModel
	case cfg.CatchUp != "latest" && cfg.CatchUp != "cursor":
		p.summarizeReason = ReasonCatchUpNotChosen
	default:
		p.summarize = true
	}

	// 前提 4：备份，做两次、各只做一次
	bctx, bcancel := context.WithTimeout(ctx, in.backupTimeout())
	defer bcancel()
	if in.BackupDirErr != nil && (p.firstWrite || (p.summarize && p.firstSu)) {
		in.logf("记忆 %s：开写前的备份没地方放（备份目录算不出来，原因见备份模块日志）", in.Char)
		if p.firstWrite {
			return readOnly(ReasonBackupFailed)
		}
		p.summarize, p.summarizeReason = false, ReasonSummarizeBackup
		return p
	}
	dir := in.backupDir()
	if p.firstWrite {
		r, err := memory.BackupMemoryDB(bctx, in.DB, dir, in.Char, memory.BackupBeforeWrite, in.now())
		if err != nil {
			return readOnly(ReasonBackupFailed)
		}
		p.backupWrite = backupName(r)
	}
	if p.summarize && p.firstSu {
		r, err := memory.BackupMemoryDB(bctx, in.DB, dir, in.Char, memory.BackupBeforeSummarize, in.now())
		if err != nil {
			p.summarize, p.summarizeReason = false, ReasonSummarizeBackup
		} else {
			p.backupSu = backupName(r)
		}
	}
	return p
}

func (in Input) backupTimeout() time.Duration {
	if in.Backup > 0 {
		return in.Backup
	}
	return BackupTimeout
}

func backupName(r memory.BackupResult) string {
	if r.Existed {
		return "已有"
	}
	return filepath.Base(r.File)
}

// checkReason：检查超时是 precheck_timeout；notGoCopy 不为空且错误是 ErrMemoryPath 用它；别的用 other
func checkReason(ctx context.Context, err error, other, notGoCopy string) string {
	switch {
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return ReasonPrecheckTimeout
	case notGoCopy != "" && errors.Is(err, paths.ErrMemoryPath):
		return notGoCopy
	}
	return other
}
