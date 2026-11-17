package approval

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"

	"sumika-native/internal/jsstr"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// ───────────── 主人的回答（tools.ts 的 pending / decide / settle、grants.ts 的 GrantStore） ─────────────

// Grants 是"始终允许"（data/config/tool-grants.yml，grants.ts GrantStore）：启动时读一次，加一条就整份写回。
// 文件里别的字段、手写的条目原样留着（只认 key 是字符串的那些）
type Grants struct {
	writeMu sync.Mutex // 写文件串行，Has 不等磁盘
	mu      sync.Mutex
	file    string
	items   []yaml.MapSlice
	// LoadErr：读文件出了问题（不是"文件不存在"）。宿主据此写日志、发提醒。
	// 读不了（权限、占用）时 noWrite 为真：不知道原内容，就不写回，免得把原有的始终允许整份盖掉；
	// 格式坏了的已改名留存（文件名写在 LoadErr 里），之后照常从空开始写。
	LoadErr error
	noWrite bool
	// savedAt 是上一次写文件的时刻（毫秒）：最近用过的时间只隔一阵才落盘，不为每次放行写一次文件
	savedAt int64
	dirty   bool
	// BadRules：规则存储里写错、被跳过的规则（原因一条一条）。宿主据此发提醒：写错的拒绝规则不会生效。
	BadRules []string
}

// touchSaveEvery 是最近用过的时间最多隔多久写一次文件；退出时 Flush 再写一次。
const touchSaveEvery = int64(time.Hour / time.Millisecond)

// Rule 是一条放行规则，"记住"和"这个对话"共用，设置页列的就是它（规格-审批分级第七节）。
type Rule struct {
	Scope        string // always（记住）/ conversation（这个对话）
	Key          string
	Label        string
	Tool         string
	Guard        string // 这条规则认的目标版本（MCP：服务绑定加参数 schema）；对不上就不算数，再问一次
	Effect       string // 主人自己写的规则才有：allow / ask / deny；点"记住"记下的是空
	Pattern      string // 主人自己写的规则的原文，如 Bash(git status:*)；点"记住"记下的是空
	Project      string // 主人自己写的 allow、ask 规则的作用范围：只对这个项目目录；空是不限
	Session      string // 同上：只对这个会话；空是不限
	Conversation string
	Workdir      string   // 这个对话：在哪个工作目录里点的；会话换了目录就不再算数
	Dirs         []string // 这个对话：当时批准的外部目录
	At           int64    // 记下的时刻，Unix 毫秒
	LastUsed     int64    // 最近一次靠它放行的时刻；0 是还没用过
}

const grantsHeader = "# 审批里点了\"记住\"的操作（规格-审批分级）。在设置 · 工具与审批里撤销，也可以直接删掉某一条\n"

// LoadGrants 读"始终允许"文件。没有文件 = 没有授权；读不了、格式坏了都当没有授权，但写进 LoadErr、不悄悄覆盖原文件
func LoadGrants(file string) *Grants {
	g := &Grants{file: file}
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return g
	}
	if err != nil {
		g.LoadErr, g.noWrite = fmt.Errorf("读不了始终允许的记录，这次运行里不会改写它：%w", err), true
		return g
	}
	var raw struct {
		Grants []yaml.MapSlice `yaml:"grants"`
	}
	if perr := yaml.Unmarshal(b, &raw); perr != nil {
		kept := fmt.Sprintf("%s.坏-%d", file, time.Now().UnixMilli())
		if rerr := os.Rename(file, kept); rerr != nil {
			g.LoadErr, g.noWrite = fmt.Errorf("始终允许的记录格式坏了，也没能改名留存（%v），这次运行里不会改写它：%w", rerr, perr), true
			return g
		}
		g.LoadErr = fmt.Errorf("始终允许的记录格式坏了，已改名留存为 %s，原有的始终允许需要重新点：%w", filepath.Base(kept), perr)
		return g
	}
	for _, x := range raw.Grants {
		// 手写的规则可以只写 pattern（和 effect），不用自己编 key
		if p := strField(x, "pattern"); p != "" && keyOf(x) == "" {
			project, _ := scopeFieldOf(x, "project")
			session, _ := scopeFieldOf(x, "session")
			x = setField(x, "key", policy.ScopedRuleKey(effectOf(x), p, project, session))
		}
		for _, kv := range x {
			if k, _ := kv.Key.(string); k == "key" {
				if _, ok := kv.Value.(string); ok {
					g.items = append(g.items, x)
				}
				break
			}
		}
	}
	g.BadRules = g.badRules()
	return g
}

// userRules 解析规则存储里主人自己写的放行、拒绝规则（有 pattern 字段的条目）；写错的跳过，另见 badRules。
func (g *Grants) userRules() ([]policy.UserRule, []string) {
	g.mu.Lock()
	items := g.items
	g.mu.Unlock()
	var rules []policy.UserRule
	var bad []string
	for _, x := range items {
		p := strField(x, "pattern")
		if p == "" {
			continue
		}
		eff := effectOf(x)
		project, badP := scopeFieldOf(x, "project")
		session, badS := scopeFieldOf(x, "session")
		if eff == policy.EffectDeny {
			project, session, badP, badS = "", "", false, false // 拒绝不分范围：手写带了范围也当全局拦（多拦，不漏拦）
		}
		var r policy.UserRule
		var err error
		if badP || badS { // 范围字段不是文字（数字、列表之类）：认不出写的是什么，按"范围写坏"处理
			err = fmt.Errorf("规则 %q 的 project、session 要写成文字", p)
		} else {
			r, err = policy.ParseScopedRule(p, eff, project, session)
		}
		if err != nil {
			// 范围写坏了：ask 规则宁可当全局（多问），allow 规则整条不要（多问），deny 本来不分范围
			if eff == policy.EffectAsk {
				if g, err2 := policy.ParseUserRule(p, eff); err2 == nil {
					bad = append(bad, err.Error()+"（这条每次都问的规则先当全局生效）")
					g.Key = keyOf(x)
					rules = append(rules, g)
					continue
				}
			}
			bad = append(bad, err.Error())
			continue
		}
		r.Key = keyOf(x) // 以存储里的 key 为准（撤销、留痕都认它）
		rules = append(rules, r)
	}
	return rules, bad
}

func (g *Grants) badRules() []string { _, bad := g.userRules(); return bad }

// UserRules 是主人自己写的放行、拒绝规则，按写下的先后；写错的不在里面（BadRules 里有）。
func (g *Grants) UserRules() []policy.UserRule { r, _ := g.userRules(); return r }

// PutRule 加一条主人自己写的全局规则（allow、ask、deny）：先按写法校验，写错了说清楚，不写进去。
// 同一原文同一效果只留一条；同一原文又有相反效果的不替换（并存，拒绝先于 ask 先于放行）。
func (g *Grants) PutRule(raw, effect string, at int64) (policy.UserRule, error) {
	return g.PutScopedRule(raw, effect, "", "", at)
}

// PutScopedRule 同 PutRule，allow、ask 还可以限定只对某个项目目录（绝对路径）、某个会话生效；
// 同一原文在不同范围里是各自独立的规则（key 带范围）。拒绝不分范围，带范围会报错。
func (g *Grants) PutScopedRule(raw, effect, project, session string, at int64) (policy.UserRule, error) {
	r, err := policy.ParseScopedRule(raw, effect, project, session)
	if err != nil {
		return r, err
	}
	return r, g.Put(Rule{Key: r.Key, Label: r.Raw, Tool: "rule", At: at, Effect: effect, Pattern: r.Raw, Project: r.Project, Session: r.Session})
}

// scopeFieldOf 读 project、session：没写是空；写了但不是文字（手写成数字、列表）返回 bad，调用方按"范围写坏"处理。
func scopeFieldOf(x yaml.MapSlice, name string) (string, bool) {
	v := fieldOf(x, name)
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return strings.TrimSpace(s), !ok
}

// scopedAllowEffect 是带范围的 allow 写进文件时的 effect：旧版本不认这个值，会把整条丢掉（多问），
// 免得换回旧 exe 以后带范围的放行悄悄变成全局放行。读回来时当 allow。
const scopedAllowEffect = "allow-scoped"

// effectOf 是条目的效果：没写当 allow，大小写不分，allow-scoped 当 allow。认不出的原样留给解析去报错。
func effectOf(x yaml.MapSlice) string {
	eff := strings.ToLower(strings.TrimSpace(strField(x, "effect")))
	switch eff {
	case "", scopedAllowEffect:
		return policy.EffectAllow
	}
	return eff
}

func keyOf(x yaml.MapSlice) string {
	s, _ := fieldOf(x, "key").(string)
	return s
}

func fieldOf(x yaml.MapSlice, name string) any {
	for _, kv := range x {
		if k, _ := kv.Key.(string); k == name {
			return kv.Value
		}
	}
	return nil
}

func strField(x yaml.MapSlice, name string) string {
	s, _ := fieldOf(x, name).(string)
	return s
}

// intField 手写、读回来的数字类型不一定，认不出当 0。
func intField(x yaml.MapSlice, name string) int64 {
	switch v := fieldOf(x, name).(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// setField 换一条的某个字段，别的字段原样留着；items 里的条目当不可变的用，这里返回新的。
func setField(x yaml.MapSlice, name string, value any) yaml.MapSlice {
	out := make(yaml.MapSlice, 0, len(x)+1)
	done := false
	for _, kv := range x {
		if k, _ := kv.Key.(string); k == name {
			kv.Value, done = value, true
		}
		out = append(out, kv)
	}
	if !done {
		out = append(out, yaml.MapItem{Key: name, Value: value})
	}
	return out
}

// Has 是有没有这一类（不看目标版本）
func (g *Grants) Has(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, x := range g.items {
		if keyOf(x) == key {
			return true
		}
	}
	return false
}

// Match 是这一类记住过、认的目标版本也对得上。没有版本的（命令、写文件、子任务）guard 传空。
func (g *Grants) Match(key, guard string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, x := range g.items {
		if keyOf(x) == key {
			return strField(x, "guard") == guard
		}
	}
	return false
}

// save 整份写回：先写临时文件再改名。要拿着 writeMu 调。
func (g *Grants) save(next []yaml.MapSlice) error {
	if g.noWrite {
		return fmt.Errorf("没有写入：%w", g.LoadErr)
	}
	b, err := yaml.MarshalWithOptions(yaml.MapSlice{{Key: "grants", Value: next}}, yaml.Indent(2), yaml.IndentSequence(true))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.file), 0o755); err != nil {
		return err
	}
	return writeAtomic(g.file, append([]byte(grantsHeader), b...))
}

func (g *Grants) snapshot() []yaml.MapSlice {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.items
}

// Put 照 GrantStore.add：没有这一类才加；有了但目标版本变了就换成新的，一类只留一条。写成了才算加上：写失败时内存里也不留
// （不然本进程当成已授权、再点一次直接返回成功不写文件，重启就没了）。
// 写出来的格式是 goccy/go-yaml 的，和 Bun 的 yaml 包在引号上可能不一样，读回来一样（规格第七节 A34）
func (g *Grants) Put(r Rule) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	items := g.snapshot()
	entry := yaml.MapSlice{{Key: "key", Value: r.Key}, {Key: "label", Value: r.Label}, {Key: "tool", Value: r.Tool}, {Key: "at", Value: r.At}}
	if r.Guard != "" {
		entry = append(entry, yaml.MapItem{Key: "guard", Value: r.Guard})
	}
	if r.Pattern != "" {
		eff := r.Effect
		if eff == policy.EffectAllow && (r.Project != "" || r.Session != "") {
			eff = scopedAllowEffect
		}
		entry = append(entry, yaml.MapItem{Key: "pattern", Value: r.Pattern}, yaml.MapItem{Key: "effect", Value: eff})
		if r.Project != "" {
			entry = append(entry, yaml.MapItem{Key: "project", Value: r.Project})
		}
		if r.Session != "" {
			entry = append(entry, yaml.MapItem{Key: "session", Value: r.Session})
		}
	}
	next := append([]yaml.MapSlice(nil), items...)
	found := false
	for i, x := range items {
		if keyOf(x) == r.Key {
			if strField(x, "guard") == r.Guard {
				return nil
			}
			next[i], found = entry, true
			break
		}
	}
	if !found {
		next = append(next, entry)
	}
	if err := g.save(next); err != nil {
		return err
	}
	g.mu.Lock()
	g.items, g.savedAt, g.dirty = next, r.At, false
	g.mu.Unlock()
	return nil
}

// Remove 撤销一条：内存里立刻没了，下一次判定就要问。写文件失败报出来（这次运行里已撤销，重启后会回来）；
// 没有这一条返回 false。
func (g *Grants) Remove(key string) (bool, error) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	items := g.snapshot()
	next := make([]yaml.MapSlice, 0, len(items))
	for _, x := range items {
		if keyOf(x) != key {
			next = append(next, x)
		}
	}
	if len(next) == len(items) {
		return false, nil
	}
	g.mu.Lock()
	g.items = next
	g.mu.Unlock()
	if err := g.save(next); err != nil {
		return true, fmt.Errorf("这次运行里已经撤销，但没写进文件，重启后这一条会回来：%w", err)
	}
	return true, nil
}

// Touch 记下这一类刚被用来放行。内存里马上变；写文件隔一阵才写一次，写不进去等下一次，不影响放行。
func (g *Grants) Touch(key string, at int64) {
	g.mu.Lock()
	next := append([]yaml.MapSlice(nil), g.items...)
	hit := false
	for i, x := range next {
		if keyOf(x) == key {
			next[i], hit = setField(x, "lastUsed", at), true
			break
		}
	}
	if !hit {
		g.mu.Unlock()
		return
	}
	g.items, g.dirty = next, true
	due := at-g.savedAt >= touchSaveEvery
	g.mu.Unlock()
	if due && g.writeMu.TryLock() { // 正在写就不等，这一步在放行的路上
		defer g.writeMu.Unlock()
		g.flushLocked(at)
	}
}

// Flush 把还没落盘的最近用过时间写下去（退出时调）。
func (g *Grants) Flush() error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	return g.flushLocked(0)
}

func (g *Grants) flushLocked(at int64) error {
	g.mu.Lock()
	items, dirty := g.items, g.dirty
	g.mu.Unlock()
	if !dirty {
		return nil
	}
	if err := g.save(items); err != nil {
		return err
	}
	g.mu.Lock()
	g.dirty = false
	if at > 0 {
		g.savedAt = at
	}
	g.mu.Unlock()
	return nil
}

// List 是所有记住的规则，按记下的先后。
func (g *Grants) List() []Rule {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Rule, 0, len(g.items))
	for _, x := range g.items {
		r := Rule{Scope: policy.ByAlways, Key: keyOf(x), Label: strField(x, "label"), Tool: strField(x, "tool"), Guard: strField(x, "guard"), At: intField(x, "at"), LastUsed: intField(x, "lastUsed")}
		if p := strField(x, "pattern"); p != "" {
			r.Pattern, r.Effect = p, effectOf(x)
			r.Project, _ = scopeFieldOf(x, "project")
			r.Session, _ = scopeFieldOf(x, "session")
			if r.Label == "" {
				r.Label = p
			}
		}
		out = append(out, r)
	}
	return out
}

// writeFile 是 os.WriteFile（测试换成写一半就失败的，模拟写到一半进程被杀）
var writeFile = os.WriteFile

// writeAtomic 先写旁边的临时文件、写完再改名替换（表一 A9）：写到一半被杀，原来的文件原样留着。
// Bun 的 GrantStore 是整份覆盖写的，写到一半被杀会把文件截断，下次读不了就当没有、旧的授权全丢
func writeAtomic(file string, b []byte) error {
	tmp := file + ".tmp"
	if err := writeFile(tmp, b, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

// ConvGrants 是"这个对话里都允许"：会话 → 同一类的 key → 规则（本地私聊的会写进会话文件，重启后保留，见 session_state.go）。
// 授权记着是在哪个工作目录里点的：会话换了项目目录，旧项目里点的授权不再放行（按会话选工作目录，规格决定 6）。
// 不靠"换目录时清空"：清空管不到换目录前弹出、换目录后才点的卡，记目录就都管得到。
type ConvGrants struct {
	mu       sync.Mutex
	m        map[string]map[string]Rule
	onChange func() // 加了或撤了一条以后调（接了会话文件就写盘）；不持锁
}

// NewConvGrants 是空的
func NewConvGrants() *ConvGrants { return &ConvGrants{m: map[string]map[string]Rule{}} }

func sameWorkdir(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// Match 是这个会话在这个工作目录里这一类批准过、认的目标版本也对得上；给出当时批准的外部目录
func (c *ConvGrants) Match(conversation, workdir, key, guard string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[conversation][key]
	if !ok || r.Guard != guard || !sameWorkdir(r.Workdir, workdir) {
		return nil, false
	}
	return r.Dirs, true
}

// Has 是这个会话里这一类批准过没有（不看工作目录和目标版本）
func (c *ConvGrants) Has(conversation, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[conversation][key]
	return ok
}

// Merge 照 decide 里的"这个对话"：同一类再点一次，批准过的外部目录合在一起（先后照旧、去重）；
// 目标版本或工作目录变了就只认新的，不带旧项目的外部目录过来
func (c *ConvGrants) Merge(conversation string, r Rule) {
	c.merge(conversation, r)
	if c.onChange != nil && localPrivateKey(conversation) {
		c.onChange()
	}
}

func (c *ConvGrants) merge(conversation string, r Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[conversation] == nil {
		c.m[conversation] = map[string]Rule{}
	}
	old, had := c.m[conversation][r.Key]
	cur := []string{}
	if had && old.Guard == r.Guard && sameWorkdir(old.Workdir, r.Workdir) {
		cur = append(cur, old.Dirs...)
		r.At, r.LastUsed = old.At, old.LastUsed
	}
	for _, d := range r.Dirs {
		if !contains(cur, d) {
			cur = append(cur, d)
		}
	}
	r.Scope, r.Conversation, r.Dirs = policy.ByConversation, conversation, cur
	c.m[conversation][r.Key] = r
}

// Touch 记下这一类刚被用来放行
func (c *ConvGrants) Touch(conversation, key string, at int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.m[conversation][key]; ok {
		r.LastUsed = at
		c.m[conversation][key] = r
	}
}

// Remove 撤销一条，下一次判定就要问；没有这一条返回 false
func (c *ConvGrants) Remove(conversation, key string) bool {
	c.mu.Lock()
	if _, ok := c.m[conversation][key]; !ok {
		c.mu.Unlock()
		return false
	}
	delete(c.m[conversation], key)
	c.mu.Unlock()
	if c.onChange != nil && localPrivateKey(conversation) {
		c.onChange()
	}
	return true
}

// List 是所有"这个对话"的规则，按记下的先后，同时刻按会话、key 排（map 的顺序不定）
func (c *ConvGrants) List() []Rule {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Rule
	for _, rules := range c.m {
		for _, r := range rules {
			r.Dirs = append([]string(nil), r.Dirs...)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Rule) int {
		return cmp.Or(cmp.Compare(a.At, b.At), cmp.Compare(a.Conversation, b.Conversation), cmp.Compare(a.Key, b.Key))
	})
	return out
}

type pend struct {
	req          Request
	kind         *Pending
	done         chan struct{} // 授权事务定了，和回合停止等待分开
	result       Decision
	persisted    chan struct{}
	delivered    Decision
	notified     <-chan struct{}
	stopped      chan struct{}
	released     chan struct{} // 已认领的写入还没返回，回合立刻停止等待
	expired      chan struct{} // 只在卡片最终收成到期时关闭
	stop         Decision
	timer        *time.Timer
	claimed      bool
	fallback     string
	conversation bool   // 只在 Ask 真正放行时合并对话授权
	via          string // 回答从哪来（DecideVia 写）；只在认领时设一次
}

// Approvals 是等着主人回答的审批（tools.ts 的 pending）。
type Approvals struct {
	mu          sync.Mutex
	pending     map[string]*pend
	subscribers []*subscription
	requests    int
	resolved    int
	records     map[string]State
	finished    []string
	active      map[*pend]struct{} // 包含已移出 pending、仍在写库的卡片
	closing     bool
	Grants      *Grants
	Conv        *ConvGrants
	Now         func() time.Time
	// Persist 在审批自己的路径上记步骤，不经过通知队列；请求写成才发卡，批准写成才放行。
	// 正常库操作不设短时限，失败由 Ask 返回拒绝原因并记日志。
	Persist func(Request, *Decision) error
	// 每一次注册各自串行投递，不同注册互不阻塞。需要请求、结局先后顺序的用 Subscribe。
	// 在开始 Ask 之前注册；OnResolved 带原卡片，不依赖请求回调维护临时索引。
	OnRequest  []func(Request)
	OnResolved []func(Request, Decision)
	// Logf、Report：审批自己出错（写库、写授权）时记日志、提醒主人。nil = log.Printf / 不提醒
	Logf func(string, ...any)
	// Errorf 记 ERROR（审批没能建立、数据会丢）；nil 时用 Logf
	Errorf func(string, ...any)
	Report func(title, body string)
}

func (a *Approvals) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (a *Approvals) errorf(format string, args ...any) {
	if a.Errorf != nil {
		a.Errorf(format, args...)
		return
	}
	a.logf(format, args...)
}

func (a *Approvals) report(title, body string) {
	if a.Report != nil {
		a.Report(title, body)
	}
}

// NewApprovals 和 gate 共用"始终""这个对话"两份授权，gate 的 Ask 接到它上面
func NewApprovals(g *Gate) *Approvals {
	a := &Approvals{pending: map[string]*pend{}, Grants: g.Grants, Conv: g.Conv, Now: g.Now}
	g.Ask = a.Ask
	return a
}

// Ask 先记等待再发卡，结局写成后才允许工具执行。取消和到期最多等 250 毫秒收尾；
// 写库和已认领的授权事务继续完成，错误明确留在结局和日志里。
func (a *Approvals) Ask(ctx context.Context, req Request, kind *Pending) (result Decision) {
	req.Version = 1
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return Decision{Kind: "cancelled", Reason: "审批已经关闭"}
	}
	p := &pend{req: req, kind: kind, done: make(chan struct{}), persisted: make(chan struct{}), stopped: make(chan struct{}), released: make(chan struct{}), expired: make(chan struct{})}
	a.registerCallbacks()
	if a.active == nil {
		a.active = map[*pend]struct{}{}
	}
	a.active[p] = struct{}{}
	p.timer = time.AfterFunc(time.Duration(req.ExpiresAt-req.CreatedAt)*time.Millisecond, func() {
		a.expire(p)
	})
	a.pending[req.ID] = p
	a.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { a.settle(req.ID, "cancelled", "") })
	defer stop()
	go a.persistAndNotify(p)
	// 写成的结局优先于同时到达的停止信号；对话授权只跟随实际放行。
	defer func() {
		select {
		case <-p.persisted:
			result = p.delivered
		default:
		}
		if result.Kind == "approved" && ctx.Err() == nil && p.conversation {
			a.Conv.Merge(p.kind.Conversation, Rule{Key: p.kind.Key, Label: p.kind.Label, Tool: p.kind.Tool, Guard: p.kind.Guard, Workdir: p.kind.Workdir, Dirs: p.kind.Dirs, At: a.Now().UnixMilli()})
		}
	}()
	waitStop := Decision{}
	select {
	case <-p.released:
		return p.stop
	case <-p.persisted:
	case <-p.stopped:
		waitStop = p.stop
	case <-ctx.Done():
		a.settle(req.ID, "cancelled", "")
		waitStop = Decision{Kind: "cancelled"}
	case <-p.expired:
		waitStop = Decision{Kind: "expired"}
	}
	grace := time.NewTimer(approvalNotifyLimit)
	defer grace.Stop()
	if waitStop.Kind != "" {
		select {
		case <-p.released:
			return p.stop
		case <-p.persisted:
		case <-grace.C:
			return waitStop
		}
	}
	select {
	case <-p.released:
		return p.stop
	case <-ctx.Done():
		return Decision{Kind: "cancelled"}
	case <-p.expired:
		return Decision{Kind: "expired"}
	case <-p.notified:
	case <-grace.C:
	}
	if waitStop.Kind != "" {
		return waitStop
	}
	if ctx.Err() != nil {
		return Decision{Kind: "cancelled"}
	}
	select {
	case <-p.expired:
		return Decision{Kind: "expired"}
	default:
		return p.delivered
	}
}

func (a *Approvals) persistAndNotify(p *pend) {
	defer func() {
		p.timer.Stop()
		a.mu.Lock()
		delete(a.active, p)
		close(p.persisted)
		a.mu.Unlock()
	}()
	var persistErr error
	if a.Persist != nil {
		persistErr = a.Persist(p.req, nil)
	}
	if persistErr != nil {
		a.errorf("记录等待审批 %s 失败，审批卡没发出，按拒绝处理：%v", p.req.ID, persistErr)
		a.report("审批没能建立", "要等你批准的操作因为记录写不进库，审批卡没发出来，这一步按拒绝处理、没有执行。")
		a.settle(p.req.ID, "denied", "记录等待审批失败："+persistErr.Error())
	} else {
		a.publish(State{Request: p.req})
	}
	<-p.done
	d := p.result
	if persistErr != nil {
		d = Decision{Kind: "denied", Reason: "记录审批失败：" + persistErr.Error(), Via: p.result.Via} // 保留回答从哪来：QQ 要据此说真话
	}
	if d.Kind == "approved" && d.By != policy.ByOnce && p.kind != nil {
		d.Rule, d.Label = p.kind.Key, p.kind.Label
	}
	req := p.req
	req.Version++
	if a.Persist != nil {
		if err := a.Persist(req, &d); err != nil {
			persistErr = err
		}
	}
	if persistErr != nil {
		a.errorf("记录审批 %s 失败：%v", req.ID, persistErr)
		if p.result.Kind == "approved" {
			a.report("你的允许没有生效", "审批结果写不进库，这次已按拒绝处理、操作没有执行。")
		}
		d = Decision{Kind: "denied", Reason: "记录审批失败：" + persistErr.Error(), Via: p.result.Via} // 保留回答从哪来：QQ 要据此说真话
	}
	p.delivered = d
	p.notified = a.publish(State{Request: req, Decision: &d})
}

// Decide 照 tools.ts decide：主人点了按钮（once / conversation / always / deny，可带原因）。卡片上没给的档只当允许一次、不记授权；
// 找不到（已经超时、取消、回答过了）返回 false，迟到的批准不执行。
// 锁里认领、锁外写始终授权、再锁里提交；期间取消和到期放开回合，授权事务仍跟踪到最终结局。
// 始终授权写成仍补发批准；对话授权只在 Ask 放行且回合未停止时合并。不认识的选择不算批准。
func (a *Approvals) Decide(id, choice, reason string) (bool, error) {
	switch choice {
	case policy.ByOnce, policy.ByConversation, policy.ByAlways, "deny":
	default:
		return false, fmt.Errorf("不认识的审批选择：%q", choice)
	}
	a.mu.Lock()
	p := a.pending[id]
	if p == nil || p.claimed {
		a.mu.Unlock()
		return false, nil
	}
	if choice != "always" || p.kind == nil || !contains(p.req.Options, choice) {
		// 不写授权文件的决定在同一把锁内落定，不能在认领和提交之间被到期放开。
		decision, said := "approved", ""
		if choice == "deny" {
			decision, said = "denied", store.JSSlice(jsstr.Trim(reason), 200)
		}
		p.conversation = choice == "conversation" && p.kind != nil && contains(p.req.Options, choice)
		by := policy.ByOnce
		if p.conversation {
			by = policy.ByConversation
		}
		delete(a.pending, id)
		a.finish(p, decision, said, by)
		a.mu.Unlock()
		return true, nil
	}
	p.claimed = true
	a.mu.Unlock()
	if err := a.Grants.Put(Rule{Key: p.kind.Key, Label: p.kind.Label, Tool: p.kind.Tool, Guard: p.kind.Guard, At: a.Now().UnixMilli()}); err != nil {
		a.errorf("记录审批 %s 的始终允许失败：%v", id, err)
		a.report("始终允许没有记下", "这次的操作照常按你的回答处理，但\"始终允许\"没写进记录，下次还会问："+err.Error())
		a.mu.Lock()
		p.claimed = false
		fallback := p.fallback
		if fallback != "" {
			delete(a.pending, id)
		}
		a.mu.Unlock()
		if fallback != "" {
			a.finish(p, fallback, "记录始终允许失败："+err.Error(), "")
		}
		return false, err
	}
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
	a.finish(p, "approved", "", policy.ByAlways)
	return true, nil
}

// DecideVia 是不在本机主窗口的入口（主人 QQ 私聊）给的回答，只收 once 和 deny：
// 不记"这个对话""始终"，也不把不认识的 choice 当批准（Decide 对不认识的按批准算，这里不行）。
// 认领和落定在同一把锁里，和本机 Decide、到期、取消抢同一张卡，谁先到算谁；输了返回 false（已经有结果了）。
func (a *Approvals) DecideVia(id, choice, via string) (bool, error) {
	if via == "" {
		return false, fmt.Errorf("没有说明回答从哪来")
	}
	decision, by := "", ""
	switch choice {
	case "once":
		decision, by = "approved", policy.ByOnce
	case "deny":
		decision = "denied"
	default:
		return false, fmt.Errorf("%s 不能给出 %q：只能是 once 或 deny", via, choice)
	}
	a.mu.Lock()
	p := a.pending[id]
	if p == nil || p.claimed {
		a.mu.Unlock()
		return false, nil
	}
	p.via = via
	delete(a.pending, id)
	a.finish(p, decision, "", by)
	a.mu.Unlock()
	return true, nil
}

// Pending 是还在等的卡片
func (a *Approvals) Pending() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Request
	for _, p := range a.pending {
		out = append(out, p.req)
	}
	return out
}

// CancelAll 照插件关掉时：等着的都算取消
func (a *Approvals) CancelAll() {
	a.mu.Lock()
	var ids []string
	for id := range a.pending {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.settle(id, "cancelled", "")
	}
}

// Cancel 只取消这一张（tools.ts settle(id, 'cancelled')）；找不到（已经有结果了）返回 false
func (a *Approvals) Cancel(id string) bool { return a.settle(id, "cancelled", "") }

func (a *Approvals) expire(p *pend) {
	a.settle(p.req.ID, "expired", "")
}

// settle 照 tools.ts settle：在锁里取下来（同一张卡片只有一个结果），再通知、放行
func (a *Approvals) settle(id, decision, reason string) bool {
	a.mu.Lock()
	p := a.pending[id]
	if p == nil {
		a.mu.Unlock()
		return false
	}
	if p.claimed {
		if p.fallback == "" {
			p.fallback = decision
			p.stop = Decision{Kind: decision, Reason: reason}
			close(p.stopped)
			close(p.released)
		}
		a.mu.Unlock()
		return false
	}
	delete(a.pending, id)
	a.mu.Unlock()
	a.finish(p, decision, reason, "")
	return true
}

// finish 只提交事务结局；持久化和通知由该卡片的流程接着做。
func (a *Approvals) finish(p *pend, decision, reason, by string) {
	p.result = Decision{Kind: decision, Reason: reason, Via: p.via}
	if decision == "approved" {
		p.result.By = by
	}
	if decision == "expired" {
		close(p.expired)
	}
	if (decision == "cancelled" || decision == "expired") && p.fallback == "" {
		p.stop = p.result
		close(p.stopped)
	}
	close(p.done)
}

const approvalNotifyLimit = 250 * time.Millisecond

// Drain 关闭新审批，取消等待，并在关库前等授权写入、步骤持久化结束或明确失败。
// 返回尚未收尾的卡片数；ctx 到点后可以再次调用，通知回调不在等待范围内。
func (a *Approvals) Drain(ctx context.Context) (int, error) {
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()
	a.CancelAll()
	for {
		a.mu.Lock()
		remaining := len(a.active)
		var next <-chan struct{}
		for p := range a.active {
			next = p.persisted
			break
		}
		a.mu.Unlock()
		if remaining == 0 {
			return 0, nil
		}
		select {
		case <-next:
		case <-ctx.Done():
			a.mu.Lock()
			remaining = len(a.active)
			a.mu.Unlock()
			if remaining == 0 {
				return 0, nil
			}
			return remaining, ctx.Err()
		}
	}
}

// userRules 是这一刻规则存储里主人写的放行、拒绝规则，给判定用。
func (g *Gate) userRules() []policy.UserRule {
	if g.Grants == nil {
		return nil
	}
	return g.Grants.UserRules()
}
