package desksettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryDesktopWrites(t *testing.T) {
	s := New(newDir(t))
	writeFile(t, s, `{"future":{"keep":7},"autoReceive":false,"handleKey":"secret"}`)
	doc, version, err := s.Document()
	if err != nil || doc["handleKey"] != nil {
		t.Fatalf("document: %v", err)
	}
	if err := s.SetSetting(context.Background(), "autoReceive", json.RawMessage("true"), version); err != nil {
		t.Fatal(err)
	}
	if on, err := New(s.Dir()).AutoReceive(); err != nil || !on {
		t.Fatalf("reopen: %v %v", on, err)
	}
	if err := s.SetSetting(context.Background(), "autoReceive", json.RawMessage("false"), version); !errors.Is(err, ErrSettingsVersion) {
		t.Fatalf("stale: %v", err)
	}
	_, version, _ = s.Document()
	appearance := DefaultAppearance()
	appearance.Mode = "night"
	raw, _ := json.Marshal(appearance)
	if err := s.SetSetting(context.Background(), "appearance", raw, version); err != nil {
		t.Fatal(err)
	}
	got, err := New(s.Dir()).Appearance()
	if err != nil || got.Mode != "night" {
		t.Fatalf("appearance: %+v %v", got, err)
	}
	final, _, _ := s.Document()
	if !bytes.Equal(final["future"], []byte("{\n    \"keep\": 7\n  }")) {
		var obj map[string]int
		if json.Unmarshal(final["future"], &obj) != nil || obj["keep"] != 7 {
			t.Fatal("unknown key lost")
		}
	}
	backups, err := os.ReadDir(filepath.Join(filepath.Dir(s.File()), "settings-history"))
	if err != nil || len(backups) != 2 {
		t.Fatalf("backups=%d err=%v", len(backups), err)
	}
}

func TestRegistryDesktopRejectsMalformedValues(t *testing.T) {
	s := New(newDir(t))
	for _, ch := range []struct{ key, value string }{
		{"autoReceive", "null"}, {"autoReceive", "0"}, {"autoReceive", `"true"`},
		{"appearance", "null"}, {"appearance", `{}`}, {"appearance", `{"mode":"night","unexpected":true}`},
		{"handleKey", `"leak"`}, {"unknown", "true"},
	} {
		_, version, _ := s.Document()
		if err := s.SetSetting(context.Background(), ch.key, json.RawMessage(ch.value), version); err == nil {
			t.Fatalf("accepted %s %s", ch.key, ch.value)
		}
	}
	if _, err := os.Stat(s.File()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected values created file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, version, _ := s.Document()
	if err := s.SetSetting(ctx, "autoReceive", json.RawMessage("true"), version); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDesktopReadbackRollback(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[exists], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "settings.json")
			before := []byte("{\"autoReceive\":false}\n")
			if exists {
				if err := os.WriteFile(file, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			corruptRead := func(path string) ([]byte, error) {
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					return nil, err
				}
				return os.ReadFile(path)
			}
			err := writeVerified(file, []byte(`{"autoReceive":true}`), replaceAtomic, corruptRead)
			if err == nil {
				t.Fatal("corrupt write accepted")
			}
			after, e := os.ReadFile(file)
			if exists && (e != nil || !bytes.Equal(after, before)) {
				t.Fatalf("not restored: %q %v", after, e)
			}
			if !exists && !errors.Is(e, os.ErrNotExist) {
				t.Fatal("new file not removed")
			}
		})
	}
}

func TestDesktopRollbackFailureIsReported(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	writes := 0
	replace := func(path string, b []byte) (os.FileInfo, error) {
		writes++
		if writes == 2 {
			return nil, errors.New("fixture restore failed")
		}
		return replaceAtomic(path, b)
	}
	err := writeVerified(file, []byte("new"), replace, func(string) ([]byte, error) { return nil, errors.New("fixture read failed") })
	if err == nil || !strings.Contains(err.Error(), "回滚失败") {
		t.Fatalf("lost rollback failure: %v", err)
	}
}

func TestDesktopVersionCheckedAgainBeforeReplace(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"autoReceive":false}`)
	external := []byte(`{"autoReceive":false,"future":1}`)
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	before, exists, err := readSnapshot(file)
	if err != nil {
		t.Fatal(err)
	}
	if documentVersion(before) != documentVersion(original) {
		t.Fatal("unexpected base")
	}
	if err := os.WriteFile(file, external, 0600); err != nil {
		t.Fatal(err)
	}
	err = writeVerifiedFrom(file, []byte(`{"autoReceive":true}`), before, exists, replaceAtomic, os.ReadFile)
	if !errors.Is(err, ErrSettingsVersion) {
		t.Fatalf("expected conflict: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, external) {
		t.Fatalf("external change lost: %q %v", got, err)
	}
}

func TestDesktopExternalReplacementIsNotRolledBack(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	before := []byte(`{"autoReceive":false}`)
	external := []byte(`{"external":true}`)
	if err := os.WriteFile(file, before, 0600); err != nil {
		t.Fatal(err)
	}
	read := func(path string) ([]byte, error) {
		other := path + ".external"
		if err := os.WriteFile(other, external, 0600); err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := os.Rename(other, path); err != nil {
			return nil, err
		}
		return os.ReadFile(path)
	}
	err := writeVerified(file, []byte(`{"autoReceive":true}`), replaceAtomic, read)
	if err == nil {
		t.Fatal("expected conflict")
	}
	got, e := os.ReadFile(file)
	if e != nil || !bytes.Equal(got, external) {
		t.Fatalf("external file lost: %q %v", got, e)
	}
}
