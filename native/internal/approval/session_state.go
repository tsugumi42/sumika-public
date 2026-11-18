package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"sumika-native/internal/policy"
)

// 会话审批档和“这个对话里都允许”重启后保留（规格-审批分级 8.2、ADR-342 里“没做”的第一项）。
//
// 两份东西都存进 data/go/approval-sessions.json：整份换名写（写到一半被杀，原文件原样留着），
// 几十行的量，不占数据库迁移号。只存本地私聊的会话（console: 开头、不是群）；读回来时再筛一遍，
// 手写塞进去的 QQ 会话、群聊、不认识的档一律丢掉——重启恢复的人不能比重启前更多。
// 不存“最近用过”的时间（Touch）：它只影响规则页的排序，不值得每次放行都写盘。

// localPrivateKey：存与读都只认本地私聊的会话号（console:<角色> 或 console:<角色>:topic:<话题>）
func localPrivateKey(conversation string) bool {
	return strings.HasPrefix(conversation, "console:") && !strings.Contains(conversation, ":group:")
}

type sessionFile struct {
	Modes map[string]string `json:"modes,omitempty"` // 会话 → 审批档
	Conv  []Rule            `json:"conv,omitempty"`  // “这个对话里都允许”
}

// sessionState 管这个文件：自己一把锁，每次存都取当时的最新快照，所以并发存最后落盘的一定是最新的
type sessionState struct {
	mu   sync.Mutex
	file string
	g    *Gate
}

// UseSessionFile 把会话审批档和“这个对话”的放行接到文件上：读回上次的、之后每次改动都写下去。
// 返回的错误不致命（读不了就当没有，原文件格式坏了改名留存）；写失败走 SessionSaveErr，不影响放行。
func (g *Gate) UseSessionFile(file string) error {
	st := &sessionState{file: file, g: g}
	loadErr := st.load()
	g.sessions = st
	g.Conv.onChange = func() {
		if err := st.save(); err != nil && g.SessionSaveErr != nil {
			g.SessionSaveErr(err)
		}
	}
	return loadErr
}

func (s *sessionState) load() error {
	b, err := os.ReadFile(s.file)
	// 上级目录的位置被文件占着时，Windows 报“路径不存在”，Linux 报 ENOTDIR；都是“没有记录”，写的时候再报写失败
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读不了会话审批档的记录，这次运行里不会改写它：%w", err)
	}
	var raw sessionFile
	if err := json.Unmarshal(b, &raw); err != nil {
		kept := fmt.Sprintf("%s.坏-%d", s.file, time.Now().UnixMilli())
		if rerr := os.Rename(s.file, kept); rerr != nil {
			return fmt.Errorf("会话审批档的记录格式坏了，也没能改名留存（%v）：%w", rerr, err)
		}
		return fmt.Errorf("会话审批档的记录格式坏了，已改名留存为 %s，各会话的审批档和“这个对话”需要重新选：%w", filepath.Base(kept), err)
	}
	g := s.g
	g.modeMu.Lock()
	for conv, mode := range raw.Modes {
		if localPrivateKey(conv) && ValidMode(mode) {
			if g.sessionModes == nil {
				g.sessionModes = map[string]string{}
			}
			g.sessionModes[conv] = mode
		}
	}
	g.modeMu.Unlock()
	for _, r := range raw.Conv {
		if localPrivateKey(r.Conversation) && r.Key != "" {
			g.Conv.restore(r)
		}
	}
	return nil
}

// save 把现在的两份全写下去
func (s *sessionState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write()
}

func (s *sessionState) write() error {
	g := s.g
	g.modeMu.RLock()
	modes := make(map[string]string, len(g.sessionModes))
	for k, v := range g.sessionModes {
		modes[k] = v
	}
	g.modeMu.RUnlock()
	out := sessionFile{Modes: modes, Conv: g.Conv.List()}
	if len(out.Modes) == 0 && len(out.Conv) == 0 {
		if err := os.Remove(s.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	return writeAtomic(s.file, b)
}

// restore 读回一条“这个对话”：只认本地私聊，范围固定是 conversation，其余原样
func (c *ConvGrants) restore(r Rule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[r.Conversation] == nil {
		c.m[r.Conversation] = map[string]Rule{}
	}
	r.Scope = policy.ByConversation
	r.Dirs = append([]string(nil), r.Dirs...)
	c.m[r.Conversation][r.Key] = r
}
