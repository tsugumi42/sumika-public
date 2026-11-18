// Command rpc-dump 把 Go 读出来的会话和消息按界面协议原样导成 JSON（规格-网页界面和Electrobun 第十节"第 2 步数据源"）。
// 网页界面量性能时用这份：和 mygo 拿去画的是同一份字节（名字、图片段和尺寸、表情回应、引用、通知都在），画法对得齐。
// Bun 后台的 QQ 显示字段排在 ADR-345 第 4 批，现在给不全，所以先从 Go 导。
//
// 只读 -data 指定的 desk-fixture 目录（有 .desk-fixture 标记才认），不碰真实 data/。
//
//	go run ./cmd/desk-fixture -out <新目录> -messages 18000
//	go run ./cmd/rpc-dump -data <同一个目录> -out <dump.json>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/store"
)

// Dump 是导出的文件：overview.read 的结果，加上每个会话从最新往前逐页的 messages.read 结果（原样的协议 JSON）。
type Dump struct {
	Overview json.RawMessage              `json:"overview"`
	Messages map[string][]json.RawMessage `json:"messages"` // 键是会话 id；值是一页一页的 MessagePage（result.value）
	Limit    int                          `json:"limit"`
	Media    map[string]json.RawMessage   `json:"media"` // 键是图的哈希；值是 media.read 的结果（tier 0 原图，网页自己量宽高、解码）
	Faces    map[string]json.RawMessage   `json:"faces"` // 小黄脸 id → faces.png 的结果
}

func main() {
	dir := flag.String("data", "", "desk-fixture 造的数据目录")
	out := flag.String("out", "", "写到哪个 JSON 文件")
	limit := flag.Int("limit", 200, "每页多少条（和界面一次读的一样）")
	flag.Parse()
	if err := run(*dir, *out, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "导出失败：", err)
		os.Exit(1)
	}
}

func run(dir, out string, limit int) error {
	if dir == "" || out == "" {
		return fmt.Errorf("要给 -data 和 -out")
	}
	if _, err := os.Stat(filepath.Join(dir, ".desk-fixture")); err != nil {
		return fmt.Errorf("%s 不是 desk-fixture 造的目录（没有 .desk-fixture 标记），不碰", dir)
	}
	p, err := paths.Open(dir)
	if err != nil {
		return err
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		return err
	}
	defer set.Close()
	entries, _ := os.ReadDir(p.CharactersDir())
	var chars []api.CharacterView
	for _, e := range entries {
		chars = append(chars, api.CharacterView{ID: e.Name(), Name: e.Name()})
	}
	svc := desktopread.New(set, chars, nil).WithData(p.Root)
	defer svc.Close()
	srv := rpcserve.New(&desktopread.Reader{Service: svc}, "dump")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, ct := rpcserve.Pipe()
	go srv.Serve(ctx, st)

	id := 0
	call := func(method string, params any) (json.RawMessage, error) {
		id++
		req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err := ct.Write(ctx, req); err != nil {
			return nil, err
		}
		for {
			raw, err := ct.Read(ctx)
			if err != nil {
				return nil, err
			}
			var r struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &r) != nil || r.ID == nil || *r.ID != id {
				continue // 通知或别的回复
			}
			if r.Error != nil {
				return nil, fmt.Errorf("%s：%d %s", method, r.Error.Code, r.Error.Message)
			}
			return r.Result, nil
		}
	}
	if _, err := call("initialize", map[string]any{"protocol_version": 1, "client": "rpc-dump"}); err != nil {
		return err
	}
	ov, err := call("overview.read", map[string]any{"limit": 1000})
	if err != nil {
		return err
	}
	var ovr struct {
		Value struct {
			Conversations struct {
				Items []struct {
					Key json.RawMessage `json:"key"`
				} `json:"items"`
			} `json:"conversations"`
		} `json:"value"`
	}
	if err := json.Unmarshal(ov, &ovr); err != nil {
		return fmt.Errorf("读不懂 overview：%w", err)
	}
	d := Dump{Overview: ov, Messages: map[string][]json.RawMessage{}, Limit: limit}
	total := 0
	for _, it := range ovr.Value.Conversations.Items {
		var key struct {
			Conversation string `json:"conversation"`
		}
		_ = json.Unmarshal(it.Key, &key)
		cursor := ""
		for {
			params := map[string]any{"request": map[string]any{"key": it.Key, "limit": limit}}
			if cursor != "" {
				params["cursor"] = cursor
			}
			res, err := call("messages.read", params)
			if err != nil {
				return fmt.Errorf("%s：%w", key.Conversation, err)
			}
			var page struct {
				State string `json:"state"`
				Value struct {
					Items      []json.RawMessage `json:"items"`
					NextCursor string            `json:"next_cursor"`
					HasMore    bool              `json:"has_more"`
				} `json:"value"`
			}
			if err := json.Unmarshal(res, &page); err != nil {
				return err
			}
			if page.State != "ready" {
				return fmt.Errorf("%s 读消息不是 ready：%s", key.Conversation, page.State)
			}
			raw, _ := json.Marshal(page.Value)
			d.Messages[key.Conversation] = append(d.Messages[key.Conversation], raw)
			total += len(page.Value.Items)
			if !page.Value.HasMore {
				break
			}
			cursor = page.Value.NextCursor
		}
	}
	// 每张不同的图读一次原图：网页按真图的宽高占位、真解码，和 mygo 一样（图段里只有引用和哈希，没有宽高）
	d.Media, d.Faces = map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for _, pages := range d.Messages {
		for _, raw := range pages {
			var page struct {
				Items []struct {
					Segments []struct {
						Kind  string          `json:"kind"`
						Ref   string          `json:"ref"`
						Media json.RawMessage `json:"media"`
					} `json:"segments"`
				} `json:"items"`
			}
			_ = json.Unmarshal(raw, &page)
			for _, m := range page.Items {
				for _, seg := range m.Segments {
					if seg.Kind == "face" && seg.Ref != "" {
						if _, ok := d.Faces[seg.Ref]; !ok {
							res, err := call("faces.png", map[string]any{"id": seg.Ref})
							if err != nil {
								return fmt.Errorf("读小黄脸 %s：%w", seg.Ref, err)
							}
							d.Faces[seg.Ref] = res
						}
					}
					if len(seg.Media) == 0 {
						continue
					}
					var ref struct {
						Hash string `json:"hash"`
					}
					_ = json.Unmarshal(seg.Media, &ref)
					if _, ok := d.Media[ref.Hash]; ok || ref.Hash == "" {
						continue
					}
					res, err := call("media.read", map[string]any{"ref": seg.Media})
					if err != nil {
						return fmt.Errorf("读图 %s：%w", ref.Hash[:8], err)
					}
					d.Media[ref.Hash] = res
				}
			}
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("导出了 %d 个会话、%d 条、%d 张图，%.1f MB → %s\n", len(d.Messages), total, len(d.Media), float64(len(b))/1e6, out)
	return nil
}
