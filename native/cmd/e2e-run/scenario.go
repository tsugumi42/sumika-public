package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

//go:embed scenarios/*.json
var assets embed.FS

type scenario struct {
	Name  string `json:"name"`
	Steps []step `json:"steps"`
}
type step struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Scene       string `json:"scene,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Text        string `json:"text,omitempty"`
	At          bool   `json:"at,omitempty"`
	Image       bool   `json:"image,omitempty"`
	Reply       string `json:"reply,omitempty"`
	Target      string `json:"target,omitempty"`
	WaitMS      int    `json:"wait_ms,omitempty"`
	ExpectReply bool   `json:"expect_reply,omitempty"`
}

func loadScenario(path string) (scenario, []byte, error) {
	var b []byte
	var err error
	if path == "full" || path == "smoke" {
		b, err = assets.ReadFile("scenarios/" + path + ".json")
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return scenario{}, nil, err
	}
	s, err := parseScenario(b)
	return s, b, err
}
func parseScenario(b []byte) (scenario, error) {
	var s scenario
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, fmt.Errorf("剧本末尾有多余内容")
	}
	if s.Name == "" || len(s.Steps) == 0 {
		return s, fmt.Errorf("剧本需要 name 和非空 steps")
	}
	seen := map[string]step{}
	for _, v := range s.Steps {
		if v.ID == "" {
			return s, fmt.Errorf("步骤缺 id")
		}
		if _, ok := seen[v.ID]; ok {
			return s, fmt.Errorf("重复 id %s", v.ID)
		}
		if v.WaitMS < 0 || v.WaitMS > 600000 {
			return s, fmt.Errorf("%s wait_ms 要在 0～600000", v.ID)
		}
		switch v.Kind {
		case "message", "offline":
			if v.Scene != "private" && v.Scene != "group" {
				return s, fmt.Errorf("%s scene 只认 private/group", v.ID)
			}
			if v.Actor != "owner" && v.Actor != "friend" {
				return s, fmt.Errorf("%s actor 只认 owner/friend", v.ID)
			}
			if v.Text == "" && !v.Image {
				return s, fmt.Errorf("%s 消息为空", v.ID)
			}
			if v.Kind == "offline" && v.ExpectReply {
				return s, fmt.Errorf("%s 离线消息不能要求实时回复", v.ID)
			}
			if v.Reply != "" {
				old, ok := seen[v.Reply]
				if !ok || old.Kind != "message" || old.Scene != v.Scene || (v.Scene == "private" && old.Actor != v.Actor) {
					return s, fmt.Errorf("%s 引用必须指向前面的同会话消息", v.ID)
				}
			}
		case "recall":
			old, ok := seen[v.Target]
			if !ok || old.Kind != "message" {
				return s, fmt.Errorf("%s 撤回必须指向前面的消息", v.ID)
			}
		case "disconnect":
			if v.WaitMS == 0 {
				return s, fmt.Errorf("%s 断线要指定 wait_ms", v.ID)
			}
		default:
			return s, fmt.Errorf("%s 未知 kind %q", v.ID, v.Kind)
		}
		seen[v.ID] = v
	}
	return s, nil
}
func (s step) wait() time.Duration { return time.Duration(s.WaitMS) * time.Millisecond }
func conversation(s step) string {
	if s.Scene == "group" {
		return "qq:group:300"
	}
	id := "200"
	if s.Actor == "friend" {
		id = "400"
	}
	return "qq:private:100:" + id
}
