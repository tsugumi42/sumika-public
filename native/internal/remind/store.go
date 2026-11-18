package remind

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sumika-native/internal/fileatomic"
)

// State 是要存的全部内容
type State struct {
	NextID    int        `json:"nextId"`
	Reminders []Reminder `json:"reminders"`
}

// Store 存提醒表。调度器每次改动后整份 Save，数据量是几十行，不需要增量
type Store interface {
	Load() (State, error)
	Save(State) error
}

// MemoryStore 只放内存，给测试用；FailSave 不空时 Save 返回它（测写盘失败）
type MemoryStore struct {
	S        State
	FailSave error
}

func (m *MemoryStore) Load() (State, error) { return cloneState(m.S), nil }
func (m *MemoryStore) Save(s State) error {
	if m.FailSave != nil {
		return m.FailSave
	}
	m.S = cloneState(s)
	return nil
}

func cloneState(s State) State {
	out := State{NextID: s.NextID, Reminders: make([]Reminder, len(s.Reminders))}
	for i, r := range s.Reminders {
		out.Reminders[i] = r.clone()
	}
	return out
}

// FileStore 把提醒表存成一个 JSON 文件：先写同目录临时文件再换名，写到一半断电不会坏掉旧文件。
type FileStore struct{ Path string }

const fileVersion = 1

type fileDoc struct {
	Version int `json:"version"`
	State
}

func (f FileStore) Load() (State, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{NextID: 1}, nil
	}
	if err != nil {
		return State{}, err
	}
	var d fileDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		// 不覆盖原文件：交给宿主报错，由人决定怎么处理
		return State{}, fmt.Errorf("提醒文件 %s 读不懂：%w", f.Path, err)
	}
	if d.Version > fileVersion {
		return State{}, fmt.Errorf("提醒文件 %s 是更新的版本（%d），这个程序读不了", f.Path, d.Version)
	}
	if d.NextID < 1 {
		d.NextID = 1
	}
	return d.State, nil
}

func (f FileStore) Save(s State) error {
	raw, err := json.MarshalIndent(fileDoc{Version: fileVersion, State: s}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".reminders-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(raw)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(name)
		return errors.Join(werr, serr, cerr)
	}
	if err := fileatomic.Replace(name, f.Path, 2*time.Second); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
