package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, s string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "sumika.yml")
	if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDefaultsWithoutFile(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", filepath.Join(t.TempDir(), "没有这个文件.yml"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Console.Port != 7720 || c.Magpie.BaseURL != "http://127.0.0.1:3425/v1" || c.Agent.Thinking.Local != "medium" || c.Agent.Thinking.Group != "low" || c.Defaults.BackgroundThinking != "low" {
		t.Fatalf("默认值和 daemon/config.ts 不一样：%+v", c)
	}
}

func TestOverridesAndUnknownKeys(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, `
console: { port: 7999 }
characters:
  a: { model: m-a, groupReminder: true }
agent:
  thinking: { local: high }
qq: { accounts: [] }
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Console.Port != 7999 || c.Characters["a"].Model != "m-a" || !c.Characters["a"].GroupReminder || c.Agent.Thinking.Local != "high" || c.Agent.Thinking.Private != "low" {
		t.Fatalf("覆盖不对：%+v", c)
	}
}

// agent.context（规格-Go后台第七节 T21）：默认值和 daemon/config.ts 一样；只写几项时别的照默认；范围不对报出是哪一项
func TestAgentContext(t *testing.T) {
	ctx := Defaults().Agent.Context
	if ctx.Window != 0 || ctx.Reserve != 16_384 || ctx.SoftRatio != 0.7 || ctx.HardRatio != 0.85 || ctx.KeepTokens != 20_000 || ctx.TopicCompactAt != 200_000 || !ctx.AutoCompactChat {
		t.Fatalf("默认值和 daemon/config.ts 不一样：%+v", ctx)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { context: { window: 200000, autoCompactChat: false } }\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ctx := c.Agent.Context; ctx.Window != 200_000 || ctx.AutoCompactChat || ctx.KeepTokens != 20_000 || ctx.SoftRatio != 0.7 {
		t.Fatalf("只写的两项覆盖、别的照默认：%+v", ctx)
	}
	for key, bad := range map[string]string{"window": "500", "softRatio": "1.5", "hardRatio": "0", "keepTokens": "10", "topicCompactAt": "100", "reserve": "-1"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "agent: { context: { "+key+": "+bad+" } }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent.context."+key) {
			t.Errorf("%s: %s 应该报出是哪一项，得到 %v", key, bad, err)
		}
	}
}

// 实例回收的两项（agent.maxLive、agent.idleMinutes）：默认 8 个、45 分钟，范围照 zod
func TestAgentRecycle(t *testing.T) {
	if a := Defaults().Agent; a.MaxLive != 8 || a.IdleMinutes != 45 {
		t.Fatalf("默认值和 daemon/config.ts 不一样：%d %v", a.MaxLive, a.IdleMinutes)
	}
	for key, bad := range map[string]string{"maxLive": "0", "idleMinutes": "0"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "agent: { "+key+": "+bad+" }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent."+key) {
			t.Errorf("%s: %s 应该报出是哪一项，得到 %v", key, bad, err)
		}
	}
}

// 本地私聊空闲压缩（agent.idleCompact，ADR-388）：默认开、50 分钟、200000；只写几项时别的照默认；范围不对报出是哪一项
func TestAgentIdleCompact(t *testing.T) {
	if a := Defaults().Agent.IdleCompact; !a.Enabled || a.After != 50 || a.MinTokens != 200_000 {
		t.Fatalf("默认值不对：%+v", a)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { idleCompact: { enabled: false, after: 20 } }\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Agent.IdleCompact; a.Enabled || a.After != 20 || a.MinTokens != 200_000 {
		t.Fatalf("只写的两项覆盖、minTokens 照默认：%+v", a)
	}
	for key, bad := range map[string]string{"after": "0", "minTokens": "0"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "agent: { idleCompact: { "+key+": "+bad+" } }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent.idleCompact."+key) {
			t.Errorf("%s: %s 应该报出是哪一项，得到 %v", key, bad, err)
		}
	}
	// 下限必须大于保留原文的 2 倍：等于 2 倍也不行，错误里要写清原因
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { context: { keepTokens: 10000 }, idleCompact: { minTokens: 20000 } }\n"))
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent.idleCompact.minTokens") || !strings.Contains(err.Error(), "2 倍") {
		t.Errorf("minTokens 等于 keepTokens 的 2 倍应该报错并写清原因，得到 %v", err)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { context: { keepTokens: 10000 }, idleCompact: { minTokens: 20001 } }\n"))
	if _, err := Load(t.TempDir()); err != nil {
		t.Errorf("minTokens 大于 keepTokens 的 2 倍应该通过，得到 %v", err)
	}
}

// 模型调用的内容空闲时限：默认 180 秒，0 是不管（显式写 0 不能被默认值盖掉），负数报错
func TestAgentContentIdle(t *testing.T) {
	if a := Defaults().Agent; a.FirstContentSeconds != 180 || a.ContentIdleSeconds != 180 {
		t.Fatalf("默认应该都是 180 秒：%v %v", a.FirstContentSeconds, a.ContentIdleSeconds)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { firstContentSeconds: 0, contentIdleSeconds: 600 }\n"))
	c, err := Load(t.TempDir())
	if err != nil || c.Agent.FirstContentSeconds != 0 || c.Agent.ContentIdleSeconds != 600 {
		t.Fatalf("显式的 0 和 600 应该留着：%v %+v", err, c.Agent)
	}
	for _, key := range []string{"firstContentSeconds", "contentIdleSeconds"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "agent: { "+key+": -1 }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent."+key) {
			t.Errorf("%s: 负数应该报出是哪一项，得到 %v", key, err)
		}
	}
}

func TestBadThinking(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, "agent: { thinking: { group: huge } }\n"))
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent.thinking.group") {
		t.Fatalf("不认识的思考档位应该报出是哪一项，得到 %v", err)
	}
}

func TestFileAndDataDirOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SUMIKA_CONFIG", "")
	t.Setenv("SUMIKA_DATA_DIR", "")
	if got := File(root); got != filepath.Join(root, "sumika.yml") {
		t.Fatalf("没有搬过的老位置：%s", got)
	}
	moved := filepath.Join(root, "data", "config", "sumika.yml")
	os.MkdirAll(filepath.Dir(moved), 0o755)
	os.WriteFile(moved, []byte("{}"), 0o644)
	if got := File(root); got != moved {
		t.Fatalf("数据目录里的应该优先：%s", got)
	}
	other := t.TempDir()
	t.Setenv("SUMIKA_DATA_DIR", other)
	if got := Defaults().DataDirIn(root); got != other {
		t.Fatalf("SUMIKA_DATA_DIR 应该优先：%s", got)
	}
}

// 规格-Go后台装配第八节第 1 步：读项目根的示例配置，qq、group、embed 三段每个键读进来的值和示例里写的一致
func TestExampleQQGroupEmbed(t *testing.T) {
	example := filepath.Join("..", "..", "..", "sumika.example.yml")
	if _, err := os.Stat(example); err != nil {
		t.Fatalf("找不到示例配置：%v", err)
	}
	t.Setenv("SUMIKA_CONFIG", example)
	c, err := Load(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if c.QQ.Owner != "" || len(c.QQ.Accounts) != 0 || len(c.QQ.Groups) != 0 || len(c.QQ.PrivateAllow) != 0 || c.QQ.Reply || c.QQ.GoSend || c.QQ.ProtocolDir != "" {
		t.Fatalf("qq 和示例里写的不一样：%+v", c.QQ)
	}
	if c.Proactive.Enabled || len(c.Proactive.Characters) != 0 || c.Proactive.Send != "live" || c.Proactive.BaseMinMinutes != 120 || c.Proactive.Sleep.Start != "01:00" || c.Proactive.Sleep.End != "09:00" || c.Proactive.DecisionLogDays != 90 {
		t.Fatalf("proactive 和示例里写的不一样：%+v", c.Proactive)
	}
	if g := c.Group; g.Frequency != 0.3 || g.Jitter != 0.03 || g.Mode != "dry" {
		t.Fatalf("group 和示例里写的不一样：%+v", g)
	}
	// 示例里 backend 和 model 是配套的一对：go-models 加 gte-multilingual-base（规格-Go本地模型 2.8：代码里没有默认模型，"嵌入换 gte"落在这一行）；
	// 其余本机路径都不写。不能是 legacy 加 gte：旧桥只认自己的名字，照抄示例的人会得到一个每隔一阵被杀一次重起的旧桥
	if e := c.Embed; !e.Enabled || e.Model != "gte-multilingual-base" || e.Backend != "go-models" || e.ModelsRoot != "" || e.ModelsExe != "" || e.OrtLibrary != "" {
		t.Fatalf("embed 和示例里写的不一样：%+v", e)
	}
	if r := c.Memory.Rerank; r.Model != "" || r.K != 40 || r.TimeoutSeconds != 5 {
		t.Fatalf("memory.rerank 和示例里写的不一样：%+v", r)
	}
	if l := c.LinkReader; !l.Context.Enabled || l.Proxy != "" {
		t.Fatalf("linkReader 和示例里写的不一样：%+v", l)
	}
}

// linkReader 关掉、写代理都读得进来；不写是开着（ADR-192）
func TestLinkReaderConfig(t *testing.T) {
	if !Defaults().LinkReader.Context.Enabled {
		t.Fatal("给她看默认开")
	}
	t.Setenv("SUMIKA_CONFIG", write(t, `
linkReader:
  context: { enabled: false }
  proxy: http://127.0.0.1:9
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.LinkReader.Context.Enabled || c.LinkReader.Proxy != "http://127.0.0.1:9" {
		t.Fatalf("%+v", c.LinkReader)
	}
}

// 三段里每一项写了都读得进来（示例里大多和默认值一样，另外拿一份全写的对一遍）
func TestQQGroupEmbedOverrides(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, `
qq:
  owner: 100
  accounts:
    - { id: 200, character: a, url: ws://127.0.0.1:3001, token: 明文令牌 }
    - { id: 300, character: b }
  groups: [400]
  privateAllow: [500]
  reply: true
  goSend: true
  qqDir: D:/QQ
  qqDataDir: D:/QQ/Tencent Files
  protocolDir: D:/SnowLuma
  protocolLogDir: D:/SnowLuma/logs
  media: { download: false }
group: { frequency: 0.5, jitter: 0.1, mode: speak }
embed: { enabled: false, python: python.exe, serverDir: D:/Embed, model: m, backend: go-models, modelsRoot: D:/Models, modelsExe: D:/Sumika/sumika-models.exe, ortLibrary: D:/Sumika/onnxruntime.dll }
memory: { rerank: { model: 编的重排模型, k: 20, timeoutSeconds: 2.5 } }
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Memory.Rerank; r.Model != "编的重排模型" || r.K != 20 || r.TimeoutSeconds != 2.5 {
		t.Fatalf("memory.rerank 读错了：%+v", r)
	}
	q := c.QQ
	if q.Owner != "100" || len(q.Accounts) != 2 || q.Accounts[0].ID != "200" || q.Accounts[0].Character != "a" ||
		q.Accounts[0].URL != "ws://127.0.0.1:3001" || q.Accounts[0].Token != "明文令牌" || q.Accounts[1].ID != "300" ||
		q.Groups[0] != "400" || q.PrivateAllow[0] != "500" || !q.Reply || !q.GoSend ||
		q.QQDir != "D:/QQ" || q.QQDataDir != "D:/QQ/Tencent Files" || q.ProtocolDir != "D:/SnowLuma" || q.ProtocolLogDir != "D:/SnowLuma/logs" {
		t.Fatalf("qq 读错了：%+v", q)
	}
	if q.Media.Download {
		t.Fatalf("qq.media.download 写了 false 却读成 true：%+v", q.Media)
	}
	if g := c.Group; g.Frequency != 0.5 || g.Jitter != 0.1 || g.Mode != "speak" {
		t.Fatalf("group 读错了：%+v", g)
	}
	if e := c.Embed; e.Enabled || e.Model != "m" || e.Backend != "go-models" ||
		e.ModelsRoot != "D:/Models" || e.ModelsExe != "D:/Sumika/sumika-models.exe" || e.OrtLibrary != "D:/Sumika/onnxruntime.dll" {
		t.Fatalf("embed 读错了：%+v", e)
	}
	// 已删的两个键读得进来、不失败，记下来给启动时警告一次（规格-嵌入不依赖外部程序 第 2 条）
	if !slices.Equal(c.RemovedKeys, []string{"embed.python", "embed.serverDir"}) {
		t.Fatalf("已删键该记下来：%v", c.RemovedKeys)
	}
}

// 没有 qq 段：零值，不报错（规格第八节第 1 步）
func TestNoQQSection(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, "console: { port: 7720 }\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.QQ.Owner != "" || c.QQ.Accounts != nil || c.QQ.Groups != nil || c.QQ.PrivateAllow != nil || c.QQ.Reply || c.QQ.GoSend {
		t.Fatalf("没有 qq 段应该是零值：%+v", c.QQ)
	}
}

// QQ 号写成数字和写成带引号的字符串读出来一样，大号不丢精度（照 daemon/config.ts 的 qqId）
func TestQQIDNumberOrString(t *testing.T) {
	for _, id := range []string{"100", `"100"`, "'100'"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "qq:\n  owner: "+id+"\n  accounts:\n    - { id: "+id+", character: a }\n  groups: ["+id+"]\n"))
		c, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		a := c.QQ.Accounts[0]
		if c.QQ.Owner != "100" || a.ID != "100" || c.QQ.Groups[0] != "100" {
			t.Fatalf("%s 读成了 %q %q %q", id, c.QQ.Owner, a.ID, c.QQ.Groups[0])
		}
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "qq:\n  owner: 4294967296\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.QQ.Owner != "4294967296" {
		t.Fatalf("大号丢了精度：%q", c.QQ.Owner)
	}
}

// qq 段的校验照 Bun：号必须是数字串、同一个号不能出现两次、账号绑的角色不能为空，报出是哪一项
func TestQQCheck(t *testing.T) {
	cases := map[string]struct{ yml, want string }{
		"号不是数字":     {"qq: { owner: abc }\n", "qq.owner"},
		"账号号不是数字":   {"qq:\n  accounts:\n    - { id: abc, character: a }\n", "qq.accounts[0].id"},
		"号重复":       {"qq:\n  accounts:\n    - { id: 100, character: a }\n    - { id: '100', character: b }\n", "出现了两次"},
		"角色为空":      {"qq:\n  accounts:\n    - { id: 100 }\n", "qq.accounts[0].character"},
		"白名单不是数字":   {"qq: { groups: [100, abc] }\n", "qq.groups[1]"},
		"私聊白名单不是数字": {"qq: { privateAllow: [abc] }\n", "qq.privateAllow[0]"},
	}
	for name, tc := range cases {
		t.Setenv("SUMIKA_CONFIG", write(t, tc.yml))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：应该报出 %q，得到 %v", name, tc.want, err)
		}
	}
}

// group 的三项：默认值照 zod；范围和档位不对报出是哪一项
func TestGroupCheck(t *testing.T) {
	g := Defaults().Group
	if g.Frequency != 0.3 || g.Jitter != 0.03 || g.Mode != "dry" {
		t.Fatalf("默认值和 daemon/config.ts 不一样：%+v", g)
	}
	for key, bad := range map[string]string{"frequency": "1.5", "jitter": "0.3", "mode": "huge"} {
		t.Setenv("SUMIKA_CONFIG", write(t, "group: { "+key+": "+bad+" }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "group."+key) {
			t.Errorf("%s: %s 应该报出是哪一项，得到 %v", key, bad, err)
		}
	}
}

// 给闸门的转换（native/internal/channel/gate.go 的 GateConfig）：jitter 0 是"不抖"、频率 0 要另写规则
func TestGateConversions(t *testing.T) {
	if got := Defaults().Group.GateJitter(); got != 0.03 {
		t.Fatalf("默认抖动：%v", got)
	}
	if got := (Group{Jitter: 0}).GateJitter(); got >= 0 {
		t.Fatalf("jitter 0 应该转成负数（闸门的 0 是用默认）：%v", got)
	}
	if got := (Group{Jitter: 0.1}).GateJitter(); got != 0.1 {
		t.Fatalf("非 0 的抖动照给：%v", got)
	}
	if v, rule := (Group{Frequency: 0.3}).GateFrequency(); v != 0.3 || rule {
		t.Fatalf("非 0 的频率照给：%v %v", v, rule)
	}
	if v, rule := (Group{Frequency: 0}).GateFrequency(); v != 0 || !rule {
		t.Fatalf("频率 0 要告诉装配处写 gate_rules：%v %v", v, rule)
	}
}

// 令牌里的 ${变量}：照 daemon/config.ts 的 expandEnv，变量没设（或者空）就报错；读配置时不展开，连接时才展开（照 Bun）
func TestTokenExpandEnv(t *testing.T) {
	t.Setenv("SUMIKA_TEST_TOKEN", "编的令牌")
	t.Setenv("SUMIKA_CONFIG", write(t, "qq:\n  accounts:\n    - { id: 100, character: a, token: \"${SUMIKA_TEST_TOKEN}\" }\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := c.QQ.Accounts[0].Token
	if raw != "${SUMIKA_TEST_TOKEN}" {
		t.Fatalf("读配置时不该展开（照 Bun，连接时再展开）：%q", raw)
	}
	got, err := ExpandEnv(raw, nil)
	if err != nil || got != "编的令牌" {
		t.Fatalf("设了变量应该换成值：%q %v", got, err)
	}
	t.Setenv("SUMIKA_TEST_TOKEN", "")
	if _, err := ExpandEnv(raw, nil); err == nil || !strings.Contains(err.Error(), "SUMIKA_TEST_TOKEN") {
		t.Fatalf("没设变量应该报出变量名，得到 %v", err)
	}
	if got, err := ExpandEnv("明文令牌", nil); err != nil || got != "明文令牌" {
		t.Fatalf("没有变量的原样返回：%q %v", got, err)
	}
}

// QQ 号按 YAML 的类型认（照 daemon/config.ts 第 8 行的 qqId：正整数或者 /^\d+$/ 的字符串）：大于 2^53 的逐位
// 保留、不经过 float64；Bun 里 1e2、100.0 都是 100；0、不是整数、布尔、列表、映射报错
// （显式 null 这一版不管：goccy 不把 null 交给 UnmarshalYAML，和"没写"一样是零值）
func TestQQIDYAMLTypes(t *testing.T) {
	ok := map[string]string{
		"9007199254740993":   "9007199254740993",
		`"9007199254740993"`: "9007199254740993",
		"'9007199254740993'": "9007199254740993",
		"1e2":                "100",
		"100.0":              "100",
		"4294967296":         "4294967296",
		`"100"`:              "100",
		// 不带引号的整数按十进制看（goccy 把前导零当八进制，Bun 的 yaml 按 1.2 读成 100）
		"0100":  "100",
		"00100": "100",
		"+100":  "100",
		"0x64":  "100",
		"0o144": "100",
		// 带引号的照字符串留（check 认它是数字串）
		`"0100"`: "0100",
	}
	for src, want := range ok {
		t.Setenv("SUMIKA_CONFIG", write(t, "qq:\n  owner: "+src+"\n  accounts:\n    - { id: "+src+", character: a }\n  groups: ["+src+"]\n"))
		c, err := Load(t.TempDir())
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if string(c.QQ.Owner) != want || string(c.QQ.Accounts[0].ID) != want || string(c.QQ.Groups[0]) != want {
			t.Errorf("%s 读成了 %q %q %q，应该都是 %q", src, c.QQ.Owner, c.QQ.Accounts[0].ID, c.QQ.Groups[0], want)
		}
	}
	bad := map[string]string{
		"0":                    "正整数",
		"-1":                   "正整数",
		"-0100":                "正整数",
		"1.5":                  "正整数",
		"1e300":                "正整数",
		"1_000":                "数字或数字串",
		"true":                 "数字或数字串",
		"[100]":                "数字或数字串",
		"{a: 1}":               "数字或数字串",
		"99999999999999999999": "超出范围",
	}
	for src, want := range bad {
		t.Setenv("SUMIKA_CONFIG", write(t, "qq: { owner: "+src+" }\n"))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s 应该报出 %q，得到 %v", src, want, err)
		}
	}
}

// 令牌里的 ${变量}：遇到第一个没设的就停，报那一个（A），后面的不再去读（照 expandEnv 在替换回调里直接抛）
func TestExpandEnvStopsAtFirstMissing(t *testing.T) {
	var asked []string
	getenv := func(name string) string {
		asked = append(asked, name)
		return ""
	}
	if _, err := ExpandEnv("${A}-${B}", getenv); err == nil || !strings.Contains(err.Error(), "A") || strings.Contains(err.Error(), "B") {
		t.Fatalf("两个都没设应该只报 A，得到 %v", err)
	}
	if len(asked) != 1 || asked[0] != "A" {
		t.Fatalf("第一个没设以后不该再读后面的：读了 %v", asked)
	}
	asked = nil
	got, err := ExpandEnv("x${A}y${B}z", func(name string) string {
		asked = append(asked, name)
		if name == "A" {
			return "编的值"
		}
		return ""
	})
	if got != "" || err == nil || !strings.Contains(err.Error(), "B") || strings.Contains(err.Error(), "A") {
		t.Fatalf("前一个设了、后一个没设应该报 B，得到 %q %v", got, err)
	}
	if len(asked) != 2 || asked[0] != "A" || asked[1] != "B" {
		t.Fatalf("应该按原文顺序读 A、B：读了 %v", asked)
	}
	if got, err := ExpandEnv("x${A}y${B}z", func(name string) string {
		return map[string]string{"A": "1", "B": "2"}[name]
	}); err != nil || got != "x1y2z" {
		t.Fatalf("都设了应该全展开：%q %v", got, err)
	}
}

// 嵌入后端默认 go-models（空也当它）；legacy 已移除但旧配置要读得进来，交给嵌入启动处报原因。重排的 K、时限有范围。
// 写错了启动时就报，不带着错值往下走
func TestEmbedBackendAndRerankChecks(t *testing.T) {
	d := Defaults()
	if d.Embed.Backend != "go-models" || d.Memory.Rerank.Model != "" || d.Memory.Rerank.K != 40 || d.Memory.Rerank.TimeoutSeconds != 5 {
		t.Fatalf("默认值不对（后端 go-models、重排关、K 40、时限 5 秒）：%+v %+v", d.Embed, d.Memory.Rerank)
	}
	for _, tc := range []struct {
		name, yaml, want string // want 空 = 该通过
	}{
		{"空后端当 go-models", "embed: { backend: '' }", ""},
		{"go-models", "embed: { backend: go-models }", ""},
		{"go-models 不写路径也不报错（没配 = 嵌入不启动，由 Local 说原因）", "embed: { backend: go-models, model: gte-multilingual-base }", ""},
		{"legacy 旧配置读得进来（由嵌入启动处报已移除）", "embed: { backend: legacy }", ""},
		{"拼错的 go-models", "embed: { backend: go_models }", "embed.backend"},
		{"拼错的后端", "embed: { backend: Legacy }", "embed.backend"},
		{"K 是 0", "memory: { rerank: { k: 0 } }", "memory.rerank.k"},
		{"K 超过服务一次的条数", "memory: { rerank: { k: 65 } }", "memory.rerank.k"},
		{"K 在 1 和 64 的边上", "memory: { rerank: { k: 1 } }", ""},
		{"K 在 64 的边上", "memory: { rerank: { k: 64 } }", ""},
		{"时限是 0", "memory: { rerank: { timeoutSeconds: 0 } }", "memory.rerank.timeoutSeconds"},
		{"时限是负数", "memory: { rerank: { timeoutSeconds: -1 } }", "memory.rerank.timeoutSeconds"},
		{"时限大得溢出", "memory: { rerank: { timeoutSeconds: 1.0e12 } }", "memory.rerank.timeoutSeconds"},
		{"时限不是数", "memory: { rerank: { timeoutSeconds: .nan } }", "memory.rerank.timeoutSeconds"},
		{"时限 600 秒", "memory: { rerank: { timeoutSeconds: 600 } }", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUMIKA_CONFIG", write(t, tc.yaml+"\n"))
			_, err := Load(t.TempDir())
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("该通过：%v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("该报 %s：%v", tc.want, err)
			}
		})
	}
}

// 记忆写入的配置键（规格-记忆写入上线 W5 验收 3、ADR-377）：默认值、读得进来、不认的取值被拒
func TestMemoryWriteConfigDefaults(t *testing.T) {
	w := Defaults().Memory.Write
	if !slices.Equal(w.Characters, []string{"*"}) || len(w.Summarize) != 0 || w.CatchUp != "" || w.AutoConfirm || w.QuarantineSince != "" {
		t.Fatalf("characters 默认该是 [*]（remember 全开）、summarize 默认空、catchUp 无默认：%+v", w)
	}
	if !w.Remembers("invented") || w.Summarizes("invented") {
		t.Fatalf("默认配置该让任意角色的 remember 可写、后台整理仍关：%+v", w)
	}
	if len(w.Places) != 1 || w.Places[0] != "owner-private" {
		t.Fatalf("places 默认该只有 owner-private：%v", w.Places)
	}
	if w.ConfirmOccasions != 3 || len(w.ConfirmKinds) != 2 || w.ConfirmKinds[0] != "fact" || w.ConfirmKinds[1] != "preference" {
		t.Fatalf("confirmOccasions 默认 3、confirmKinds 默认 [fact, preference]：%+v", w)
	}
	if err := Defaults().check(); err != nil {
		t.Fatalf("默认配置该通过自己的检查：%v", err)
	}
}

// 示例配置里 memory.write 的每个键都读得进来，值和默认一致（示例里写的是默认值，不替人把开关打开）
func TestExampleMemoryWrite(t *testing.T) {
	example := filepath.Join("..", "..", "..", "sumika.example.yml")
	t.Setenv("SUMIKA_CONFIG", example)
	c, err := Load(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"characters", "summarize", "places", "catchUp", "autoConfirm", "confirmOccasions", "confirmKinds", "quarantineSince"} {
		if !c.Present["memory.write."+key] {
			t.Errorf("示例配置里没有 memory.write.%s", key)
		}
	}
	d := Defaults().Memory.Write
	w := c.Memory.Write
	if !slices.Equal(w.Characters, []string{"*"}) || len(w.Summarize) != 0 || w.CatchUp != "" || w.AutoConfirm || w.QuarantineSince != "" ||
		!slices.Equal(w.Places, d.Places) || w.ConfirmOccasions != d.ConfirmOccasions || !slices.Equal(w.ConfirmKinds, d.ConfirmKinds) {
		t.Fatalf("示例里写的该和默认值一样：%+v", w)
	}
}

func TestMemoryWriteConfigParses(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, `
memory:
  write:
    characters: [suzu, kiri]
    summarize: [suzu]
    places: [owner-private, platform-group]
    catchUp: latest
    autoConfirm: true
    confirmOccasions: 4
    confirmKinds: [fact, promise]
    quarantineSince: "1762041600000"
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := c.Memory.Write
	if !slices.Equal(w.Characters, []string{"suzu", "kiri"}) || !slices.Equal(w.Summarize, []string{"suzu"}) || !slices.Equal(w.Places, []string{"owner-private", "platform-group"}) ||
		w.CatchUp != "latest" || !w.AutoConfirm || w.ConfirmOccasions != 4 || !slices.Equal(w.ConfirmKinds, []string{"fact", "promise"}) || w.QuarantineSince != "1762041600000" {
		t.Fatalf("读出来不对：%+v", w)
	}
	// 空名单（places: []）是"什么场合都不整理"，不是默认值
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    places: []\n"))
	c, err = Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Memory.Write.Places == nil || len(c.Memory.Write.Places) != 0 {
		t.Fatalf("places: [] 该读成非 nil 的空名单：%#v", c.Memory.Write.Places)
	}
	// 写了键没写值（YAML 的 null）等于没写，留着默认值；要"什么场合都不整理"就写 places: []
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    places:\n"))
	c, err = Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Memory.Write.Places, Defaults().Memory.Write.Places) {
		t.Fatalf("places 写了键没写值该留着默认值：%#v", c.Memory.Write.Places)
	}
}

func TestMemoryWriteStarAndExplicitEmpty(t *testing.T) {
	// 显式空名单：全部只读，和没写（默认 [*]）不是一回事
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    characters: []\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Memory.Write.Remembers("invented") || c.Memory.Write.Summarizes("invented") {
		t.Fatalf("characters: [] 该全部只读：%+v", c.Memory.Write)
	}
	// summarize 也认 *（characters 仍是默认的 *，两者不冲突）
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    summarize: [\"*\"]\n"))
	c, err = Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Memory.Write.Remembers("invented") || !c.Memory.Write.Summarizes("invented") {
		t.Fatalf("summarize: [*] 该让所有角色都能整理：%+v", c.Memory.Write)
	}
	// 显式列名字：只有列了的角色开，别的仍只读
	w := MemoryWrite{Characters: []string{"a"}, Summarize: []string{"a"}}
	if !w.Remembers("a") || !w.Summarizes("a") || w.Remembers("b") || w.Summarizes("b") {
		t.Fatalf("显式名单只该开列了的角色：%+v", w)
	}
}

// 显式写了键但值是 null（`characters:` 后面留空、`~`）按空名单处理，不能掉回默认的 ["*"]（复核 2026-11-16）
func TestMemoryWriteExplicitNullIsEmpty(t *testing.T) {
	for _, tc := range []struct{ name, yml string }{
		{"characters 留空", "memory:\n  write:\n    characters:\n"},
		{"characters 波浪号", "memory:\n  write:\n    characters: ~\n"},
		{"characters 空列表", "memory:\n  write:\n    characters: []\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUMIKA_CONFIG", write(t, tc.yml))
			c, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			w := c.Memory.Write
			if w.Remembers("invented") || w.Summarizes("invented") {
				t.Fatalf("%s 该按空名单、全部只读：%+v", tc.name, w)
			}
			if w.Characters == nil || len(w.Characters) != 0 {
				t.Fatalf("%s 该读成非 nil 的空名单：%#v", tc.name, w.Characters)
			}
		})
	}
	// 键没写：characters 用默认 ["*"]，remember 全开
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    places: [owner-private]\n"))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Memory.Write.Remembers("invented") || c.Memory.Write.Summarizes("invented") {
		t.Fatalf("键没写该用默认 [*]，整理仍关：%+v", c.Memory.Write)
	}
	// summarize 留空：不整理（characters 仍用默认的 *）
	t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    summarize:\n"))
	c, err = Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Memory.Write.Remembers("invented") || c.Memory.Write.Summarizes("invented") {
		t.Fatalf("summarize 留空该不整理、remember 照默认全开：%+v", c.Memory.Write)
	}
	if c.Memory.Write.Summarize == nil || len(c.Memory.Write.Summarize) != 0 {
		t.Fatalf("summarize 留空该读成非 nil 的空名单：%#v", c.Memory.Write.Summarize)
	}
}

func TestMemoryWriteConfigRejects(t *testing.T) {
	for _, c := range []struct{ name, yml, want string }{
		{"未知场合", "places: [everywhere]", "memory.write.places"},
		{"场合写了两遍", "places: [owner-private, owner-private]", "写了两遍"},
		{"未知种类", "confirmKinds: [lie]", "memory.write.confirmKinds"},
		{"catchUp 不认识", "catchUp: yes", "memory.write.catchUp"},
		{"catchUp 大小写", "catchUp: Latest", "memory.write.catchUp"},
		{"证据次数 1", "confirmOccasions: 1", "memory.write.confirmOccasions"},
		{"证据次数 0", "confirmOccasions: 0", "memory.write.confirmOccasions"},
		{"证据次数负数", "confirmOccasions: -3", "memory.write.confirmOccasions"},
		{"隔离时间不是数字", "quarantineSince: yesterday", "memory.write.quarantineSince"},
		{"隔离时间是 0", "quarantineSince: \"0\"", "memory.write.quarantineSince"},
		{"隔离时间是负数", "quarantineSince: \"-5\"", "memory.write.quarantineSince"},
		{"隔离时间填成了秒（位数不够）", "quarantineSince: \"12345678\"", "像是秒"},
		{"隔离时间差一位", "quarantineSince: \"999999999999\"", "像是秒"},
		{"角色 id 带路径", "characters: [\"../x\"]", "memory.write.characters"},
		{"角色 id 带斜杠", "characters: [\"a/b\"]", "memory.write.characters"},
		{"角色 id 带冒号", "summarize: [\"a:b\"]", "memory.write.summarize"},
		{"角色 id 是空串", "characters: [\"\"]", "memory.write.characters"},
		{"角色写了两遍", "characters: [suzu, suzu]", "写了两遍"},
		{"characters 混写 * 和名字", "characters: [\"*\", suzu]", "要只开几个就去掉 *"},
		{"summarize 混写 * 和名字", "summarize: [\"*\", suzu]", "要只开几个就去掉 *"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SUMIKA_CONFIG", write(t, "memory:\n  write:\n    "+c.yml+"\n"))
			_, err := Load(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("该拒绝并点名 %s，得到 %v", c.want, err)
			}
		})
	}
}

// qq.media.download 默认开；配置里写了别的、没写 media 这一段、写了空的 media，都不该把它悄悄关掉
func TestQQMediaDownloadDefaultsOn(t *testing.T) {
	if !Defaults().QQ.Media.Download {
		t.Fatal("默认值该是开")
	}
	for name, yml := range map[string]string{"没写": "qq: { reply: true }\n", "空的 media": "qq: { media: {} }\n", "写了 true": "qq: { media: { download: true } }\n"} {
		t.Setenv("SUMIKA_CONFIG", write(t, yml))
		c, err := Load(t.TempDir())
		if err != nil || !c.QQ.Media.Download {
			t.Fatalf("%s：%v %+v", name, err, c.QQ.Media)
		}
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "qq: { media: { download: false } }\n"))
	if c, err := Load(t.TempDir()); err != nil || c.QQ.Media.Download {
		t.Fatalf("写了 false 就该关：%v %+v", err, c.QQ.Media)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "qq: { media: { ffmpeg: 'custom-ffmpeg', ffprobe: 'custom-ffprobe' } }\n"))
	if c, err := Load(t.TempDir()); err != nil || c.QQ.Media.FFmpeg != "custom-ffmpeg" || c.QQ.Media.FFprobe != "custom-ffprobe" {
		t.Fatalf("视频配置未读入：%v %+v", err, c.QQ.Media)
	}
}

// 配置文件的位置：SUMIKA_CONFIG → 数据目录里的 config/sumika.yml（存在才算）→ 项目根的 sumika.yml。
// Bun 那边的同一张表在 server/test/backend-config.test.ts 的“配置文件的位置”（复核 R7：两边的回退位置要一样）
func TestFileInOrder(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	rootFile := filepath.Join(root, "sumika.yml")
	dataFile := filepath.Join(data, "config", "sumika.yml")
	t.Setenv("SUMIKA_CONFIG", "")
	t.Setenv("SUMIKA_DATA_DIR", "")
	if got, err := FileIn(root, data); err != nil || got != rootFile {
		t.Fatalf("数据目录里没有：该落到项目根的：%q %v", got, err)
	}
	os.MkdirAll(filepath.Dir(dataFile), 0o755)
	os.WriteFile(dataFile, []byte("{}"), 0o644)
	if got, err := FileIn(root, data); err != nil || got != dataFile {
		t.Fatalf("数据目录里有就用它：%q %v", got, err)
	}
	other := filepath.Join(t.TempDir(), "别处.yml")
	t.Setenv("SUMIKA_CONFIG", other)
	if got, err := FileIn(root, data); err != nil || got != other {
		t.Fatalf("SUMIKA_CONFIG 压过两个位置：%q %v", got, err)
	}
}
