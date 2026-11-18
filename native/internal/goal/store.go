package goal

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/fileatomic"
)

// Store 是目标的持久化接口，存哪由宿主注入。
// Load 找不到返回 (nil, nil)；Delete 不存在不算错。
type Store interface {
	Load(session string) (*Goal, error)
	Save(g *Goal) error
	Delete(session string) error
	List() ([]string, error)
}

// FileStore 把每个会话的目标存成目录下的一个 JSON 文件。文件名是会话号的十六进制，避开非法字符。
type FileStore struct{ Dir string }

func NewFileStore(dir string) *FileStore { return &FileStore{Dir: dir} }

func (s *FileStore) path(session string) string {
	return filepath.Join(s.Dir, hex.EncodeToString([]byte(session))+".json")
}

func (s *FileStore) Load(session string) (*Goal, error) {
	b, err := os.ReadFile(s.path(session))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g Goal
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("读取目标文件 %s 失败：%w", filepath.Base(s.path(session)), err)
	}
	if g.Session != session {
		return nil, fmt.Errorf("目标文件 %s 的会话号对不上", filepath.Base(s.path(session)))
	}
	return &g, nil
}

func (s *FileStore) Save(g *Goal) error {
	if g == nil || g.Session == "" {
		return errors.New("目标没有会话号")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	p := s.path(g.Session)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := fileatomic.Replace(tmp, p, 2*time.Second); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *FileStore) Delete(session string) error {
	err := os.Remove(s.path(session))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *FileStore) List() ([]string, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSuffix(n, ".json"))
		if err != nil {
			continue
		}
		out = append(out, string(raw))
	}
	return out, nil
}
