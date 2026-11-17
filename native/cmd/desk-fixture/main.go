// Command desk-fixture 造一整套编的数据目录，给桌面入口的端到端和十倍量测用（规格-界面接Go后台 第九节 T10 固定负载）。
// 名字、号码、聊天、图全是编的；只往 -out 指定的新目录写，目录已存在且不空就拒绝，不碰真实 data/。
//
//	go run ./cmd/desk-fixture -out <新目录> [-messages 10000]
//	sumika.exe -root <仓库根> -data <新目录>     # 只看历史打开它
//
// 内容：10 个角色（卡 + 配置）；每个角色的本地日常 120 条、第一个角色一个话题；一个 QQ 群至少 -messages 条，
// 两个角色观察（交替、部分两人都看到）、每 97 条一条撤回通知、部分她的发言标未发送、静态图和动图，
// 最新处连着 20 张动图，一条 60 KiB 的长消息；一个 QQ 私聊；小黄脸索引和两张图。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

const group = "qq:group:90002"

// FixtureMarker 是 desk-fixture 在数据目录根写的标记文件名
const FixtureMarker = ".desk-fixture"

var names = []string{"编造甲", "编造乙", "编造丙", "编造丁", "编造戊", "编造己", "编造庚", "编造辛", "编造壬", "编造癸"}
var lines = []string{"今天吃什么", "哈哈哈这也太巧了", "有人看了昨晚那集吗", "我觉得还行吧", "等等我去拿个外卖", "这段代码跑不起来", "周末有人去漫展吗"}

func main() {
	out := flag.String("out", "", "新的数据目录（不存在或为空）")
	n := flag.Int("messages", 10000, "QQ 群的消息条数")
	flag.Parse()
	if err := run(*out, *n); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string, n int) error {
	if out == "" {
		return fmt.Errorf("要给 -out")
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s 已经有东西了，换一个新目录", out)
	}
	p, err := paths.Init(out)
	if err != nil {
		return err
	}
	// 标记：这是编造的数据目录。会往库里写的工具（cmd/rpc-bench）只认带这个标记的目录
	if err := os.WriteFile(filepath.Join(out, FixtureMarker), []byte("desk-fixture\n"), 0o644); err != nil {
		return err
	}
	var cfg strings.Builder
	cfg.WriteString("characters:\n")
	for i, name := range names {
		id := fmt.Sprintf("c%d", i)
		cfg.WriteString(fmt.Sprintf("  %s:\n    model: fake-model-%d\n", id, i%3))
		dir := filepath.Join(p.CharactersDir(), id, "card")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		yml := fmt.Sprintf("name: %s\ncolor: \"#%02x%02x%02x\"\n", name, 80+i*15, 140, 200-i*10)
		if err := os.WriteFile(filepath.Join(dir, "character.yml"), []byte(yml), 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(p.ConfigDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(cfg.String()), 0o644); err != nil {
		return err
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		return err
	}
	defer set.Close()
	local, _ := set.Platform("local")
	qq, _ := set.Platform("qq")
	start := time.Now().Add(-time.Duration(n+2000) * time.Minute).UnixMilli()

	// 本地日常和一个话题
	for i := range names {
		conv := fmt.Sprintf("console:c%d", i)
		for j := 0; j < 120; j++ {
			e := store.NewEvent{Channel: "console", Conversation: conv, Kind: "message", Text: lines[(i+j)%len(lines)], CreatedAt: start + int64(j)*60_000}
			if j%2 == 0 {
				e.Origin, e.ActorKind, e.ActorID = "owner", "owner", "owner"
			} else {
				e.Origin, e.ActorKind, e.ActorID = "character", "character", fmt.Sprintf("c%d", i)
			}
			if _, err := local.AppendEvent(e, nil); err != nil {
				return err
			}
		}
	}
	if _, err := local.Events.Exec(`INSERT INTO local_topics(id,character_id,title,created_at) VALUES ('t1','c0','编的话题',?)`, start); err != nil {
		return err
	}
	for j := 0; j < 40; j++ {
		e := store.NewEvent{Channel: "console", Conversation: "console:c0:topic:t1", Kind: "message", Origin: "owner", ActorKind: "owner", ActorID: "owner", Text: "话题里的第 " + fmt.Sprint(j) + " 句", CreatedAt: start + int64(j)*30_000}
		if _, err := local.AppendEvent(e, nil); err != nil {
			return err
		}
	}

	// QQ：群友名字
	for k := 0; k < 20; k++ {
		if _, err := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname) VALUES (?,?)`, fmt.Sprint(10001+k), fmt.Sprintf("群友%02d", k)); err != nil {
			return err
		}
	}
	if _, err := qq.Events.Exec(`INSERT INTO qq_groups(gid,name,updated_at) VALUES ('90002','编的测试群',?)`, start); err != nil {
		return err
	}
	statics, anims := make([]string, 6), make([]string, 20)
	for k := range statics {
		h, err := writeMedia(p.Root, pngBytes(320+k*80, 240, k))
		if err != nil {
			return err
		}
		statics[k] = h
	}
	for k := range anims {
		h, err := writeMedia(p.Root, gifBytes(96, k))
		if err != nil {
			return err
		}
		anims[k] = h
	}
	long := strings.Repeat("很长的中文消息，用来量排版和滚动。func main() { fmt.Println(\"代码也算\") }\n", 600) // 约 60 KiB
	var last string
	for j := 0; j < n; j++ {
		at := start + int64(j)*60_000
		obs := []string{"c0"}
		if j%3 == 1 {
			obs = []string{"c1"}
		} else if j%5 == 0 {
			obs = []string{"c0", "c1"}
		}
		e := store.NewEvent{Channel: "qq", Conversation: group, Kind: "message", Origin: "member", ActorKind: "human", ActorID: fmt.Sprint(10001 + j%20), Text: lines[j%len(lines)], CreatedAt: at}
		switch {
		case j == n-30:
			e.Text = long
		case j >= n-20: // 最新处连着 20 张动图，一屏能同时看到
			e.Text, e.Segments = "[动图]", map[string]any{"segs": []any{map[string]any{"kind": "emoji", "hash": anims[n-1-j]}}}
		case j%41 == 7:
			e.Text, e.Segments = "[图片]", map[string]any{"segs": []any{map[string]any{"kind": "image", "hash": statics[j%len(statics)]}}}
		case j%53 == 11:
			e.Text, e.Segments = "[表情]", map[string]any{"segs": []any{map[string]any{"kind": "text", "text": "笑死 "}, map[string]any{"kind": "face", "ref": "14"}}}
		case j%17 == 3:
			e.Origin, e.ActorKind, e.ActorID = "character", "character", obs[0]
			if j%34 == 3 {
				e.Segments = map[string]any{"segs": []any{map[string]any{"kind": "text", "text": e.Text}}, "unsent": true}
			}
		}
		seq, err := qq.AppendEvent(e, obs)
		if err != nil {
			return err
		}
		if j%97 == 50 && last != "" {
			ref := last
			notice := store.NewEvent{Channel: "qq", Conversation: group, Kind: "notice", Origin: "member", ActorKind: "human", ActorID: "10001", Text: "撤回了一条消息", ReplyTo: &ref, Segments: map[string]any{"notice": "recall"}, CreatedAt: at + 1}
			if _, err := qq.AppendEvent(notice, obs); err != nil {
				return err
			}
		}
		if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&last); err != nil {
			return err
		}
	}
	for j := 0; j < 200; j++ {
		e := store.NewEvent{Channel: "qq", Conversation: "qq:private:90001:20001", Kind: "message", Origin: "member", ActorKind: "human", ActorID: "20001", Text: lines[j%len(lines)], CreatedAt: start + int64(j)*90_000}
		if j%2 == 1 {
			e.Origin, e.ActorKind, e.ActorID = "character", "character", "c0"
		}
		if _, err := qq.AppendEvent(e, []string{"c0"}); err != nil {
			return err
		}
	}

	// 小黄脸
	faces := filepath.Join(p.Root, "cache", "qq", "faces")
	if err := os.MkdirAll(faces, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(faces, "index.json"), []byte(`{"version":"fixture","faces":{"14":{"name":"微笑","kind":"classic"},"21":{"name":"可爱","kind":"classic"}}}`), 0o644); err != nil {
		return err
	}
	for _, id := range []string{"14", "21"} {
		if err := os.WriteFile(filepath.Join(faces, id+".png"), pngBytes(64, 64, len(id)), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("写好了：%s（QQ 群 %d 条，本地 %d 个角色）\n", p.Root, n, len(names))
	return nil
}

func writeMedia(root string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	dir := filepath.Join(root, "go", "media", "qq", h[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return h, os.WriteFile(filepath.Join(dir, h), data, 0o644)
}

func pngBytes(w, h, seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(60 + seed*30), uint8(x * 255 / w), uint8(y * 255 / h), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// gifBytes 8 帧的小动图，每张颜色不同（哈希也就不同）
func gifBytes(size, seed int) []byte {
	g := &gif.GIF{}
	for f := 0; f < 8; f++ {
		img := image.NewPaletted(image.Rect(0, 0, size, size), palette.Plan9)
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if (x+f*size/8)%size < size/2 {
					img.Set(x, y, color.RGBA{uint8(seed * 12), 120, 200, 255})
				} else {
					img.Set(x, y, color.RGBA{240, uint8(seed * 12), 80, 255})
				}
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 8)
	}
	var b bytes.Buffer
	_ = gif.EncodeAll(&b, g)
	return b.Bytes()
}
