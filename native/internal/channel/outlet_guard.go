package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
)

// guardActions 在落库前累计检查引用、媒体语义和转发文字；卡片不能成为文字守卫的旁路。
// 普通回合与复读仍使用原有裁决；后台动作不授予主人提问例外。
func (o *Outlet) guardActions(ctx context.Context, character string, conv types.Conversation, parts []types.Part) error {
	if conv.Channel == "console" && conv.Scene == types.ScenePrivate {
		return nil
	}
	needed := false
	for _, p := range parts {
		if p.Kind == types.PartForward || (p.Kind == types.PartMessage && (p.Quote != "" || hasMedia(p.Body))) {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a := AudienceOf(types.Batch{Conv: conv}, o.cfg.Roster)
	var passed []string
	check := func(text string) error {
		if o.cfg.GuardFor == nil {
			return fmt.Errorf("发送前守卫未接入")
		}
		why, err := safe(func() string {
			g := o.cfg.GuardFor(character)
			if g == nil {
				return "发送前守卫未接入"
			}
			full := strings.Join(append(passed, text), "\n")
			refs, err := g.CheckOutbound(ctx, full, a)
			if err != nil || ctx.Err() != nil {
				return "守卫没判成"
			}
			if len(refs) > 0 {
				return "守卫拦下"
			}
			return ""
		})
		if err != nil {
			return fmt.Errorf("守卫没判成")
		}
		if why != "" {
			return fmt.Errorf("发送前检查没过：%s", why)
		}
		passed = append(passed, text)
		return nil
	}
	checkBody := func(body []types.BodySegment) error {
		for _, s := range body {
			text := s.Text
			if s.Kind != types.BodyText {
				text = s.Semantic
			}
			if text != "" {
				if err := check(text); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, p := range parts {
		if p.Kind == types.PartMessage && (p.Quote != "" || hasMedia(p.Body)) {
			if err := checkBody(p.Body); err != nil {
				return err
			}
		}
		for _, node := range p.Nodes {
			if err := check(node.Name); err != nil {
				return err
			}
			if err := checkBody(node.Body); err != nil {
				return err
			}
		}
		if c := p.Card; !c.Empty() {
			for _, t := range append([]string{c.Source, c.Summary, c.Prompt}, c.News...) {
				if t == "" {
					continue
				}
				if err := check(t); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func hasMedia(body []types.BodySegment) bool {
	for _, s := range body {
		if s.Kind != types.BodyText {
			return true
		}
	}
	return false
}
