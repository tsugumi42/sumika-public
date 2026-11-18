// Package backup 是 Sumika 的备份（规格-数据目录与备份 第十节）：每天自动备份、手动备份、校验、轮换。
//
// 叶子包：只依赖 paths、sqlitesnap 和标准库，不 import store、memory、app。持有库连接的一方通过 Service.Register 登记自己的快照函数，
// 路径只在 paths 和 layout.go 里算一次。
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Program 是本程序写的清单的标记。备份目录里别的东西（Bun 时代的 每天-…/manifest.json、记忆开写前的 memory-*.db）没有它，一律不认
const Program = "sumika-native-backup"

// FormatVersion 是清单格式。不认识的版本（更新的、没有的）当作不是自己的，不碰
const FormatVersion = 1

// ManifestFile 是每份备份里清单的文件名
const ManifestFile = "manifest.json"

// maxManifestBytes：清单读多大就不认了（带全部媒体的清单也到不了这个数；防备份目录里被塞进一个巨大的假文件）
const maxManifestBytes = 512 << 20

// syncFile 把写好的文件刷到盘上再关（备份副本、清单写完都要落盘，断电后不能留下写了一半的“好备份”）。放成变量只为测试：
// 测试库都在临时目录，不测断电，换成不刷（backup/fastsync_test.go），机器忙时每个文件几十毫秒的等待就没有了
var syncFile = func(f *os.File) error { return f.Sync() }

// Reason 是为什么做这份备份
type Reason string

const (
	ReasonDaily     Reason = "daily"      // 每天自动
	ReasonManual    Reason = "manual"     // 主人点的、sumika-ctl backup now
	ReasonPreChange Reason = "pre-change" // 有风险的改动之前
)

// Status 是一份备份现在的状态
type Status string

const (
	StatusPartial Status = "partial" // 还在做，或者做到一半崩了
	StatusOK      Status = "ok"      // 做完了，校验全过
	StatusBad     Status = "bad"     // 做完了，有一项没过；这份不算数
)

// Class 是“丢了能不能找回来”的四类里要备份的三类（第二节）
type Class string

const (
	ClassSelf     Class = "self"     // 她本人
	ClassMaterial Class = "material" // 素材
	ClassMedia    Class = "media"    // 媒体
)

// 媒体档（backup.dailyMedia）
const (
	MediaNone     = "none"
	MediaStickers = "stickers"
	MediaAll      = "all"
)

// ValidMedia 是不是认识的媒体档
func ValidMedia(m string) bool { return m == MediaNone || m == MediaStickers || m == MediaAll }

// FileEntry 是清单里一个文件
type FileEntry struct {
	Path   string `json:"path"` // 相对数据目录，斜杠
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Class  Class  `json:"class"`
	Linked bool   `json:"linked,omitempty"` // 和上一份备份共用同一个文件（硬链接）
}

// DBEntry 是清单里一个库
type DBEntry struct {
	Path      string           `json:"path"`
	Method    string           `json:"method"`    // holder：持有方登记的快照；fallback：备份包自己用只读连接取的
	Integrity string           `json:"integrity"` // integrity_check 的结果，"ok" 才算过
	Tables    map[string]int64 `json:"tables"`    // 每个普通表的行数
}

const (
	MethodHolder   = "holder"
	MethodFallback = "fallback"
)

// Manifest 是一份备份的清单（manifest.json）
type Manifest struct {
	Program  string    `json:"program"`
	Format   int       `json:"format"`
	Version  string    `json:"version"` // 程序版本（提交号）
	Name     string    `json:"name"`
	Time     time.Time `json:"time"`
	Reason   Reason    `json:"reason"`
	Label    string    `json:"label,omitempty"`
	Source   string    `json:"source,omitempty"` // 这份备份属于哪个数据目录（真实路径）：别的数据目录的备份不参与这里的轮换、“最近一份”和硬链接
	Status   Status    `json:"status"`
	Problems []string  `json:"problems,omitempty"` // 没过的原因：有一条这份就是 bad
	Notes    []string  `json:"notes,omitempty"`    // 备份时遇到的小事（跳过的链接、没登记的库），不影响好坏
	Classes  []Class   `json:"classes"`
	Media    string    `json:"media"`
	Bytes    int64     `json:"bytes"`  // 文件大小合计（含共用的）
	Linked   int       `json:"linked"` // 其中和上一份共用的个数
	// SharedBytes 是其中共用的那些的大小合计：Bytes - SharedBytes 是这份自己占的（估算总占用时用，不用逐个文件取身份）
	SharedBytes int64 `json:"sharedBytes,omitempty"`
	// SkippedMedia 是走数据目录时没法带的媒体（链接、读不了）的个数；属于她本人、素材、库的没法带是问题（这份记 bad），不在这里
	SkippedMedia int       `json:"skippedMedia,omitempty"`
	DBs          []DBEntry `json:"dbs"`
	// WriteWait 是备份跑着时各聊天库的写锁等待采样（规格 10.10.6）；没有探针的备份缺这一项
	WriteWait []WriteWaitEntry `json:"writeWait,omitempty"`
	Files     []FileEntry      `json:"files"`
}

// header 是清单去掉文件列表的部分：调度、轮换、列表只要它，不用把带全部媒体的几万行也解出来
type header struct {
	Program      string    `json:"program"`
	Format       int       `json:"format"`
	Version      string    `json:"version"`
	Name         string    `json:"name"`
	Time         time.Time `json:"time"`
	Reason       Reason    `json:"reason"`
	Label        string    `json:"label"`
	Source       string    `json:"source"`
	Status       Status    `json:"status"`
	Problems     []string  `json:"problems"`
	Notes        []string  `json:"notes"`
	Classes      []Class   `json:"classes"`
	Media        string    `json:"media"`
	Bytes        int64     `json:"bytes"`
	Linked       int       `json:"linked"`
	SharedBytes  int64     `json:"sharedBytes"`
	SkippedMedia int       `json:"skippedMedia"`
	DBs          []DBEntry `json:"dbs"`
	// WriteWait：同 Manifest
	WriteWait []WriteWaitEntry `json:"writeWait"`
}

func (h header) manifest() Manifest {
	return Manifest{Program: h.Program, Format: h.Format, Version: h.Version, Name: h.Name, Time: h.Time, Reason: h.Reason, Label: h.Label, Source: h.Source,
		Status: h.Status, Problems: h.Problems, Notes: h.Notes, Classes: h.Classes, Media: h.Media, Bytes: h.Bytes, Linked: h.Linked, SharedBytes: h.SharedBytes, SkippedMedia: h.SkippedMedia, DBs: h.DBs, WriteWait: h.WriteWait}
}

// Entry 是备份目录里一份认得出来的备份
type Entry struct {
	Name     string   // 文件夹名（不含 .partial）
	Folder   string   // 文件夹的完整路径
	Partial  bool     // 文件夹名带 .partial
	Manifest Manifest // 没有 Files（要全部文件列表用 ReadManifest）
}

// ErrNotOurs：这个文件夹不是本程序建的备份（没有清单、清单空或坏、不是本程序的标记、格式不认识、名字对不上）
var ErrNotOurs = errors.New("不是本程序建的备份")

func isLinkEntry(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return true
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	return linkOnDisk(path)
}

// readHeader 读 folder 里的清单头；folderName 是文件夹名（去掉 .partial 的），清单里的名字必须和它一致
func readHeader(folder, folderName string) (Manifest, error) {
	f, err := os.Open(filepath.Join(folder, ManifestFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w：读不到清单", ErrNotOurs)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxManifestBytes {
		return Manifest{}, fmt.Errorf("%w：清单是空的、太大或不是普通文件", ErrNotOurs)
	}
	h, err := decodeHeader(io.LimitReader(f, maxManifestBytes))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w：清单读不懂", ErrNotOurs)
	}
	if h.Program != Program || h.Format != FormatVersion {
		return Manifest{}, fmt.Errorf("%w：标记或格式不认识", ErrNotOurs)
	}
	if h.Name == "" || h.Name != folderName {
		return Manifest{}, fmt.Errorf("%w：清单里的名字和文件夹名对不上", ErrNotOurs)
	}
	switch h.Status {
	case StatusPartial, StatusOK, StatusBad:
	default:
		return Manifest{}, fmt.Errorf("%w：状态不认识", ErrNotOurs)
	}
	switch h.Reason {
	case ReasonDaily, ReasonManual, ReasonPreChange:
	default:
		return Manifest{}, fmt.Errorf("%w：原因不认识", ErrNotOurs)
	}
	if h.Time.IsZero() {
		return Manifest{}, fmt.Errorf("%w：没有时间", ErrNotOurs)
	}
	return h.manifest(), nil
}

// decodeHeader 只读清单的头：文件列表（files）总是写在最后，头的必填项齐了、读到 files 就停，后面几万行不再解。
// 带全部媒体的清单有几十 MB，backup list、调度、轮换每次都要读每一份的头，不能把文件列表也解一遍
func decodeHeader(r io.Reader) (header, error) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return header{}, errors.New("不是 json 对象")
	}
	obj := map[string]json.RawMessage{}
	have := func() bool {
		for _, k := range []string{"program", "format", "name", "time", "reason", "status"} {
			if _, ok := obj[k]; !ok {
				return false
			}
		}
		return true
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return header{}, err
		}
		key, ok := kt.(string)
		if !ok {
			return header{}, errors.New("键不是字符串")
		}
		if key == "files" && have() {
			break
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return header{}, err
		}
		obj[key] = raw
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return header{}, err
	}
	var h header
	if err := json.Unmarshal(b, &h); err != nil {
		return header{}, err
	}
	return h, nil
}

// ReadManifest 读一份备份的完整清单（含文件列表）。同样只认本程序的
func ReadManifest(folder string) (Manifest, error) {
	name := strings.TrimSuffix(filepath.Base(folder), ".partial")
	if _, err := readHeader(folder, name); err != nil {
		return Manifest{}, err
	}
	b, err := os.ReadFile(filepath.Join(folder, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w：清单读不懂", ErrNotOurs)
	}
	return m, nil
}

// writeManifest 把清单写进 folder：先写临时文件、落盘、再改名，写坏一半不会留下一个读不懂的 manifest.json
func writeManifest(folder string, m Manifest) error {
	m.Program, m.Format = Program, FormatVersion
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(folder, ManifestFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	serr := syncFile(f)
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(folder, ManifestFile))
}

// Scan 列出备份目录里所有认得出来的备份（含 .partial），新的在前。不是本程序建的、是联接或符号链接的一概不列。
// 目录不存在返回空，不报错。
func Scan(dir string) ([]Entry, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range ents {
		if !e.IsDir() { // 联接在不同系统里 IsDir 不一定为真；下面 isLinkEntry 再查一道
			continue
		}
		folder := filepath.Join(dir, e.Name())
		if isLinkEntry(folder) {
			continue
		}
		name := e.Name()
		partial := strings.HasSuffix(name, ".partial")
		name = strings.TrimSuffix(name, ".partial")
		m, err := readHeader(folder, name)
		if err != nil {
			continue
		}
		// 带 .partial 的文件夹清单必须还在写（partial）或已判 bad；清单说 ok 的不该叫 .partial（改名前崩了的残留，也不当好的）
		if partial && m.Status == StatusOK {
			m.Status = StatusPartial
		}
		if !partial && m.Status == StatusPartial {
			continue // 正式名字却是 partial：改名到一半的怪状态，不认
		}
		out = append(out, Entry{Name: name, Folder: folder, Partial: partial, Manifest: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Manifest.Time.Equal(out[j].Manifest.Time) {
			return out[i].Manifest.Time.After(out[j].Manifest.Time)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}
