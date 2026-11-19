package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"sumika-native/internal/fileatomic"
)

func TestSettingSaveEveryKey(t *testing.T) {
	raw := []byte("# root comment\nunknown: {keep: 'quoted # value'}\nqq:\n  accounts:\n    - id: '100' # account id\n      character: old\n      url: ws://old\n      token: secret-value\n      future: keep\n  groups:\n    - '200' # group comment\n  privateAllow: ['300']\nplugins:\n  sessions:\n    'qq:group:200':\n      repeater: true # switch comment\nreply:\n  deny:\n    'qq:400': bot # deny comment\n")
	cases := []struct {
		key   string
		value any
	}{
		{"magpie.baseUrl", "https://gateway.invalid/v1"}, {"qq.owner", json.Number("9007199254740993")},
		{"qq.accounts", []any{map[string]any{"id": "100", "character": "new", "url": "ws://new"}, map[string]any{"id": json.Number("500"), "character": "other"}}},
		{"qq.groups", []any{json.Number("9007199254740993"), "201"}}, {"qq.privateAllow", []any{"301"}},
		{"qq.reply", true}, {"qq.goSend", true}, {"qq.qqDir", "D:/qq"}, {"qq.qqDataDir", "D:/data"},
		{"qq.protocolDir", "D:/protocol"}, {"qq.protocolLogDir", "D:/log"},
		{"qq.media.download", false}, {"qq.media.ffmpeg", "ffmpeg-custom"}, {"qq.media.ffprobe", "ffprobe-custom"},
		{"qq.backfill.enabled", false}, {"qq.backfill.groups", false}, {"qq.forward.fetch", false},
		{"qq.remoteApproval.enabled", true}, {"qq.remoteApproval.awayAfterSeconds", json.Number("120")},
		{"notifications.goal", false}, {"notifications.loop", false}, {"notifications.approval", false}, {"notifications.question", false}, {"notifications.reminder", false},
		{"group.frequency", json.Number("0")}, {"group.jitter", json.Number("0.1")}, {"group.mode", "speak"},
		{"plugins.sessions", map[string]any{"qq:group:200": map[string]any{"poke": false}}},
		{"reply.deny", map[string]any{"qq:401": "ai"}}, {"linkReader.proxy", "http://127.0.0.1:8080"},
		{"linkReader.expand.startEmoji", "123"},
		{"location", map[string]any{"name": "编的市", "city": "编的市", "latitude": json.Number("31.5"), "longitude": json.Number("121.25")}},
		{"tools.approvalMode", "auto"}, {"tools.lineEnding", "crlf"},
		{"embed.modelsRoot", "D:/Models"},
		{"web.searchOrder", []any{"tavily", "deepseek", "duckduckgo"}}, {"web.fetchOrder", []any{"deepseek", "http"}},
		{"freeApi.probe.enabled", false}, {"freeApi.probe.intervalHours", json.Number("24")},
		{"freeApi.limits.weather.perActorDaily", json.Number("30")}, {"freeApi.limits.weather.perConversationHourly", json.Number("10")},
		{"freeApi.limits.exchangeRate.perActorDaily", json.Number("30")}, {"freeApi.limits.exchangeRate.perConversationHourly", json.Number("10")},
		{"freeApi.limits.calendar.perActorDaily", json.Number("5")}, {"freeApi.limits.calendar.perConversationHourly", json.Number("0")},
	}
	if len(cases) != len(settingKinds) {
		t.Fatal("missing whitelist case")
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			file, history := filepath.Join(dir, "sumika.yml"), filepath.Join(dir, "history")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if !WritableSetting(tc.key) {
				t.Fatal("missing whitelist entry")
			}
			got, err := SaveSetting(context.Background(), file, history, ContentVersion(raw), SettingChange{tc.key, tc.value}, time.Unix(1234, 0))
			if err != nil {
				t.Fatal(err)
			}
			onDisk, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(onDisk, file)
			if err != nil || !reflect.DeepEqual(got, parsed) {
				t.Fatalf("saved config did not reload: %v", err)
			}
			if !bytes.Contains(onDisk, []byte("# root comment")) || !bytes.Contains(onDisk, []byte("unknown: {keep: 'quoted # value'}")) {
				t.Fatal("unrelated content lost")
			}
			var doc map[string]any
			if err := yaml.Unmarshal(onDisk, &doc); err != nil {
				t.Fatal(err)
			}
			var value any = doc
			for _, part := range strings.Split(tc.key, ".") {
				m, ok := value.(map[string]any)
				if !ok {
					t.Fatal("missing saved path")
				}
				value = m[part]
			}
			if value == nil {
				t.Fatal("missing value")
			}
			if tc.key == "qq.accounts" {
				if len(got.QQ.Accounts) != 2 || got.QQ.Accounts[0].Token != "secret-value" || got.QQ.Accounts[0].Character != "new" || got.QQ.Accounts[1].ID != "500" || got.QQ.Accounts[1].Token != "" || !bytes.Contains(onDisk, []byte("future")) || !bytes.Contains(onDisk, []byte("# account id")) {
					t.Fatal("account merge lost data")
				}
			} else if tc.key == "group.frequency" {
				if got.Group.Frequency != 0 {
					t.Fatal("zero frequency was not saved")
				}
			} else if tc.key == "qq.remoteApproval.awayAfterSeconds" {
				if got.QQ.RemoteApproval.AwayAfterSeconds != 120 {
					t.Fatal("wait seconds were not saved")
				}
			} else {
				want := tc.value
				if tc.key == "qq.owner" {
					want = "9007199254740993"
				}
				if tc.key == "qq.groups" {
					want = []any{"9007199254740993", "201"}
				}
				if tc.key == "group.jitter" {
					want = 0.1
				}
				if tc.key == "location" {
					want = map[string]any{"name": "编的市", "city": "编的市", "latitude": 31.5, "longitude": 121.25}
				}
				if n, ok := tc.value.(json.Number); ok && strings.HasPrefix(tc.key, "freeApi.") {
					i, _ := n.Int64()
					want = int(i)
				}
				b, err := yaml.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				var normalized any
				if err := yaml.Unmarshal(b, &normalized); err != nil || !reflect.DeepEqual(value, normalized) {
					t.Fatalf("wrong saved value %s", tc.key)
				}
			}
			entries, err := os.ReadDir(history)
			if err != nil || len(entries) != 1 {
				t.Fatal("missing backup", err)
			}
			backup, err := os.ReadFile(filepath.Join(history, entries[0].Name()))
			if err != nil || !bytes.Equal(backup, raw) {
				t.Fatal("incorrect backup", err)
			}
		})
	}
}

func TestSettingInputBoundaries(t *testing.T) {
	raw := []byte("unknown: keep\nqq:\n  accounts:\n    - id: '100'\n      character: old\n      token: top-secret-sentinel\n")
	bad := []SettingChange{
		{"qq.token", "leak"}, {"qq.accounts.100.url", "x"}, {"qq.media.bad", true}, {"qq.backfill", map[string]any{}},
		{"qq.reply", nil}, {"qq.reply", "true"}, {"group.frequency", "0.5"}, {"group.frequency", json.Number("1.1")},
		{"group.jitter", json.Number("-0.1")}, {"group.mode", "top-secret-sentinel"}, {"qq.owner", "0"}, {"qq.owner", float64(9007199254740993)},
		{"qq.owner", json.Number("1.0")}, {"qq.groups", []any{nil}}, {"qq.groups", []any{float64(42)}}, {"qq.groups", "123"},
		{"qq.accounts", nil}, {"qq.accounts", []any{nil}}, {"qq.accounts", []any{map[string]any{"id": "100", "character": "x", "token": "top-secret" + "-sentinel"}}},
		{"qq.accounts", []any{map[string]any{"id": "100", "character": "x", "unknown": true}}},
		{"qq.accounts", []any{map[string]any{"id": "100", "character": nil}}},
		{"qq.accounts", []any{map[string]any{"id": "100", "character": "x"}, map[string]any{"id": "0100", "character": "y"}}},
		{"plugins.sessions", map[string]any{"qq:group:123": nil}}, {"plugins.sessions", map[string]any{"qq:group:123": map[string]any{"poke": nil}}},
		{"plugins.sessions", map[string]any{"qq:group:123": map[string]any{"unknown": true}}},
		{"reply.deny", map[string]any{"qq:123": nil}}, {"reply.deny", map[string]any{"qq:123": "wrong"}},
		{"magpie.baseUrl", "http://"}, {"linkReader.proxy", "https://bad.invalid"}, {"linkReader.expand.startEmoji", "0"},
	}
	for _, ch := range bad {
		_, _, err := PatchSetting(raw, "fixture", ch)
		if err == nil {
			t.Fatalf("accepted invalid value for %s", ch.Key)
		}
		if strings.Contains(err.Error(), "top-secret-sentinel") {
			t.Fatal("error leaked secret")
		}
	}
	for _, raw := range []string{"qq: null\n", "qq: []\n", "qq: &anchor {reply: true}\nother: *anchor\n", "qq: {}\n---\nunknown: 1\n"} {
		if _, _, err := PatchSetting([]byte(raw), "fixture", SettingChange{"qq.reply", false}); err == nil {
			t.Fatal("accepted unsafe YAML shape")
		}
	}
	for _, ch := range []SettingChange{{"qq.groups", []any{}}, {"qq.accounts", []any{}}, {"reply.deny", map[string]any{}}, {"plugins.sessions", map[string]any{}}, {"group.frequency", json.Number("0")}, {"qq.reply", false}} {
		if _, _, err := PatchSetting(raw, "fixture", ch); err != nil {
			t.Fatalf("valid empty or zero %s: %v", ch.Key, err)
		}
	}
}

func TestSettingCollectionsRetainComments(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		raw := []byte(strings.ReplaceAll("plugins:\n  sessions:\n    'qq:group:200': # group\n      repeater: true # repeater\n      poke: false\nother: 'untouched # text'\n", "\n", nl))
		out, _, err := PatchSetting(raw, "fixture", SettingChange{"plugins.sessions", map[string]any{"qq:group:201": map[string]any{"poke": true}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"# group", "# repeater", "other: 'untouched # text'"} {
			if !bytes.Contains(out, []byte(s)) {
				t.Fatalf("lost %s", s)
			}
		}
		if nl == "\r\n" && bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
			t.Fatal("mixed line endings")
		}
	}
}

func TestSettingSaveFailuresAndRollback(t *testing.T) {
	for _, mode := range []string{"version", "cancel", "backup", "corrupt", "corrupt-model", "corrupt-link", "read", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			file, history := filepath.Join(dir, "sumika.yml"), filepath.Join(dir, "history")
			raw := []byte("unknown: preserve\nqq: {reply: false}\n")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			base := ContentVersion(raw)
			if mode == "version" {
				base = "stale"
			}
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "backup" {
				if err := os.WriteFile(history, []byte("block directory"), 0600); err != nil {
					t.Fatal(err)
				}
				history = filepath.Join(history, "nested")
			}
			reads, replaces := 0, 0
			io := configFileIO{
				read: func(path string) ([]byte, error) {
					reads++
					if mode == "read" && reads == 3 {
						return nil, errors.New("injected read failure")
					}
					return os.ReadFile(path)
				},
				replace: func(src, dst string, timeout time.Duration) error {
					replaces++
					if mode == "rollback" && replaces == 2 {
						return errors.New("injected rollback failure")
					}
					if err := fileatomic.Replace(src, dst, timeout); err != nil {
						return err
					}
					if replaces == 1 && (strings.HasPrefix(mode, "corrupt") || mode == "rollback") {
						return os.WriteFile(dst, []byte("corrupted"), 0600)
					}
					return nil
				},
			}
			_, err := saveConfigWithIO(ctx, file, history, base, time.Unix(1234, 0), func(b []byte) ([]byte, Config, error) {
				switch mode {
				case "corrupt-model":
					return PatchModel(b, file, ModelChange{Field: BackgroundThinking, String: new("high")})
				case "corrupt-link":
					return PatchLink(b, file, LinkChange{Key: LinkEnabled, Bool: true})
				default:
					return PatchSetting(b, file, SettingChange{"qq.reply", true})
				}
			}, io)
			if err == nil {
				t.Fatal("expected failure")
			}
			if mode == "version" && !errors.Is(err, ErrConfigVersion) {
				t.Fatal(err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode == "rollback" && !strings.Contains(err.Error(), "回滚失败") {
				t.Fatal("rollback failure omitted")
			}
			current, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			if mode != "rollback" && !bytes.Equal(current, raw) {
				t.Fatal("original file not restored")
			}
			if strings.HasPrefix(mode, "corrupt") || mode == "read" || mode == "rollback" {
				entries, e := os.ReadDir(history)
				if e != nil || len(entries) != 1 {
					t.Fatal("missing original backup", e)
				}
				backup, e := os.ReadFile(filepath.Join(history, entries[0].Name()))
				if e != nil || !bytes.Equal(backup, raw) {
					t.Fatal("incorrect original backup", e)
				}
			}
		})
	}
}

func TestSettingPreservesProjectedURLs(t *testing.T) {
	raw := []byte("magpie: {baseUrl: 'https://user:secret@gateway.invalid/v1?key=secret#fragment'}\nlinkReader: {proxy: 'http://proxy.invalid:8080?key=secret#fragment'}\nqq:\n  accounts:\n    - id: '100'\n      character: old\n      url: 'ws://user:secret@server.invalid/path?key=secret#fragment'\n      token: " + "secret-token" + "\n")
	for _, tc := range []struct {
		ch    SettingChange
		check func(Config) bool
	}{
		{SettingChange{"magpie.baseUrl", "https://gateway.invalid/v1"}, func(c Config) bool {
			return c.Magpie.BaseURL == "https://user:secret@gateway.invalid/v1?key=secret#fragment"
		}},
		{SettingChange{"linkReader.proxy", "http://proxy.invalid:8080"}, func(c Config) bool { return c.LinkReader.Proxy == "http://proxy.invalid:8080?key=secret#fragment" }},
		{SettingChange{"qq.accounts", []any{map[string]any{"id": "100", "character": "new", "url": "ws://server.invalid/path"}}}, func(c Config) bool {
			return c.QQ.Accounts[0].Character == "new" && c.QQ.Accounts[0].URL == "ws://user:secret@server.invalid/path?key=secret#fragment" && c.QQ.Accounts[0].Token == "secret-token"
		}},
		{SettingChange{"qq.accounts", []any{map[string]any{"id": "100", "character": "new", "url": "ws://new.invalid"}}}, func(c Config) bool {
			return c.QQ.Accounts[0].URL == "ws://new.invalid" && c.QQ.Accounts[0].Token == "secret-token"
		}},
	} {
		_, c, err := PatchSetting(raw, "fixture", tc.ch)
		if err != nil || !tc.check(c) {
			t.Fatalf("URL change %s failed: %v", tc.ch.Key, err)
		}
	}
}

func TestSettingPresenceIncludesSequenceMembers(t *testing.T) {
	c, err := Parse([]byte("qq:\n  accounts:\n    - id: '100'\n      character: first\n      token: ''\n    - id: '200'\n      character: second\n  groups: ['300']\n"), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"qq", "qq.accounts", "qq.accounts.0", "qq.accounts.0.id", "qq.accounts.0.character", "qq.accounts.0.token", "qq.accounts.1.id", "qq.groups.0"} {
		if !c.Present[key] {
			t.Fatalf("missing explicit key %s", key)
		}
	}
	for _, key := range []string{"qq.accounts.0.url", "qq.accounts.1.token", "qq.accounts.2", "qq.groups.1", "qq.owner"} {
		if c.Present[key] {
			t.Fatalf("marked absent key %s", key)
		}
	}
}

func TestSettingRollbackPreservesExternalReplacement(t *testing.T) {
	for _, when := range []string{"before-readback", "during-rollback", "during-rollback-in-place"} {
		t.Run(when, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "sumika.yml")
			history := filepath.Join(dir, "history")
			raw := []byte("qq: {reply: false}\n")
			external := []byte("qq: {reply: false}\nexternal: keep\n")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			swap := func() error {
				name, err := configTemp(file, external)
				if err != nil {
					return err
				}
				defer os.Remove(name)
				return fileatomic.Replace(name, file, 2*time.Second)
			}
			reads, replacements := 0, 0
			io := configFileIO{
				read: func(path string) ([]byte, error) {
					reads++
					if when == "during-rollback" && reads == 4 {
						if err := swap(); err != nil {
							return nil, err
						}
					}
					if when == "during-rollback-in-place" && reads == 4 {
						if err := os.WriteFile(path, external, 0600); err != nil {
							return nil, err
						}
					}
					return os.ReadFile(path)
				},
				replace: func(src, dst string, timeout time.Duration) error {
					replacements++
					if err := fileatomic.Replace(src, dst, timeout); err != nil {
						return err
					}
					if replacements == 1 {
						if err := os.WriteFile(dst, []byte("damaged"), 0600); err != nil {
							return err
						}
						if when == "before-readback" {
							return swap()
						}
					}
					return nil
				},
			}
			_, err := saveConfigWithIO(context.Background(), file, history, ContentVersion(raw), time.Unix(1234, 0), func(b []byte) ([]byte, Config, error) {
				return PatchSetting(b, file, SettingChange{"qq.reply", true})
			}, io)
			if !errors.Is(err, ErrConfigVersion) {
				t.Fatalf("external replacement not reported: %v", err)
			}
			got, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(got, external) {
				t.Fatal("external replacement lost", err)
			}
			if replacements != 1 {
				t.Fatal("overwrote external replacement")
			}
			backups, err := os.ReadDir(history)
			if err != nil || len(backups) != 1 {
				t.Fatal("original backup missing", err)
			}
			backup, err := os.ReadFile(filepath.Join(history, backups[0].Name()))
			if err != nil || !bytes.Equal(backup, raw) {
				t.Fatal("original backup changed", err)
			}
		})
	}
}
