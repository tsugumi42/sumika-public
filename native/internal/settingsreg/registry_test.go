package settingsreg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/desksettings"
)

// Walk the schema independently of the catalog, including inline fields and
// concrete types nested in maps and slices.
func configLeaves(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			parts := strings.Split(f.Tag.Get("yaml"), ",")
			if parts[0] == "-" {
				continue
			}
			if len(parts) > 1 && parts[1] == "inline" {
				configLeaves(f.Type, prefix, out)
				continue
			}
			if parts[0] != "" {
				configLeaves(f.Type, join(prefix, parts[0]), out)
			}
		}
	case reflect.Map, reflect.Slice, reflect.Array:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct || elem.Kind() == reflect.Map || elem.Kind() == reflect.Slice {
			configLeaves(t.Elem(), join(prefix, "*"), out)
		} else {
			out[prefix] = true
		}
	default:
		out[prefix] = true
	}
}
func TestCatalogCoversConfig(t *testing.T) {
	leaves := map[string]bool{}
	configLeaves(reflect.TypeOf(config.Config{}), "", leaves)
	entries := Entries()
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Key] {
			t.Errorf("重复登记 %s", e.Key)
		}
		seen[e.Key] = true
		if e.Name == "" || e.Category == "" || e.Storage == "" || e.Type == "" || e.Effect == "" {
			t.Errorf("登记不完整: %+v", e)
		}
		if e.ReadOnly && e.Reason == "" {
			t.Errorf("缺只读原因: %s", e.Key)
		}
	}
	for k := range leaves {
		if !seen[k] {
			t.Errorf("未登记配置叶键 %s", k)
		}
	}
}
func byKey(t *testing.T, values []Value, key string) Value {
	t.Helper()
	for _, v := range values {
		if v.Key == key {
			return v
		}
	}
	t.Fatalf("缺少 %s", key)
	return Value{}
}
func TestReadValuesSourcesAndRedaction(t *testing.T) {
	c := config.Defaults()
	c.QQ.Reply = false
	c.QQ.Owner = ""
	c.Present = map[string]bool{"qq.reply": true, "qq.owner": true, "qq.accounts": true, "qq.accounts.0.url": true, "qq.accounts.0.token": true, "characters.anne.model": true, "characters.123": true}
	c.QQ.Accounts = []config.QQAccount{{Character: "anne", URL: "wss://user:password@example.test/ws?token=secret#frag", Token: "secret"}}
	c.Characters = map[string]config.CharacterModels{"anne": {Model: "model"}, "123": {}}
	values, err := Read(c, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, source string
		value       any
	}{{"qq.reply", "yml", false}, {"qq.owner", "yml", config.QQID("")}, {"qq.backfill.enabled", "默认", true}, {"qq.accounts.0.token", "yml", "已存"}, {"qq.accounts.0.url", "yml", "wss://example.test/ws"}, {"characters.anne.model", "yml", "model"}} {
		v := byKey(t, values, tc.key)
		if v.Source != tc.source || !reflect.DeepEqual(v.Value, tc.value) {
			t.Errorf("%s = %#v (%s)", tc.key, v.Value, v.Source)
		}
	}
	for _, key := range []string{"qq.accounts.0.character", "characters.123.model"} {
		if v := byKey(t, values, key); v.Source != "默认" {
			t.Errorf("%s 默认字段误标为 %s", key, v.Source)
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "secret") {
		t.Fatal("输出泄露秘密")
	}
	if v := byKey(t, values, "trace.debug"); v.Value != nil || !v.ReadOnly {
		t.Fatal("退役项不应有当前值")
	}
}
func TestDefaultsAndEmptyTemplates(t *testing.T) {
	values, err := Read(config.Defaults(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key   string
		value any
	}{{"qq.reply", false}, {"agent.context.window", 0}, {"qq.backfill.enabled", true}, {"characters.*.model", ""}, {"qq.accounts.*.token", "没存"}} {
		v := byKey(t, values, tc.key)
		if v.Source != "默认" || !reflect.DeepEqual(v.Value, tc.value) || !reflect.DeepEqual(v.Default, tc.value) {
			t.Errorf("%s: 值 %#v 默认 %#v 来源 %s", tc.key, v.Value, v.Default, v.Source)
		}
	}
	if _, err := Read(config.Defaults(), nil, filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	obstructed := filepath.Join(t.TempDir(), "characters")
	if err := os.WriteFile(obstructed, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(config.Defaults(), nil, obstructed); err == nil {
		t.Fatal("existing file was treated as an absent directory")
	}
}
func TestConstraintsAndEffect(t *testing.T) {
	entries := Entries()
	byEntry := map[string]Entry{}
	for _, e := range entries {
		byEntry[e.Key] = e
	}
	for _, tc := range []struct{ key, effect string }{{"plugins.sessions", "重启接收"}, {"reply.deny", "重启接收"}, {"linkReader.proxy", "重启接收"}, {"linkReader.expand.startEmoji", "立即"}, {"agent.maxLive", "重启接收"}, {"agent.context.window", "下一轮对话"}, {"linkReader.expand.enabled", "立即"}, {"desktop.appearance", "立即"}} {
		if got := byEntry[tc.key].Effect; got != tc.effect {
			t.Errorf("%s 生效时间 %s", tc.key, got)
		}
	}
	e := byEntry["group.frequency"]
	if e.Min == nil || e.Max == nil || *e.Min != 0 || *e.Max != 1 {
		t.Fatal("群聊频率范围错误")
	}
	if len(byEntry["agent.thinking.local"].Options) != 7 {
		t.Fatal("思考档位缺少选项")
	}
}
func TestCardsDesktopAndErrors(t *testing.T) {
	root := t.TempDir()
	cards := filepath.Join(root, "characters")
	card := filepath.Join(cards, "anne", "card")
	if err := os.MkdirAll(card, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: 安妮\ncolor: '#ffffff'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(card, "character.md"), []byte("角色设定"), 0600); err != nil {
		t.Fatal(err)
	}
	desktop := desksettings.New(root)
	if err := os.MkdirAll(filepath.Dir(desktop.File()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop.File(), []byte(`{"autoReceive":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := Read(config.Defaults(), desktop, cards)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, source string
		value       any
	}{{"desktop.autoReceive", "桌面设置", false}, {"card.anne.name", "角色卡", "安妮"}, {"card.anne.persona", "角色卡", "角色设定"}} {
		v := byKey(t, values, tc.key)
		if v.Source != tc.source || !reflect.DeepEqual(v.Value, tc.value) {
			t.Errorf("%s = %#v (%s)", tc.key, v.Value, v.Source)
		}
	}
	if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(config.Defaults(), desktop, cards); err == nil {
		t.Fatal("坏角色卡应返回错误")
	}
}

// 没有 character.yml 的目录（data/characters/_shared 这种共用目录）不是角色：不算错，也不进表。
// 2026-11-09 真窗口查出来：真实数据里有 _shared，整张设置表都读失败。
func TestReadSkipsDirsWithoutCard(t *testing.T) {
	root := t.TempDir()
	cards := filepath.Join(root, "characters")
	if err := os.MkdirAll(filepath.Join(cards, "_shared"), 0755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cards, "anne", "card")
	if err := os.MkdirAll(card, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: 安妮\n"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := Read(config.Config{}, desksettings.New(root), cards)
	if err != nil {
		t.Fatalf("共用目录把设置表读失败了：%v", err)
	}
	found := false
	for _, v := range items {
		if strings.HasPrefix(v.Key, "card._shared.") {
			t.Fatalf("共用目录进了设置表：%s", v.Key)
		}
		found = found || strings.HasPrefix(v.Key, "card.anne.")
	}
	if !found {
		t.Fatal("真角色没进设置表")
	}
}

// linkReader.fakeIPRanges 是安全项：登记了，但设置页只读，只能在配置文件里改
func TestFakeIPRangesIsReadOnlySecurityKey(t *testing.T) {
	for _, e := range Entries() {
		if e.Key != "linkReader.fakeIPRanges" {
			continue
		}
		if !e.ReadOnly || e.Reason == "" || e.Type != "列表" || e.Effect != "重启接收" {
			t.Fatalf("应只读、列表、重启接收：%+v", e)
		}
		def, ok := e.Default.([]any)
		if !ok || len(def) != 3 || def[0] != "198.18.0.0/15" || def[1] != "2001:2::/48" || def[2] != "fdfe:dcba:9876::/48" {
			t.Fatalf("默认值：%#v", e.Default)
		}
		return
	}
	t.Fatal("没登记 linkReader.fakeIPRanges")
}

// 每个键都写明生效方式：记录里的生效方式、分类没有默认值，漏写的是空串，过不了（质检 X-01、X-10）。
func TestEveryKeyHasExplicitEffect(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range records {
		if r.key == "" || r.name == "" {
			t.Errorf("记录缺键或名字：%+v", r)
		}
		if seen[r.key] {
			t.Errorf("重复登记 %s", r.key)
		}
		seen[r.key] = true
		if _, ok := effectLabels[r.effect]; !ok {
			t.Errorf("%s 没写生效方式或写错了：%q", r.key, r.effect)
		}
		if _, ok := categoryText[r.cat]; !ok {
			t.Errorf("%s 没写分类或写错了：%q", r.key, r.cat)
		}
		if _, ok := storageLabel[r.storage]; !ok {
			t.Errorf("%s 存储写错了：%q", r.key, r.storage)
		}
		if r.kind != "" {
			if _, ok := typeLabels[r.kind]; !ok {
				t.Errorf("%s 类型写错了：%q", r.key, r.kind)
			}
		}
	}
	// 一条漏写生效方式的记录不能被 Entries 补上默认值
	e := record{key: "some.new.key", name: "新的设置", cat: catAbout}.entry(reflect.ValueOf(config.Defaults()))
	if e.Effect != "" || slices.Contains(EffectKinds, e.Effect) {
		t.Fatalf("没写生效方式的记录被补了默认值：%q", e.Effect)
	}
	for _, e := range Entries() {
		if !slices.Contains(EffectKinds, e.Effect) {
			t.Errorf("%s 的生效方式 %q 不在五种之内", e.Key, e.Effect)
		}
	}
}

// 重构的边界：除说明和别名外，每个键的全部字段和重构前逐项一致（含顺序）。
// testdata/entries.golden.json 是重构前 Entries() 的输出去掉说明、别名。有意改变某个键，先在提交说明里写原因再重录：
// UPDATE_GOLDEN=1 go test ./internal/settingsreg -run TestEntriesMatchGolden
func TestEntriesMatchGolden(t *testing.T) {
	raw, err := json.Marshal(Entries())
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		delete(m, "Description")
		delete(m, "Aliases")
	}
	path := filepath.Join("testdata", "entries.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		out, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("键的个数 %d，重构前是 %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("第 %d 条 %v 变了：\n现在 %v\n原来 %v", i, want[i]["Key"], got[i], want[i])
		}
	}
}

// 后台没人读的键：只读、不出可编辑控件，说明里也不许说“重启后生效”之类；
// 读它的代码确实存在的键不能被标成尚未生效。每一项的依据写在旁边（读取处文件:行号，或“搜过都没有”）。
func TestInertKeysAreHonest(t *testing.T) {
	byKey := map[string]Entry{}
	for _, e := range Entries() {
		byKey[e.Key] = e
	}
	inert := []string{
		"memory.idleMinutes", "memory.batchEvents", "memory.topK", "memory.maxChars", // 只在默认值里出现；整理批大小写死 200（memory/summary.go）
		"expression.learnGroups", "expression.learnPrivate", "expression.learnHome", "expression.autoApprove", "expression.reviewHours", "expression.reviewBatches",
		"vision.enabled", "vision.concurrency", "vision.backfillLanes", "vision.model", // vision.model 只在模型页作只读展示（app/models.go:266）
		"console.host", "charactersDir", "characters.*.groupReminder", "characters.*.topic",
		"qq.qqDir", "qq.qqDataDir", "qq.protocolDir", "qq.protocolLogDir",
		"agent.lifecycle.recoverInputs", "agent.advisor.mode", "agent.budget.maxCostMicros", "agent.context.topicCompactAt",
		"trace.debug", "desktop.autostart", "desktop.closeBehavior", "card.*.avatar", "desktop.newInterface", "platform.connection",
		"characters.*.adultPreset", "notifications.alerts", "notifications.autostart", "occupation", "platform.selectedAccount",
		"shortcuts.registered", "plugins.builtins", "backup.mediaRetention", "tools.rules",
	}
	for _, key := range inert {
		e, ok := byKey[key]
		if !ok {
			t.Errorf("没登记 %s", key)
			continue
		}
		if !e.Inert() || e.Effect != "尚未生效" {
			t.Errorf("%s 后台没人读，应标尚未生效，现在是 %q", key, e.Effect)
		}
	}
	// 反过来：这些键今天有代码读，不能标尚未生效（读取处在括号里）
	live := []string{
		"backup.dir", "backup.daily", "backup.dailyMedia", "backup.alertGB", "backup.minFreeGB", "backup.runMaxHours", "backup.closeWaitSeconds", // app/backup.go:68-77
		"vision.direct", "vision.directMax", // app/images.go:130,134
		"tools.workdir", "tools.approvalMinutes", "tools.approvalMode", "tools.lineEnding", "tools.autoApproveDirs", // app/app.go:476-489、974
		"memory.write.characters", "memory.write.places", "memory.write.quarantineSince", "memory.rerank.model", "memory.rerank.k", "memory.rerank.timeoutSeconds",
		"embed.backend", "embed.enabled", "dataDir", "console.port", "group.frequency", "agent.maxLive",
	}
	for _, key := range live {
		e, ok := byKey[key]
		if !ok {
			t.Errorf("没登记 %s", key)
			continue
		}
		if e.Inert() {
			t.Errorf("%s 有代码在读，不能标尚未生效", key)
		}
	}
	for _, e := range byKey {
		if !e.Inert() {
			continue
		}
		if !e.ReadOnly || e.Reason == "" {
			t.Errorf("%s 尚未生效却没有只读原因，界面会出可编辑控件", e.Key)
		}
		for _, text := range []string{e.Name, e.Description, e.Reason} {
			if strings.Contains(text, "重启") || strings.Contains(text, "重新启动") || strings.Contains(text, "下一轮") || strings.Contains(text, "立即生效") {
				t.Errorf("%s 尚未生效，说明里却在说什么时候生效：%q", e.Key, text)
			}
		}
	}
}

// 说明文字里的“什么时候生效”只从生效方式来：写了重启的，生效方式就得是重启类。
func TestDescriptionAgreesWithEffect(t *testing.T) {
	for _, e := range Entries() {
		restart := strings.Contains(e.Description, "重启接收生效") || strings.Contains(e.Description, "重启程序生效")
		if restart && e.Effect != "重启接收" && e.Effect != "重启程序" {
			t.Errorf("%s 说明写要重启，生效方式却是 %s：%q", e.Key, e.Effect, e.Description)
		}
	}
}

// Lookup 认带通配符的登记项，和 Entries 同源
func TestLookupMatchesWildcardEntries(t *testing.T) {
	if e, ok := Lookup("characters.anne.model"); !ok || e.Key != "characters.*.model" || e.Effect != "下一轮对话" {
		t.Fatalf("角色模型：%+v %v", e, ok)
	}
	if e, ok := Lookup("characters.anne.topic"); !ok || !e.Inert() {
		t.Fatalf("角色话题模型：%+v %v", e, ok)
	}
	if e, ok := Lookup("tools.approvalMode"); !ok || e.Effect != "立即" {
		t.Fatalf("审批方式：%+v %v", e, ok)
	}
	if _, ok := Lookup("no.such.key"); ok {
		t.Fatal("不存在的键被认成了登记项")
	}
	if _, ok := Lookup("characters..model"); ok {
		t.Fatal("通配符位置是空的也被认成了登记项")
	}
}
