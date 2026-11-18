package desksettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sumika-native/internal/paths"
)

var ErrSettingsVersion = errors.New("桌面设置在别处改过，请重新读取")

func documentVersion(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Document 交出文件副本和内容版本。不存在的文件具有空内容版本。
// handleKey 属于内部密钥，不能作为设置值交给界面。
func (s *Store) Document() (map[string]json.RawMessage, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.File())
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	if err != nil {
		return nil, "", err
	}
	m := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return nil, "", errors.New("桌面设置文件读不懂")
		}
	}
	delete(m, keyHandle)
	return m, documentVersion(raw), nil
}

// SetSetting 只写注册表开放的桌面键；旧界面的 Set* 仍走同一事务。
func (s *Store) SetSetting(ctx context.Context, key string, value json.RawMessage, base string) error {
	var v any
	switch key {
	case "autoReceive":
		var on *bool
		if json.Unmarshal(value, &on) != nil || on == nil {
			return errors.New("自动接收需要开关值")
		}
		v = *on
	case "appearance":
		var a *Appearance
		if !json.Valid(value) {
			return errors.New("设置值不是完整 JSON")
		}
		dec := json.NewDecoder(bytes.NewReader(value))
		dec.DisallowUnknownFields()
		if dec.Decode(&a) != nil || a == nil {
			return errors.New("外观需要完整主题设置")
		}
		if _, _, err := a.Compile(); err != nil {
			return err
		}
		v = *a
	default:
		return errors.New("桌面设置不允许写这个键")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	raw, existed, err := readSnapshot(s.File())
	if err != nil {
		return err
	}
	if documentVersion(raw) != base {
		return ErrSettingsVersion
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := paths.Open(s.dir); err != nil {
		return fmt.Errorf("数据目录还不能写桌面设置：%w", err)
	}
	m := map[string]json.RawMessage{}
	if existed {
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return errors.New("桌面设置文件读不懂")
		}
	}
	m[key], err = json.Marshal(v)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeVerifiedFrom(s.File(), append(data, '\n'), raw, existed, replaceAtomic, os.ReadFile)
}

// readSnapshot distinguishes a missing file from an existing empty file.
func readSnapshot(file string) ([]byte, bool, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func writeVerified(file string, data []byte, replace func(string, []byte) (os.FileInfo, error), read func(string) ([]byte, error)) error {
	before, existed, err := readSnapshot(file)
	if err != nil {
		return err
	}
	return writeVerifiedFrom(file, data, before, existed, replace, read)
}
func writeVerifiedFrom(file string, data, before []byte, existed bool, replace func(string, []byte) (os.FileInfo, error), read func(string) ([]byte, error)) error {
	if existed {
		history := filepath.Join(filepath.Dir(file), "settings-history")
		if err := os.MkdirAll(history, 0700); err != nil {
			return err
		}
		backup, err := os.CreateTemp(history, "desktop-settings-*.json")
		if err != nil {
			return err
		}
		if err = backup.Chmod(0600); err == nil {
			_, err = backup.Write(before)
		}
		if err == nil {
			err = backup.Sync()
		}
		err = errors.Join(err, backup.Close())
		if err != nil {
			return err
		}
	}
	current, stillExists, err := readSnapshot(file)
	if err != nil {
		return err
	}
	if stillExists != existed || !bytes.Equal(current, before) {
		return ErrSettingsVersion
	}
	candidate, err := replace(file, data)
	if err != nil {
		return err
	}
	actual, err := read(file)
	if err == nil && bytes.Equal(actual, data) {
		return nil
	}
	failure := errors.New("桌面设置写后核对失败，已尝试回滚")
	currentInfo, statErr := os.Stat(file)
	if statErr != nil || candidate == nil || !os.SameFile(candidate, currentInfo) {
		return errors.Join(failure, err, errors.New("文件已被外部替换，保留现有文件"))
	}
	var rollback error
	if existed {
		_, rollback = replace(file, before)
	} else {
		rollback = os.Remove(file)
	}
	if rollback == nil {
		restored, e := os.ReadFile(file)
		if existed && (e != nil || !bytes.Equal(restored, before)) {
			rollback = errors.New("回滚后原文不一致")
		}
		if !existed && !errors.Is(e, os.ErrNotExist) {
			rollback = errors.New("回滚后新文件仍存在")
		}
	}
	if rollback != nil {
		return errors.Join(failure, err, fmt.Errorf("回滚失败：%w", rollback))
	}
	return errors.Join(failure, err)
}
