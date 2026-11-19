package private

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
	"sumika-native/internal/devtools/gitrun"
)

// 全部用 t.TempDir 造名单和小仓库，号码、名字都是编的，不碰真实 data/。

func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "") // 先登记恢复
			os.Unsetenv(k)
		}
	}
}

func execAll(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s：%v", s, err)
		}
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtures(t *testing.T, dir string) Sources {
	t.Helper()
	qq := filepath.Join(dir, "qq.db")
	execAll(t, qq,
		"CREATE TABLE events(actor_kind TEXT, actor_id TEXT, conversation TEXT)",
		"CREATE TABLE qq_people(uid TEXT, nickname TEXT, cards_json TEXT, aliases_json TEXT)",
		"CREATE TABLE qq_groups(gid TEXT, name TEXT)",
		"CREATE TABLE qq_sync(account TEXT)",
		"INSERT INTO events VALUES ('human','qq:1111222','qq:group:3333444'),('character','qq:9999000','qq:private:9999000:5555666')",
		`INSERT INTO qq_people VALUES ('7777888','编的人名','{"g":"编的名片"}','["编的别名"]')`,
		"INSERT INTO qq_groups VALUES ('1212343','编的群名')",
		"INSERT INTO qq_sync VALUES ('3131313')")
	mb := filepath.Join(dir, "mb.db")
	execAll(t, mb,
		"CREATE TABLE person_info(user_id TEXT, person_name TEXT, user_nickname TEXT, group_cardname TEXT)",
		"CREATE TABLE chat_sessions(group_id TEXT, user_id TEXT, account_id TEXT, group_name TEXT)",
		"CREATE TABLE mai_messages(user_id TEXT, group_id TEXT, account_id TEXT)",
		"CREATE TABLE bot_platform_accounts(account_id TEXT)",
		"INSERT INTO person_info VALUES ('4545676','编的麦麦名',NULL,NULL)",
		"INSERT INTO chat_sessions VALUES ('8989010',NULL,'2323454','编的会话群')",
		"INSERT INTO mai_messages VALUES ('6262626',NULL,NULL)",
		"INSERT INTO bot_platform_accounts VALUES ('7373737')")
	write(t, filepath.Join(dir, "groups.json"), `{"6767898":{"name":"编的群表名"}}`)
	write(t, filepath.Join(dir, "c.yml"), strings.Join([]string{
		"qq:", "  accounts:", "    - id: 4242424 # 她的小号", "  owner: 5656565", "  port: 30010", "  groups:", "    - 5151515",
		"  # 群改前备份 sumika.yml.bak-20261108", "magpie:", "  timeout: 12000000", "linkReader:", "  expand:", `    conversations: ["qq:group:6464646"]`, "",
	}, "\n"))
	b, _ := json.Marshal([]any{strings.ToUpper(fakeMail), "ab", 42})
	write(t, filepath.Join(dir, "p.json"), string(b))
	return Sources{QQDB: qq, MaibotDB: mb, GroupsJSON: filepath.Join(dir, "groups.json"), ConfigYML: filepath.Join(dir, "c.yml"), PrivateJSON: filepath.Join(dir, "p.json")}
}

func TestLoadKnownReadsAllSources(t *testing.T) {
	k, notes, err := LoadKnown(fixtures(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes=%v", notes)
	}
	for _, n := range []string{"1111222", "3333444", "5555666", "9999000", "7777888", "1212343", "3131313", "4545676", "8989010", "2323454", "6262626", "7373737", "6767898", "4242424", "5656565", "5151515", "6464646"} {
		if !k.numbers.has(n) {
			t.Errorf("名单里应该有 %s", n)
		}
	}
	for _, n := range []string{"12000000", "20261108", "30010"} {
		if k.numbers.has(n) {
			t.Errorf("端口、注释、无关配置不该当号码：%s", n)
		}
	}
	for _, n := range []string{"编的群名", "编的麦麦名", "编的群表名", "编的会话群"} {
		if !k.names.has(n) {
			t.Errorf("名字里应该有 %s", n)
		}
	}
	for _, n := range []string{"编的人名", "编的名片", "编的别名"} {
		if k.names.has(n) {
			t.Errorf("聊天库昵称不进名字：%s", n)
		}
	}
	if strings.Join(k.secrets.ord, "|") != fakeMail {
		t.Errorf("secrets=%v", k.secrets.ord)
	}
}

func TestLoadKnownBrokenSources(t *testing.T) {
	dir := t.TempDir()
	src := fixtures(t, dir)
	write(t, src.PrivateJSON, "["+fakeMail+"]")
	_, _, err := LoadKnown(src)
	if _, ok := err.(*SourceError); !ok {
		t.Fatalf("坏 JSON 要报 SourceError：%v", err)
	}
	if strings.Contains(err.Error(), "someone") {
		t.Errorf("报错不带出名单内容：%v", err)
	}
	write(t, src.PrivateJSON, "{}")
	if _, _, err := LoadKnown(src); err == nil || !strings.Contains(err.Error(), "不是数组") {
		t.Errorf("不是数组：%v", err)
	}
	write(t, src.PrivateJSON, "[]")
	write(t, src.QQDB, "not a database")
	if _, _, err := LoadKnown(src); err == nil {
		t.Error("聊天库坏了要报错")
	}
	_, notes, err := LoadKnown(Sources{QQDB: "nope", MaibotDB: "nope", GroupsJSON: "nope", ConfigYML: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(notes, "|") != "缺 Go 聊天库|缺 MaiBot 库|缺群名表|缺配置文件" {
		t.Errorf("缺来源如实说：%v", notes)
	}
}

// ───── 命令行：临时仓库，数据目录也是临时的 ─────

func gitIn(t *testing.T, dir string, mail string, args ...string) string {
	t.Helper()
	out, code, err := gitrun.Run(dir, nil, nil, append([]string{"-c", "user.name=编", "-c", "user.email=" + mail, "-c", "commit.gpgsign=false"}, args...)...)
	if err != nil || code != 0 {
		t.Fatalf("git %v 失败：code=%d err=%v out=%s", args, code, err, out)
	}
	return string(out)
}

func repo(t *testing.T) (dir string) {
	t.Helper()
	clearGitEnv(t)
	// 扫描跑 git 用测试自己的干净配置：不读这台机器的全局、系统配置（敌对配置在专门的用例里写进仓库）
	empty := filepath.Join(t.TempDir(), "gitconfig")
	write(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir = t.TempDir()
	gitIn(t, dir, "x@example.com", "init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "data/dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := fixtures(t, filepath.Join(dir, "data/dev"))
	if err := os.MkdirAll(filepath.Join(dir, "data/go/chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(src.QQDB)
	write(t, filepath.Join(dir, "data/go/chat/qq.db"), string(b))
	write(t, filepath.Join(dir, "data/dev/private-strings.json"), mustFile(t, src.PrivateJSON))
	write(t, filepath.Join(dir, "data/dev/human-replies/groups.json"), mustFile(t, src.GroupsJSON))
	t.Setenv("SUMIKA_SCAN_MAIBOT", src.MaibotDB)
	write(t, filepath.Join(dir, ".gitignore"), "data/\n")
	write(t, filepath.Join(dir, "README.md"), "起点\n")
	gitIn(t, dir, "x@example.com", "add", ".gitignore", "README.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "起点")
	return dir
}

func mustFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func run(dir string, args ...string) (int, string) {
	var o, e bytes.Buffer
	code := Run(args, dir, &o, &e)
	return code, o.String() + e.String()
}

func TestRunStaged(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, "a.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "a.md")
	code, out := run(dir, "--staged")
	if code != 1 || strings.Contains(out, "1111222") {
		t.Errorf("暂存了认识的号码：退出码 1、输出打码；code=%d", code)
	}
	if !strings.Contains(out, "拦 号码 11＊＊＊＊＊") {
		t.Errorf("打码样子不对：%s", out)
	}
	write(t, filepath.Join(dir, "a.md"), "陌生数字 13572468\n")
	gitIn(t, dir, "x@example.com", "add", "a.md")
	code, out = run(dir, "--staged")
	if code != 0 || !strings.Contains(out, "只有提醒") {
		t.Errorf("只有提醒：退出码 0；code=%d out=%s", code, out)
	}
	write(t, filepath.Join(dir, "a.md"), "没什么\n")
	gitIn(t, dir, "x@example.com", "add", "a.md")
	code, out = run(dir, "--staged")
	if code != 0 || !strings.Contains(out, "没发现") {
		t.Errorf("干净：code=%d out=%s", code, out)
	}
}

func TestRunMissingList(t *testing.T) {
	dir := repo(t)
	if err := os.Remove(filepath.Join(dir, "data/go/chat/qq.db")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.md"), "普通\n")
	gitIn(t, dir, "x@example.com", "add", "a.md")
	if code, _ := run(dir, "--staged"); code != 2 {
		t.Errorf("没有聊天库：退出码 2，code=%d", code)
	}
	if code, out := run(dir, "--staged", "--allow-missing"); code != 0 {
		t.Errorf("--allow-missing 才照常跑：code=%d out=%s", code, out)
	}
}

func TestRunRange(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "b.md"), "普通\n")
	gitIn(t, dir, "x@example.com", "add", "b.md")
	gitIn(t, dir, fakeMail, "commit", "-q", "-m", "普通")
	code, out := run(dir, "--range", base+"..HEAD")
	if code != 1 || !strings.Contains(out, "作者和提交者") {
		t.Errorf("作者邮箱在名单里：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "reset", "-q", "--hard", base)
	gitIn(t, dir, "x@example.com", "commit", "-q", "--allow-empty", "-m", "说明里写了 3333444")
	if code, _ := run(dir, "--range", base+"..HEAD"); code != 1 {
		t.Errorf("提交说明里的号码要拦：code=%d", code)
	}
	// 合并时手改：冲突解决里加了认识的号码
	gitIn(t, dir, "x@example.com", "reset", "-q", "--hard", base)
	gitIn(t, dir, "x@example.com", "checkout", "-q", "-b", "side")
	write(t, filepath.Join(dir, "c.md"), "一\n")
	gitIn(t, dir, "x@example.com", "add", "c.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "旁")
	gitIn(t, dir, "x@example.com", "checkout", "-q", "-")
	write(t, filepath.Join(dir, "c.md"), "二\n")
	gitIn(t, dir, "x@example.com", "add", "c.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "主")
	out2, code2, _ := gitrun.Run(dir, nil, nil, "-c", "user.name=编", "-c", "user.email=x@example.com", "merge", "--no-commit", "side")
	_ = out2
	_ = code2 // 冲突是预期的
	write(t, filepath.Join(dir, "c.md"), "合 5555666\n")
	gitIn(t, dir, "x@example.com", "add", "c.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "合并")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 {
		t.Errorf("合并提交里手改的内容也要扫：code=%d out=%s", code, out)
	}
}

// 推送前把“这次会新发到远端的全部提交”写成 “本地提交 --not 远端已有的提交”：后面的参数整个交给 rev-list
func TestRunRangeNot(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "b.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "b.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "加了号码")
	write(t, filepath.Join(dir, "b.md"), "没了\n")
	gitIn(t, dir, "x@example.com", "add", "b.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "又删掉")
	head := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	if code, out := run(dir, "--range", head, "--not", base); code != 1 {
		t.Errorf("远端只有 base：中间那个提交里加过又删掉的号码也要拦：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--range", head, "--not", head); code != 0 || !strings.Contains(out, "没发现") {
		t.Errorf("远端已经有这个提交：没有要扫的，过：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--range", head); code != 1 {
		t.Errorf("远端什么都没有：全部可达历史都扫：code=%d out=%s", code, out)
	}
	if code, _ := run(dir, "--range", "没有这个提交", "--not", base); code != 2 {
		t.Errorf("rev-list 报错要失败：code=%d", code)
	}
}

// 含 NUL 的文本文件、标了 -diff 的文本文件，git 默认当二进制不给内容；--text 之后也要扫
func TestRunTextWithNul(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "n.md"), "号 1111222\x00尾\n")
	gitIn(t, dir, "x@example.com", "add", "n.md")
	if code, out := run(dir, "--staged"); code != 1 {
		t.Errorf("暂存的含 NUL 文件里有认识的号码：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "含 NUL")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 {
		t.Errorf("范围里含 NUL 文件里有认识的号码：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "reset", "-q", "--hard", base)
	write(t, filepath.Join(dir, ".gitattributes"), "*.md -diff\n")
	write(t, filepath.Join(dir, "d.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", ".gitattributes", "d.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "标了 -diff")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 {
		t.Errorf("标了 -diff 的文件里有认识的号码：code=%d out=%s", code, out)
	}
}

// 章鱼合并（三个以上父提交）：git 的 --remerge-diff 对它只在 stderr 打 Skipping、标准输出是空的、退出码 0，所以用 -m；合并时手加的文件里的号码要拦下
func TestRunOctopusMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("起很多个 git 的慢用例（每个 5～10 秒），-short 跳过，每日整套和合并前手动跑")
	}
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	cur := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "--abbrev-ref", "HEAD"))
	for _, b := range []string{"o1", "o2", "o3"} {
		gitIn(t, dir, "x@example.com", "checkout", "-q", "-b", b, base)
		write(t, filepath.Join(dir, b+".md"), "普通\n")
		gitIn(t, dir, "x@example.com", "add", b+".md")
		gitIn(t, dir, "x@example.com", "commit", "-q", "-m", b)
	}
	gitIn(t, dir, "x@example.com", "checkout", "-q", cur)
	gitIn(t, dir, "x@example.com", "merge", "-q", "--no-commit", "o1", "o2", "o3")
	write(t, filepath.Join(dir, "n.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "n.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "章鱼合并")
	par := strings.Fields(gitIn(t, dir, "x@example.com", "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(par) != 4 {
		t.Fatalf("要是三个父提交的合并：%v", par)
	}
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 || strings.Contains(out, "1111222") {
		t.Errorf("章鱼合并里手加的文件有认识的号码要拦、输出打码：code=%d out=%s", code, out)
	}
}

// 批与批的边界：每批两个提交，号码在中间某批里
func TestRunRangeBatches(t *testing.T) {
	if testing.Short() {
		t.Skip("起很多个 git 的慢用例（每个 5～10 秒），-short 跳过，每日整套和合并前手动跑")
	}
	old := batchSize
	batchSize = 2
	t.Cleanup(func() { batchSize = old })
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	for i, text := range []string{"一", "二", "号 1111222"} {
		f := fmt.Sprintf("f%d.md", i)
		write(t, filepath.Join(dir, f), text+"\n")
		gitIn(t, dir, "x@example.com", "add", f)
		gitIn(t, dir, "x@example.com", "commit", "-q", "-m", f)
	}
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 {
		t.Errorf("号码在第二批：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--range", base+"..HEAD~1"); code != 0 {
		t.Errorf("范围只含前两个（不含号码那个）：code=%d out=%s", code, out)
	}
}

// 新建的空文件没有 +++ 行，文件名也要扫
func TestRunEmptyFileName(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "qq_1111222.md"), "")
	gitIn(t, dir, "x@example.com", "add", "qq_1111222.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "空文件")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 || !strings.Contains(out, "文件名") {
		t.Errorf("空文件的文件名里有认识的号码要拦：code=%d out=%s", code, out)
	}
}

// 分支名、tag 名和带标注 tag 的标签对象文本也扫
func TestRunRefNamesAndTagObject(t *testing.T) {
	if testing.Short() {
		t.Skip("起很多个 git 的慢用例（每个 5～10 秒），-short 跳过，每日整套和合并前手动跑")
	}
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "b.md"), "普通\n")
	gitIn(t, dir, "x@example.com", "add", "b.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "普通")
	rng := []string{"--range", base + "..HEAD"}
	if code, out := run(dir, rng...); code != 0 {
		t.Fatalf("起点是干净的：code=%d out=%s", code, out)
	}
	if code, out := run(dir, append([]string{"--line", "refs/heads/qq_1111222"}, rng...)...); code != 1 || !strings.Contains(out, "分支名或标签名") {
		t.Errorf("ref 名里的号码要拦：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "tag", "-a", "v1", "-m", "标签说明里写了 1111222")
	obj := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "v1"))
	if code, out := run(dir, append([]string{"--tag", obj}, rng...)...); code != 1 || !strings.Contains(out, "标签对象") {
		t.Errorf("标签对象文本里的号码要拦：code=%d out=%s", code, out)
	}
	if code, out := run(dir, append([]string{"--tag", base}, rng...)...); code != 0 {
		t.Errorf("不是标签对象的当没有：code=%d out=%s", code, out)
	}
}

// 用户配置能让 git 的输出变样：diff.noprefix（文件名没有 a/ b/ 前缀）、textconv（把文件内容换成别的）、log.showRoot=false（根提交不出 diff）、
// log.showSignature。扫描自己带参数盖过它们；这里把这些“敌对配置”全写进测试仓库，证明仍然拦得住。
func TestRunHostileConfig(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	for _, kv := range [][2]string{{"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}, {"log.showRoot", "false"}, {"log.showSignature", "true"}, {"diff.evil.textconv", "git hash-object"}} {
		gitIn(t, dir, "x@example.com", "config", kv[0], kv[1])
	}
	write(t, filepath.Join(dir, ".git/info/attributes"), "*.md diff=evil\n")

	// 文件名（空文件没有 +++ 行）：noprefix 下 diff 头没有 a/ b/，不带 --src-prefix 就认不出
	write(t, filepath.Join(dir, "qq_1111222.txt"), "")
	gitIn(t, dir, "x@example.com", "add", "qq_1111222.txt")
	if code, out := run(dir, "--staged"); code != 1 || !strings.Contains(out, "文件名") {
		t.Errorf("noprefix 下暂存的空文件名里有认识的号码要拦：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "空文件")
	if code, out := run(dir, "--range", base+"..HEAD"); code != 1 || !strings.Contains(out, "文件名") {
		t.Errorf("noprefix 下范围里的空文件名里有认识的号码要拦：code=%d out=%s", code, out)
	}

	// textconv：*.md 的内容被换成哈希，号码看不见
	write(t, filepath.Join(dir, "t.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "t.md")
	if code, out := run(dir, "--staged"); code != 1 {
		t.Errorf("textconv 下暂存的 md 里有认识的号码要拦：code=%d out=%s", code, out)
	}
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "md")
	if code, out := run(dir, "--range", "HEAD~1..HEAD"); code != 1 {
		t.Errorf("textconv 下范围里的 md 里有认识的号码要拦：code=%d out=%s", code, out)
	}

	// log.showRoot=false：根提交不出 diff；要 --root
	gitIn(t, dir, "x@example.com", "checkout", "-q", "--orphan", "root2")
	gitIn(t, dir, "x@example.com", "rm", "-rf", "-q", ".")
	write(t, filepath.Join(dir, "r.txt"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "r.txt")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "另一个根")
	if code, out := run(dir, "--range", "root2"); code != 1 {
		t.Errorf("showRoot=false 下根提交里有认识的号码要拦：code=%d out=%s", code, out)
	}
}

// refs/replace/ 能让 git 把一个提交换成另一个的内容；扫描设了 GIT_NO_REPLACE_OBJECTS
func TestRunReplaceObjects(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	cur := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "--abbrev-ref", "HEAD"))
	gitIn(t, dir, "x@example.com", "checkout", "-q", "-b", "clean", base)
	write(t, filepath.Join(dir, "c.md"), "干净\n")
	gitIn(t, dir, "x@example.com", "add", "c.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "干净")
	cleanC := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	gitIn(t, dir, "x@example.com", "checkout", "-q", cur)
	write(t, filepath.Join(dir, "c.md"), "号 1111222\n")
	gitIn(t, dir, "x@example.com", "add", "c.md")
	gitIn(t, dir, "x@example.com", "commit", "-q", "-m", "带号码")
	bad := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	gitIn(t, dir, "x@example.com", "replace", bad, cleanC)
	if code, out := run(dir, "--range", bad, "--not", base); code != 1 {
		t.Errorf("提交被 refs/replace 换成了干净的：扫的还是原来的提交：code=%d out=%s", code, out)
	}
}

// 手工造的提交对象：说明里有 NUL（%B 取到 NUL 就断了）、头里有额外字段（x-note）
func TestRunForgedCommitObjects(t *testing.T) {
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	tree := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD^{tree}"))
	ident := "a <x@example.com> 1700000000 +0000"
	forge := func(extra, msg string) string {
		obj := "tree " + tree + "\nparent " + base + "\nauthor " + ident + "\ncommitter " + ident + "\n" + extra + "\n" + msg
		out, code, err := gitrun.Run(dir, nil, []byte(obj), "hash-object", "-t", "commit", "--literally", "-w", "--stdin")
		if err != nil || code != 0 {
			t.Fatalf("造提交失败：code=%d err=%v out=%s", code, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	clean := forge("", "干净\n")
	if code, out := run(dir, "--range", clean, "--not", base); code != 0 {
		t.Fatalf("手造的干净提交要过：code=%d out=%s", code, out)
	}
	nul := forge("", "说明\x00号 1111222\n")
	if code, out := run(dir, "--range", nul, "--not", base); code != 1 || !strings.Contains(out, "提交说明") {
		t.Errorf("说明里 NUL 之后的号码要拦：code=%d out=%s", code, out)
	}
	hdr := forge("x-note 号 1111222\n", "干净\n")
	if code, out := run(dir, "--range", hdr, "--not", base); code != 1 || !strings.Contains(out, "提交头") {
		t.Errorf("头里额外字段里的号码要拦：code=%d out=%s", code, out)
	}
}

// 标签嵌套超过上限：读不完就报错，不放行
func TestRunTagNestingLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("起很多个 git 的慢用例（每个 5～10 秒），-short 跳过，每日整套和合并前手动跑")
	}
	dir := repo(t)
	base := strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", "HEAD"))
	prev := "HEAD"
	var objs []string
	for i := 1; i <= maxTagDepth+1; i++ {
		name := fmt.Sprintf("t%d", i)
		gitIn(t, dir, "x@example.com", "tag", "-a", name, "-m", "层", prev)
		objs = append(objs, strings.TrimSpace(gitIn(t, dir, "x@example.com", "rev-parse", name)))
		prev = name
	}
	if code, out := run(dir, "--tag", objs[len(objs)-1], "--range", base+"..HEAD"); code != 2 || !strings.Contains(out, "嵌套") {
		t.Errorf("嵌套超过 %d 层要报错：code=%d out=%s", maxTagDepth, code, out)
	}
	if code, out := run(dir, "--tag", objs[len(objs)-3], "--range", base+"..HEAD"); code != 0 {
		t.Errorf("嵌套不深的标签照常读：code=%d out=%s", code, out)
	}
}

func TestCommitLines(t *testing.T) {
	h := strings.Repeat("a", 40)
	text := "tree " + h + "\nparent " + h + "\nauthor 甲 <j@example.com> 1700000000 +0800\ncommitter 乙 <y@example.com> 1700000001 +0800\ngpgsig -----BEGIN\n 续行里有 号 1111222\n -----END\nx-note 额外\n\n标题\n\n正文\x00还有\n"
	var where []string
	var texts []string
	for _, l := range commitLines(h, text) {
		where = append(where, l.Where)
		if l.KnownOnly {
			continue // 时间戳那一行只对本机认识的号码比一次，别的规则不扫
		}
		texts = append(texts, l.Text)
	}
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, h) || strings.Contains(joined, "1700000000") {
		t.Errorf("tree、parent 的哈希、author 的时间戳不扫：%q", texts)
	}
	for _, want := range []string{"续行里有 号 1111222", "x-note 额外", "author 甲 <j@example.com>", "正文\x00还有"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %q：%q", want, texts)
		}
	}
	w := strings.Join(where, "|")
	for _, want := range []string{"aaaaaaaa 作者和提交者", "aaaaaaaa 提交头", "aaaaaaaa 提交说明"} {
		if !strings.Contains(w, want) {
			t.Errorf("位置缺 %q：%s", want, w)
		}
	}
	// 头里的 tree 不是哈希（手造的）：照扫
	if ls := commitLines(h, "tree 号 1111222\n\n"); len(ls) == 0 || !strings.Contains(ls[0].Text, "1111222") {
		t.Errorf("tree 值不是哈希要扫：%v", ls)
	}
}

func TestCheckMarkers(t *testing.T) {
	if err := checkMarkers([]string{"a", "b"}, []string{"a", "b"}, false); err != nil {
		t.Errorf("对得上：%v", err)
	}
	if err := checkMarkers([]string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Error("少了一个要报错")
	}
	if err := checkMarkers([]string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Errorf("章鱼合并允许少：%v", err)
	}
	if err := checkMarkers([]string{"a"}, []string{"a", "x"}, true); err == nil {
		t.Error("多出没请求的提交要报错")
	}
}

func TestCollectUnknownPrefix(t *testing.T) {
	label := func(f string, n int) string { return fmt.Sprintf("%s:%d", f, n) }
	if _, err := Collect("diff --git x/y.md z/y.md\nnew file mode 100644\n", label); err == nil {
		t.Error("认不出 a/ b/ 前缀要报错，不能静默跳过")
	}
	if _, err := Collect("diff --git a/old.md b/renamed-longer-name.md\nrename from old.md\nrename to renamed-longer-name.md\n", label); err != nil {
		t.Errorf("改名两边长度不同照常认：%v", err)
	}
}

func TestRunAllowFlow(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, "a.md"), "陌生数字 13572468\n")
	gitIn(t, dir, "x@example.com", "add", "a.md")
	if code, _ := run(dir, "--staged"); code != 0 {
		t.Fatal("提醒不拦")
	}
	if code, out := run(dir, "--allow", "9"); code != 1 || !strings.Contains(out, "没有编号 9") {
		t.Errorf("没有这个编号：code=%d out=%s", code, out)
	}
	if code, out := run(dir, "--allow", "1"); code != 0 || !strings.Contains(out, "登记了 1 条误报，一共 1 条") {
		t.Errorf("登记：code=%d out=%s", code, out)
	}
	b := mustFile(t, filepath.Join(dir, "data/dev/private-scan-allow.json"))
	var arr []string
	if err := json.Unmarshal([]byte(b), &arr); err != nil || len(arr) != 1 || strings.Contains(b, "13572468") {
		t.Errorf("登记文件只存哈希：%s", b)
	}
	if !strings.HasPrefix(b, "[\n \"") {
		t.Errorf("登记文件格式和旧版一致（缩进 1 个空格）：%q", b)
	}
	if _, out := run(dir, "--staged"); !strings.Contains(out, "登记过的误报 1 条") || !strings.Contains(out, "没发现") {
		t.Errorf("登记后不再报：%s", out)
	}
}

func TestRunUsage(t *testing.T) {
	dir := repo(t)
	if code, out := run(dir, "--bogus"); code != 2 || !strings.Contains(out, "用法：") {
		t.Errorf("code=%d out=%s", code, out)
	}
}

func TestRunTree(t *testing.T) {
	dir := repo(t)
	tree := t.TempDir()
	write(t, filepath.Join(tree, "a.md"), "号 1111222\n陌生数字 13572468\n")
	write(t, filepath.Join(tree, "sub/b.txt"), "没事\n")
	if err := os.WriteFile(filepath.Join(tree, "bin.dat"), []byte("1111222\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := run(dir, "--tree", tree)
	if code != 1 || strings.Contains(out, "1111222") || !strings.Contains(out, "拦 1 组 / 1 处，提醒 1 处") {
		t.Errorf("code=%d out=%s", code, out)
	}
	write(t, filepath.Join(tree, "a.md"), "干净\n")
	if code, out := run(dir, "--tree", tree); code != 0 || !strings.Contains(out, "拦 0 组") {
		t.Errorf("code=%d out=%s", code, out)
	}
	os.Remove(filepath.Join(dir, "data/go/chat/qq.db"))
	os.Remove(filepath.Join(dir, "data/dev/human-replies/groups.json"))
	t.Setenv("SUMIKA_SCAN_MAIBOT", "")
	if code, _ := run(dir, "--tree", tree); code != 2 {
		t.Errorf("没有号码名单：退出码 2，code=%d", code)
	}
}

// 配置的 qq 子树里写 .inf、-.inf、.nan 时旧版（Bun.YAML）读成 Infinity 等字符串，不是号码；新版以前在 jsNum 里越界 panic。
func TestJSNumSpecials(t *testing.T) {
	cases := map[float64]string{0: "0", 1: "1", -12345678: "-12345678", 1.5: "1.5", 1e21: "1e+21", 1.5e-7: "1.5e-7", 123456789012345680000: "123456789012345680000"}
	for f, want := range cases {
		if got := jsNum(f); got != want {
			t.Errorf("jsNum(%v)=%q want %q", f, got, want)
		}
	}
	inf, ninf, nan := math.Inf(1), math.Inf(-1), math.NaN()
	if jsNum(inf) != "Infinity" || jsNum(ninf) != "-Infinity" || jsNum(nan) != "NaN" || jsNum(math.Copysign(0, -1)) != "0" {
		t.Errorf("特殊值：%q %q %q", jsNum(inf), jsNum(ninf), jsNum(nan))
	}
}

func TestLoadKnownYAMLInf(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yml"), "qq:\n  a: .inf\n  b: -.inf\n  c: .nan\n  id: 24681357\n")
	k, _, err := LoadKnown(Sources{ConfigYML: filepath.Join(dir, "c.yml")})
	if err != nil {
		t.Fatal(err)
	}
	if !k.numbers.has("24681357") || k.numbers.size() != 1 {
		t.Errorf("numbers=%v", k.numbers.ord)
	}
}

// 配置读取对拍：yaml-parity.json 是旧版（Bun.YAML）对这批编造配置收到的号码，顺序也比。
// 旧版放过、新版有意报错（退出码 2）的写法标 strict：重复键、复杂键。
func TestConfigParityWithOld(t *testing.T) {
	b, err := os.ReadFile("testdata/yaml-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string
		Yaml    string
		Numbers []string
		Strict  bool
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 25 {
		t.Fatalf("对拍文件只有 %d 条", len(cases))
	}
	for _, c := range cases {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "c.yml"), c.Yaml)
		k, _, err := LoadKnown(Sources{ConfigYML: filepath.Join(dir, "c.yml")})
		if c.Strict {
			if _, ok := err.(*SourceError); !ok {
				t.Errorf("%s：新版应该报错退出 2，err=%v", c.Name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s：%v", c.Name, err)
			continue
		}
		if strings.Join(k.numbers.ord, ",") != strings.Join(c.Numbers, ",") {
			t.Errorf("%s：新版 %v，旧版 %v", c.Name, k.numbers.ord, c.Numbers)
		}
	}
}

// SUMIKA_SCAN_DATA 把名单和误报登记的位置指到编造的目录（导出自检的测试用它，不用 SUMIKA_DEV_ROOT，免得程序按假源码根去重编）。
func TestScanDataEnv(t *testing.T) {
	clearGitEnv(t)
	// 编造的号码在运行时拼出来：源码里写成“号 + 整串数字”的话，导出自检扫整棵树时会把这个测试文件自己当成泄露拦下（2026-11-19）
	num := "2468" + "1357"
	data := t.TempDir()
	write(t, filepath.Join(data, "data/dev/human-replies/groups.json"), `{"g":"`+num+`"}`)
	tree := t.TempDir()
	write(t, filepath.Join(tree, "a.md"), "号 "+num+"\n")
	t.Setenv("SUMIKA_SCAN_DATA", data)
	root := t.TempDir() // 没有 data/，也不是 git 仓库：不设环境变量时读不到名单
	code, out := run(root, "--tree", tree)
	if code != 1 || strings.Contains(out, num) || !strings.Contains(out, "拦 号码") || !strings.Contains(out, "警告：名单目录被 SUMIKA_SCAN_DATA 改到了") || !strings.Contains(out, "只该在测试里用") {
		t.Errorf("code=%d out=%s", code, out)
	}
	t.Setenv("SUMIKA_SCAN_DATA", "")
	if code, _ := run(root, "--tree", tree); code != 2 {
		t.Errorf("没有名单应该退出 2，code=%d", code)
	}
}
