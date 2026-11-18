package app

// 控制接口（桌面程序的 sumika-ctl，规格-本机控制接口 第二节第 6 条）用的群档位窄接口：列各群的档位、把一个群调成 off 或 dry。
// 后台不把 store.Set 交出去，只交这两个方法。"只许往不开口的方向调"在这里是第二道关（第一道在 internal/ctlserver 的参数校验）：
//   - 档位一字不差只认 off、dry，speak（让群真开口）无论怎么写都不行，留给主人在界面上亲手改；
//   - 会话只认 qq:group:<群号>，私聊、本地会话、通配符一律拒绝；
//   - 群得是已经认识的：QQ 库里有这个群的消息、群名表里有这个群、或者档位表里已经有它自己的一行；
//   - 只看历史（archive）模式的库是只读的，不写，说"先开始接收"。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 控制接口调档位的几种拒绝。服务端按错误种类给回复码（readonly、invalid），文字给人看
var (
	ErrGateReadOnly     = errors.New("现在是只看历史模式，库是只读的，先开始接收再调档位")
	ErrGateNotAllowed   = errors.New("这个档位不能通过控制接口调：只能调成 off（只记录）或 dry（试运行）")
	ErrGateBadGroup     = errors.New("会话要写完整的群会话身份 qq:group:<群号>")
	ErrGateUnknownGroup = errors.New("没有这个群：QQ 库里没有它的消息，群名表和档位表里也没有")
)

// GateRow 是 gate list 的一行
type GateRow struct {
	Conversation string
	Name         string // 群名表里的名字；没有是空
	Mode         string // off / dry / speak：这个群现在的档位设置
	Source       string // own 这个群自己的 / all 所有群的 / default 没设过，用配置里的默认
	Actual       string // 接收中才有：真正在用的档位（发送条件不齐时 speak 被压成 dry）；只看历史是空
}

// GateChange 是 LowerGate 的结果
type GateChange struct {
	Conversation string
	Before       string // 改之前这个群自己的设置；没设过是空
	After        string
}

// sqlGroupConversations 列出 events 里有消息的群会话（qq:group: 后面紧跟数字的，奇怪的形状留给后面的正则筛）。
// 不能写成 SELECT DISTINCT … WHERE conversation GLOB 'qq:group:[0-9]*'：那样要把范围里的每一条消息的索引项都读一遍，
// 实测 220 万条消息要 0.56～0.65 秒，而 gate list 是在宿主的 op 锁里跑的，这段时间托盘开始 / 停止接收和别的控制命令都得排队。
// 这里按会话号"跳着找"：每一步在 ix_events_conv 上找比上一个大的最小会话号，一共只查（群数 + 1）次，不随消息条数增长。
// 范围 ['qq:group:0', 'qq:group::') 就是 GLOB 的范围（':' 是 '9' 的下一个字符）
const sqlGroupConversations = `WITH RECURSIVE c(conv) AS (
  SELECT MIN(conversation) FROM events WHERE conversation >= 'qq:group:0' AND conversation < 'qq:group::'
  UNION ALL
  SELECT (SELECT MIN(conversation) FROM events WHERE conversation > c.conv AND conversation < 'qq:group::') FROM c WHERE c.conv IS NOT NULL
)
SELECT conv FROM c WHERE conv IS NOT NULL`

// 群会话的写法；和 internal/ctlserver 里那一道各写各的，两道关互不依赖
var strictGroupConv = regexp.MustCompile(`^qq:group:([0-9]{1,20})$`)

// gateAdmin 是真正干活的部分，和 *App 分开是为了测试能用临时库直接跑形状表（接收模式的 App 要假协议端，这里不需要）
type gateAdmin struct {
	set      *store.Set
	readonly bool                            // 只看历史：库是只读的
	def      string                          // 没设过档位的群用的默认档（off / dry / speak）
	actual   func(types.Conversation) string // 接收中真正生效的档位；nil = 不给
	now      func() time.Time
	logf     func(string, ...any)
}

// GateList 列各群的档位。archive 和 receive 都能用（只读）
func (a *App) GateList(ctx context.Context) ([]GateRow, error) { return a.gateAdmin().list(ctx) }

// LowerGate 把一个群的档位调成 off 或 dry
func (a *App) LowerGate(conversation, mode string) (GateChange, error) {
	return a.gateAdmin().lower(conversation, mode)
}

func (a *App) gateAdmin() *gateAdmin {
	g := &gateAdmin{set: a.set, readonly: a.opt.Mode != Receive, def: storedDefault(a.cfg.Group.Mode), now: a.now, logf: a.logfFor(applog.Channel)}
	if !g.readonly && a.modeOf != nil {
		g.actual = a.modeOf
	}
	return g
}

// storedDefault 是 group.mode 在 gate_modes 里的写法（和 storedMode 同一套取舍：不认识的按试运行）
func storedDefault(mode string) string {
	switch mode {
	case "off", "speak":
		return mode
	}
	return "dry"
}

func (g *gateAdmin) qq() (*store.Store, error) {
	st, ok := g.set.Platform("qq")
	if !ok {
		return nil, errors.New("QQ 的库没打开")
	}
	return st, nil
}

// known 这个群是不是"已经认识的"：消息、群名表、档位表三处有一处有就算
func (g *gateAdmin) known(ctx context.Context, st *store.Store, conv, gid string, modes map[string]string) (bool, error) {
	if _, ok := modes[conv]; ok {
		return true, nil
	}
	var one int
	err := st.Events.QueryRowContext(ctx, `SELECT 1 FROM events WHERE conversation = ? LIMIT 1`, conv).Scan(&one)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	names, err := groupNames(ctx, st)
	if err != nil {
		return false, err
	}
	_, ok := names[gid]
	return ok, nil
}

func (g *gateAdmin) list(ctx context.Context) ([]GateRow, error) {
	st, err := g.qq()
	if err != nil {
		return nil, err
	}
	modes, err := g.set.TraceStore().GateModeRows()
	if err != nil {
		return nil, fmt.Errorf("读档位表：%w", err)
	}
	names, err := groupNames(ctx, st)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	rows, err := st.Events.QueryContext(ctx, sqlGroupConversations)
	if err != nil {
		return nil, fmt.Errorf("列有消息的群：%w", err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		seen[c] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for gid := range names {
		seen["qq:group:"+gid] = true
	}
	for c := range modes { // 档位表里有、但还没消息也没群名的群（Bun 导入来的）
		if strictGroupConv.MatchString(c) {
			seen[c] = true
		}
	}
	all, hasAll := modes["*"]
	out := make([]GateRow, 0, len(seen))
	for conv := range seen {
		m := strictGroupConv.FindStringSubmatch(conv)
		if m == nil {
			continue // GLOB 放进来的奇怪形状（qq:group:123abc）不是合格的群会话
		}
		row := GateRow{Conversation: conv, Name: names[m[1]]}
		switch own, ok := modes[conv]; {
		case ok:
			row.Mode, row.Source = own, "own"
		case hasAll:
			row.Mode, row.Source = all, "all"
		default:
			row.Mode, row.Source = g.def, "default"
		}
		if g.actual != nil {
			row.Actual = displayMode(g.actual(types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: m[1]}))
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Conversation < out[j].Conversation })
	return out, nil
}

// displayMode 把出口认的三个值（record、dry-run、speak）写成 gate_modes 里的说法
func displayMode(m string) string {
	switch m {
	case channel.ModeSpeak:
		return "speak"
	case channel.ModeDryRun:
		return "dry"
	}
	return "off"
}

func (g *gateAdmin) lower(conv, mode string) (GateChange, error) {
	// 第二道关：服务端已经校验过一遍，这里再校验一遍，让谁也不能经由这个方法放宽
	if mode != "off" && mode != "dry" {
		return GateChange{}, ErrGateNotAllowed
	}
	m := strictGroupConv.FindStringSubmatch(conv)
	if m == nil {
		return GateChange{}, ErrGateBadGroup
	}
	if g.readonly {
		return GateChange{}, ErrGateReadOnly
	}
	st, err := g.qq()
	if err != nil {
		return GateChange{}, err
	}
	modes, err := g.set.TraceStore().GateModeRows()
	if err != nil {
		return GateChange{}, fmt.Errorf("读档位表：%w", err)
	}
	ok, err := g.known(context.Background(), st, conv, m[1], modes) // 写档位是短操作（索引查一行、小表读一遍），不带取消
	if err != nil {
		return GateChange{}, fmt.Errorf("查这个群在不在：%w", err)
	}
	if !ok {
		return GateChange{}, ErrGateUnknownGroup
	}
	before := modes[conv]
	if err := g.set.TraceStore().SetGateMode(conv, mode, g.now().UnixMilli()); err != nil {
		return GateChange{}, fmt.Errorf("写档位：%w", err)
	}
	// 谁改的：ctl。会话号经过 a.logf 会被打码
	g.logf("控制接口（ctl）把群 %s 的档位从「%s」调成「%s」", conv, orDefault(before), mode)
	return GateChange{Conversation: conv, Before: before, After: mode}, nil
}

func orDefault(m string) string {
	if m == "" {
		return "没设过"
	}
	return m
}

// groupNames 是群名表（qq_groups）：群号 → 名字。表不存在（很老的库）当没有
func groupNames(ctx context.Context, st *store.Store) (map[string]string, error) {
	rows, err := st.Events.QueryContext(ctx, `SELECT gid, name FROM qq_groups`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("读群名表：%w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var gid, name string
		if err := rows.Scan(&gid, &name); err != nil {
			return nil, err
		}
		out[gid] = name
	}
	return out, rows.Err()
}
