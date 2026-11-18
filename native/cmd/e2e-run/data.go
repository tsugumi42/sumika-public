package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func prepare(ctx context.Context, o options, dir, peerURL, gateway string) (paths.Paths, error) {
	p, err := paths.Init(filepath.Join(dir, "data"))
	if err != nil {
		return p, err
	}
	cfg := config.Defaults()
	cfg.Magpie.BaseURL = gateway
	cfg.DataDir = p.Root
	cfg.QQ.Owner = "200"
	cfg.QQ.Accounts = []config.QQAccount{{ID: "100", Character: "lanhe", URL: peerURL, Token: peerToken}}
	cfg.QQ.Groups = []config.QQID{"300"}
	cfg.QQ.Reply = true
	cfg.QQ.GoSend = true
	cfg.Group.Mode = "speak"
	cfg.Group.Frequency = 0.3
	cfg.Group.Jitter = 0
	cfg.Characters = map[string]config.CharacterModels{"lanhe": {Model: o.model}}
	cfg.Defaults.BackgroundModel = o.model
	cfg.Defaults.BackgroundThinking = "low"
	cfg.Embed.Enabled = true
	cfg.Embed.Backend = "go-models"
	cfg.Embed.Model = "gte-multilingual-base"
	cfg.Embed.ModelsRoot = o.modelsRoot
	cfg.Embed.ModelsExe = o.modelsExe
	cfg.Embed.OrtLibrary = o.ort
	cfg.Memory.Write.Characters = []string{"lanhe"}
	cfg.Memory.Write.Summarize = []string{}
	cfg.Backup.Dir = filepath.Join(p.Root, "backups")
	cfg.Backup.Daily = false
	cfg.Tools.Workdir = p.Root
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return p, err
	}
	if err = writeFile(p.ConfigFile(), b); err != nil {
		return p, err
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err = os.ReadFile(filepath.Join(o.root, "characters", "example", name))
		if err != nil {
			return p, err
		}
		text := strings.NewReplacer("示例角色", "岚禾", "小样", "岚禾", "阿样", "阿禾").Replace(string(b))
		if err = writeFile(filepath.Join(p.CardDir("lanhe"), name), []byte(text)); err != nil {
			return p, err
		}
	}
	// 记忆库不在这里造：交给程序启动时的 memory.CreateIfEmpty（smoke 顺带测自动建库）。
	// 这只是自建假号的协议交接记录，不调用会枚举真实后台的首次接管入口。
	if err = writeJSON(sendlock.IntentFile(p.Root), sendlock.Intent{Account: "100", Holder: sendlock.HolderGo}); err != nil {
		return p, err
	}
	if err = writeJSON(sendlock.LockFile(p.Root), sendlock.Record{Protocol: sendlock.Protocol, Account: "100", Holder: sendlock.HolderGo, State: sendlock.StateReleased, ReleasedAt: time.Now().UnixMilli(), Origin: "e2e-fixture"}); err != nil {
		return p, err
	}
	return p, nil
}
func writeFile(file string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	return os.WriteFile(file, b, 0600)
}
func writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(file, b)
}

type observations struct{ chat, trace *sql.DB }

func openObservations(p paths.Paths) (*observations, error) {
	c, err := store.OpenReadOnlyFile(p.ChatDB("qq"))
	if err != nil {
		return nil, err
	}
	t, err := store.OpenReadOnlyFile(p.GoTraceDB())
	if err != nil {
		c.Close()
		return nil, err
	}
	return &observations{c, t}, nil
}
func (d *observations) close() { d.chat.Close(); d.trace.Close() }
func (d *observations) received(ctx context.Context, id string) (bool, error) {
	var n int
	err := d.chat.QueryRowContext(ctx, `SELECT count(*) FROM channel_msgs WHERE platform_id=?`, id).Scan(&n)
	return n > 0, err
}
func (d *observations) settle(ctx context.Context, s step, id string, at time.Time) (string, error) {
	var state, kind, reason string
	ready := func() (bool, error) {
		ok, err := d.received(ctx, id)
		if err != nil || !ok {
			return false, err
		}
		err = d.chat.QueryRowContext(ctx, `SELECT state,kind,COALESCE(reason,'') FROM outbound_plans WHERE conversation=? AND created_at>=? ORDER BY created_at DESC LIMIT 1`, conversation(s), at.UnixMilli()).Scan(&state, &kind, &reason)
		if err == nil {
			if state == "sending" {
				return false, nil
			}
			return true, nil
		}
		if err != sql.ErrNoRows {
			return false, err
		}
		if s.Scene == "group" {
			var verdict, outcome, why string
			err = d.trace.QueryRowContext(ctx, `SELECT verdict,COALESCE(outcome,''),reason FROM gate_decisions WHERE conversation=? AND at>=? ORDER BY id DESC LIMIT 1`, conversation(s), at.UnixMilli()).Scan(&verdict, &outcome, &why)
			if err == sql.ErrNoRows {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			// merged 还在等一分钟的真实批处理，不能记成“没回”就推进下一步。
			if verdict == "merged" || ((verdict == "forced" || verdict == "enter") && outcome == "") {
				return false, nil
			}
			state = "闸门 " + verdict + "/" + outcome + "：" + why
			return time.Since(at) >= s.wait(), nil
		}
		if !s.ExpectReply && time.Since(at) >= max(s.wait(), 4*time.Second) {
			state = "没有回复"
			return true, nil
		}
		return false, nil
	}
	if err := until(ctx, ready); err != nil {
		return "", err
	}
	if kind != "" {
		state = kind + "/" + state + " " + reason
	}
	return state, nil
}
func until(ctx context.Context, f func() (bool, error)) error {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		ok, err := f()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
func (d *observations) recalled(ctx context.Context, id string) (bool, error) {
	var n int
	err := d.chat.QueryRowContext(ctx, `SELECT count(*) FROM events r JOIN events e ON r.reply_to=e.id JOIN channel_msgs m ON e.seq=m.event_seq WHERE m.platform_id=? AND r.kind='notice' AND json_extract(r.segments_json,'$.notice')='recall'`, id).Scan(&n)
	return n > 0, err
}
func collectRows(ctx context.Context, db *sql.DB, q string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, k := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			row[k] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func collect(ctx context.Context, p paths.Paths, r *report) error {
	for _, item := range []struct{ file, q, name string }{
		{filepath.Join(p.Root, "go", "characters", "lanhe", "memory.db"), `SELECT id,kind,text,status,scope,subject,source_seqs FROM facts ORDER BY created_at,id`, "记忆事实"},
		{filepath.Join(p.Root, "go", "characters", "lanhe", "memory.db"), `SELECT id,text,status,scope FROM episodes ORDER BY created_at,id`, "记忆片段"},
		{filepath.Join(p.Root, "go", "characters", "lanhe", "memory.db"), `SELECT ref,scope FROM item_scopes ORDER BY ref,scope`, "记忆可见范围"},
		{filepath.Join(p.Root, "go", "characters", "lanhe", "memory.db"), `SELECT key,value FROM memory_meta ORDER BY key`, "记忆写入状态"},
		{p.ChatDB("qq"), `SELECT * FROM media_fetch ORDER BY event_seq,idx`, "收图下载"},
		{p.GoTraceDB(), `SELECT r.conversation,r.started_at,p.text FROM model_runs r,json_each(r.sections_json) s,prompt_sections p WHERE json_extract(s.value,'$.id')='memory' AND p.hash=json_extract(s.value,'$.hash') ORDER BY r.started_at,r.id`, "本轮实际注入的参考信息"},
		{p.GoTraceDB(), `SELECT conversation,turn_id,name,args_json,preview,status,is_error FROM steps WHERE kind='tool' AND name IN ('remember','memory_search','recall_raw') ORDER BY started_at,id`, "记忆工具"},
		{p.GoTraceDB(), `SELECT * FROM gate_decisions ORDER BY id`, "闸门判定"},
	} {
		db, err := store.OpenReadOnlyFile(item.file)
		if err != nil {
			return err
		}
		rows, err := collectRows(ctx, db, item.q)
		db.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
		r.Evidence[item.name] = rows
	}
	return nil
}
