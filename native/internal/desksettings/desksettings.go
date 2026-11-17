// Package desksettings 保存桌面程序的本机习惯，文件是 <数据目录>/go/desktop-settings.json。
//
// 包含启动后自动接收 QQ（默认关）、分栏布局、外观主题，以及群句柄 HMAC 用的本机 handleKey。
// 这些属于桌面程序，不写用户手工维护的 sumika.yml；同一个 Store 串行保留其他键并原子写回。
//
// 写法：先写同目录下的临时文件再改名，读到的要么是旧的、要么是新的，不会是半个文件；文件里没认识的键原样留着
// （以后加开关，旧版本改这个文件不会把新开关抹掉）。数据目录没有身份证（还没初始化）时不写，也不建 go/：
// 先建目录会让之后的 receive 把它当成"已经有目录、认不出身份证"（filelog 里写过同一个坑）。
package desksettings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sumika-native/internal/fileatomic"
	"sumika-native/internal/paths"
)

const (
	keyAutoReceive = "autoReceive"
	keyHandle      = "handleKey"
)

// 不同 Store（托盘和后台）也必须串行修改同一份文件。
var settingsWriteMu sync.Mutex

// Store 是一个数据目录里的桌面开关。同一个进程里托盘和命令行都经过同一个 Store，写的时候用锁串起来
type Store struct {
	dir string
	mu  sync.Mutex
	key []byte // handleKey 的缓存
}

// New 不碰磁盘；dir 是数据目录
func New(dir string) *Store { return &Store{dir: dir} }

// Dir 是这个 Store 管着的数据目录
func (s *Store) Dir() string { return s.dir }

// File 是开关文件的位置（给日志和错误文字用）
func (s *Store) File() string { return paths.Paths{Root: s.dir}.DesktopSettingsFile() }

// AutoReceive 读"启动后自动接收 QQ"。文件不存在是默认值（关）；文件读不懂或这一项不是 true / false 也按关，同时返回错误，
// 让调用方写一行日志——坏文件不能让程序起不来，也不能悄悄当成"开"
func (s *Store) AutoReceive() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return false, err
	}
	raw, ok := m[keyAutoReceive]
	if !ok {
		return false, nil
	}
	var on *bool // 指针：null 也要认出来，不然 null 读成 false 却不报错
	if err := json.Unmarshal(raw, &on); err != nil || on == nil {
		return false, fmt.Errorf("%s 里的 %s 不是 true 或 false", s.File(), keyAutoReceive)
	}
	return *on, nil
}

// SetAutoReceive 改"启动后自动接收 QQ"。原文件读不懂时不报错，直接用新内容换掉它：这个文件只有程序自己写，用户明确点了开或关
func (s *Store) SetAutoReceive(on bool) error { return s.setKey(keyAutoReceive, on) }

// setKey 改文件里的一项：读出整个文件、换这一项、原子写回；其他键原样留着。数据目录没有身份证时不写；
// 原文件读不懂时用只含这一项的新内容换掉它（已经在用的句柄密钥带着）
func (s *Store) setKey(key string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	return s.setKeyLocked(key, v)
}

func (s *Store) setKeyLocked(key string, v any) error {
	if _, err := paths.Open(s.dir); err != nil {
		return fmt.Errorf("数据目录还不能写桌面设置：%w", err)
	}
	raw, existed, err := readSnapshot(s.File())
	if err != nil {
		return err
	}
	m := map[string]json.RawMessage{}
	if existed {
		err = json.Unmarshal(raw, &m)
		if m == nil && err == nil {
			err = errors.New("桌面设置不是对象")
		}
	}
	if err != nil {
		m = map[string]json.RawMessage{}
		if s.key != nil { // 文件读坏被换掉时，已经在用的句柄密钥带着，不让句柄换一茬
			m[keyHandle], _ = json.Marshal(hex.EncodeToString(s.key))
		}
	}
	m[key], _ = json.Marshal(v)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeVerifiedFrom(s.File(), append(b, '\n'), raw, existed, replaceAtomic, os.ReadFile)
}

// HandleKeyLen 是句柄密钥的字节数
const HandleKeyLen = 32

// HandleKey 是这台机器上的随机密钥（32 字节，第一次要用时生成，存在设置文件的 handleKey 里，之后一直是它）。
// sumika-ctl 的 gate list 不给完整群号，给群的"句柄"：HMAC-SHA256(这个密钥, 会话号) 的前 8 个十六进制字符。
// 密钥不出这台机器、不进任何回复和日志，所以拿到句柄反推不出群号（群号只有十来位，没有密钥的哈希是能穷举的）。
// 数据目录还没有身份证（没初始化）时不生成、不建 go/，返回错误；文件里的值不是 64 个十六进制字符就当没有，重新生成
func (s *Store) HandleKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	if s.key != nil {
		return append([]byte(nil), s.key...), nil
	}
	snapshot, existed, loadErr := readSnapshot(s.File())
	if loadErr != nil {
		return nil, loadErr
	}
	m := map[string]json.RawMessage{}
	if existed {
		loadErr = json.Unmarshal(snapshot, &m)
		if m == nil && loadErr == nil {
			loadErr = errors.New("桌面设置不是对象")
		}
	}
	if loadErr == nil {
		if raw, ok := m[keyHandle]; ok {
			var h string
			if json.Unmarshal(raw, &h) == nil {
				if b, err := hex.DecodeString(h); err == nil && len(b) == HandleKeyLen {
					s.key = b
					return append([]byte(nil), b...), nil
				}
			}
		}
	}
	if _, err := paths.Open(s.dir); err != nil {
		return nil, fmt.Errorf("数据目录还不能存群句柄的密钥：%w", err)
	}
	if loadErr != nil {
		m = map[string]json.RawMessage{} // 读坏的文件换成新的（这个文件只有程序自己写）
	}
	key := make([]byte, HandleKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成群句柄的密钥：%w", err)
	}
	m[keyHandle], _ = json.Marshal(hex.EncodeToString(key))
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeVerifiedFrom(s.File(), append(b, '\n'), snapshot, existed, replaceAtomic, os.ReadFile); err != nil {
		return nil, err
	}
	s.key = key
	return append([]byte(nil), key...), nil
}

// load 读整个文件。不存在是空表
func (s *Store) load() (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(s.File())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, fmt.Errorf("%s 读不懂", s.File())
	}
	return m, nil
}

// replaceAtomic 写同目录下的临时文件，再改名盖过去；失败不留临时文件。
func replaceAtomic(file string, data []byte) (os.FileInfo, error) {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(file)+".tmp-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	info, statErr := tmp.Stat()
	if statErr != nil {
		tmp.Close()
		os.Remove(name)
		return nil, statErr
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fileatomic.Replace(name, file, renameRetryFor)
	}
	if err != nil {
		os.Remove(name)
	}
	return info, err
}

// renameRetryFor 是改名盖过去最多重试多久（测试里缩短）
var renameRetryFor = 2 * time.Second
