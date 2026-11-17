package runstats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sumika-native/internal/ctlproto"
)

// ErrNoRequest 是按 id 找不到这次请求（记录被清理了，或 id 不对）。
var ErrNoRequest = errors.New("没找到这次请求的记录")

// injectedSection 是 sections_json 里哪些段算“注入的上下文”：每轮从记忆里取来追加的参考信息。
// 其余的段（核心规矩、人设、场景）算系统提示词。
const injectedSection = "memory"

// maxDetailText 是单段正文最多交给界面多少字节：超过的截掉末尾并写明，免得一段巨长的提示词拖垮一帧。
const maxDetailText = 256 << 10

// RequestDetail 读一次请求的详情（规格-右栏上下文 第二刀）：这是唯一碰 sections_json、injected_json、context_json
// 这几列大文本的入口，只在界面点开某一次请求时调；列表查询（ConvTurns）仍然不碰。
// 各列解析不了不报错：原文放进对应的 Raw，ParseErr 写明，界面原样显示。
func (s *Service) RequestDetail(ctx context.Context, id string) (ctlproto.RequestDetail, error) {
	if id == "" {
		return ctlproto.RequestDetail{}, ErrNoRequest
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var d ctlproto.RequestDetail
	var sections, injected, context_, request, route sql.NullString
	var stage, model, failure string
	var write int64
	err := s.db.QueryRowContext(ctx, `SELECT id, stage, model, COALESCE(route_provider,'') || CASE WHEN COALESCE(route_model,'')='' THEN '' ELSE '/' || route_model END,
		COALESCE(total_ms,0), COALESCE(ttft_ms,0), COALESCE(input_tokens,0), COALESCE(cache_read_tokens,0), COALESCE(cache_write_tokens,0),
		COALESCE(output_tokens,0), COALESCE(reasoning_tokens,0), COALESCE(error,''), sections_json, injected_json, context_json, request_json
		FROM model_runs WHERE id = ?`, id).Scan(&d.ID, &stage, &model, &route, &d.TotalMs, &d.TTFTMs, &d.Input, &d.CacheRead, &write,
		&d.Output, &d.Reasoning, &failure, &sections, &injected, &context_, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return ctlproto.RequestDetail{}, ErrNoRequest
	}
	if err != nil {
		return ctlproto.RequestDetail{}, s.queryErr(ctx, err)
	}
	d.Stage, d.Model, d.Route, d.CacheWrite, d.Error = stage, model, route.String, write, failure
	d.RequestRaw = request.String

	// sections_json：[{id, source, chars, hash}]，全文按哈希在 prompt_sections
	var metas []struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Chars  int    `json:"chars"`
		Hash   string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(sections.String), &metas); err != nil {
		d.SectionsRaw = sections.String
		d.ParseErr = append(d.ParseErr, "sections_json："+err.Error())
		metas = nil
	}
	var hashes []string
	for _, m := range metas {
		if m.Hash != "" {
			hashes = append(hashes, m.Hash)
		}
	}
	texts, err := s.sectionTexts(ctx, hashes)
	if err != nil {
		return ctlproto.RequestDetail{}, err
	}
	for _, m := range metas {
		sec := ctlproto.RequestSection{ID: m.ID, Source: m.Source, Chars: m.Chars}
		if t, ok := texts[m.Hash]; ok {
			sec.Text = clipDetail(t)
		} else {
			sec.Missing = true
		}
		if m.ID == injectedSection {
			d.Injected = append(d.Injected, sec)
		} else {
			d.System = append(d.System, sec)
		}
	}

	// injected_json：宿主另外记的注入条目，形状不固定（目前后台常写空数组）：能当字符串就当，其余原样转成文本
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(injected.String), &items); err != nil {
		d.InjectedRaw = injected.String
		d.ParseErr = append(d.ParseErr, "injected_json："+err.Error())
	}
	for i, raw := range items {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = string(raw)
		}
		d.Injected = append(d.Injected, ctlproto.RequestSection{ID: fmt.Sprintf("injected-%d", i+1), Chars: len([]rune(text)), Text: clipDetail(text)})
	}

	// context_json：工具表和窗口都在 breakdown 里
	if strings.TrimSpace(context_.String) != "" {
		var cx struct {
			Estimate  int `json:"estimate"`
			Breakdown struct {
				Window int `json:"window"`
				Parts  struct {
					Tools struct {
						Tokens int                    `json:"tokens"`
						Items  []ctlproto.RequestTool `json:"items"`
					} `json:"tools"`
				} `json:"parts"`
			} `json:"breakdown"`
		}
		if err := json.Unmarshal([]byte(context_.String), &cx); err != nil {
			d.ContextRaw = context_.String
			d.ParseErr = append(d.ParseErr, "context_json："+err.Error())
		} else {
			d.Tools, d.ToolTokens = cx.Breakdown.Parts.Tools.Items, cx.Breakdown.Parts.Tools.Tokens
			d.Window, d.Estimate = cx.Breakdown.Window, cx.Estimate
		}
	}
	return d, nil
}

// sectionTexts 按哈希取提示词正文。只读连接只有一条，读完就放开。
func (s *Service) sectionTexts(ctx context.Context, hashes []string) (map[string]string, error) {
	out := map[string]string{}
	if len(hashes) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	args := make([]any, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	rows, err := s.db.QueryContext(ctx, `SELECT hash, text FROM prompt_sections WHERE hash IN (`+marks+`)`, args...)
	if err != nil {
		return nil, s.queryErr(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, t string
		if err := rows.Scan(&h, &t); err != nil {
			return nil, s.queryErr(ctx, err)
		}
		out[h] = t
	}
	if err := rows.Err(); err != nil {
		return nil, s.queryErr(ctx, err)
	}
	return out, nil
}

// clipDetail 把过长的正文截到上限（不切开一个字），末尾写明截掉了多少字节。
func clipDetail(t string) string {
	if len(t) <= maxDetailText {
		return t
	}
	cut := maxDetailText
	for cut > 0 && t[cut]&0xc0 == 0x80 {
		cut--
	}
	return t[:cut] + fmt.Sprintf("\n……[已截断，省略 %d 字节]", len(t)-cut)
}
