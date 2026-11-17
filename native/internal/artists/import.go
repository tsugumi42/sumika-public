package artists

// 导入用户在确认页勾完导出的 artists-confirmed.json（规格-画师原图 三点二）。幂等：没有变化不生成批次；
// 有变化的行带批次号，--undo 只动还带着这个批次号的行。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxImportBytes 是导入文件的大小上限
const MaxImportBytes = 8 << 20

// ImportReport 是一次导入的结果。Batch 为空表示没有变化（没生成批次）。
type ImportReport struct {
	Batch string
	// Added 新增为生效；Upgraded 原来是待确认或“不是（判断）”、改成生效；Already 本来就生效；
	// Skipped 因为用户手动做过“不是”的决定、或表满了而跳过；Malformed 是 ID 格式不对的行。
	// Declined 是 rejected 里新标成“不是”的（没有行就新增，待确认、生效的改过来并记 Prev）；AlreadyDeclined 本来就是“不是”
	Added, Upgraded, Already, Skipped, Malformed int
	Declined, AlreadyDeclined                    int
}

// UndoReport 是一次撤回的结果
type UndoReport struct{ Restored, Deleted int }

// importAccount 是导出文件里的一个账号
type importAccount struct {
	ID         string `json:"id"`
	ScreenName string `json:"screen_name"`
	Name       string `json:"name"`
}

// importFile 是确认页导出的文件：confirmedArtists 是用户标“是”的，rejected 是标“不是”的（第三版确认页起才有，可以没有）；别的字段不管。
type importFile struct {
	ConfirmedArtists []importAccount `json:"confirmedArtists"`
	Rejected         []importAccount `json:"rejected"`
}

// ReadImportFile 读导入文件：必须是绝对路径、规范化后是普通文件（不是目录、不是指到别处的符号链接）、不超过 MaxImportBytes。
func ReadImportFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("要写绝对路径")
	}
	path = filepath.Clean(path)
	st, err := os.Lstat(path) // Lstat：最后一级是符号链接就不是普通文件，拒绝
	if err != nil {
		return nil, errors.New("找不到这个文件")
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("这不是一个普通文件（目录、符号链接都不行）")
	}
	if st.Size() > MaxImportBytes {
		return nil, fmt.Errorf("文件太大（上限 %d MB）", MaxImportBytes>>20)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("文件打不开")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxImportBytes+1))
	if err != nil {
		return nil, errors.New("文件读不完")
	}
	if len(b) > MaxImportBytes {
		return nil, fmt.Errorf("文件太大（上限 %d MB）", MaxImportBytes>>20)
	}
	return b, nil
}

// Import 把确认结果并进表。confirmedArtists 里每个合格的 ID：没有行就新增生效（来源 import）；待确认、判断出的“不是”改成生效并记 Prev；
// 已经生效的不动；用户手动做过“不是”的（来源 manual）跳过。rejected 里的：没有行就新增“不是”（来源 import），
// 待确认、生效的改成“不是”并记 Prev，本来就是“不是”的不动（用户 2026-11-13：确认页标“不是”的要记下，按需判断别再提）。
// 有变化才生成批次号 imp-<UTC 时间>。
func (s *Store) Import(data []byte) (ImportReport, error) {
	var f importFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&f); err != nil {
		return ImportReport{}, errors.New("文件不是确认页导出的 JSON")
	}
	if f.ConfirmedArtists == nil {
		return ImportReport{}, errors.New("文件里没有 confirmedArtists（不是确认页导出的结果）")
	}
	if len(f.ConfirmedArtists)+len(f.Rejected) > MaxEntries {
		return ImportReport{}, fmt.Errorf("文件里的账号太多（上限 %d）", MaxEntries)
	}
	var rep ImportReport
	err := s.mutate(func(m map[string]Entry) error {
		rep = ImportReport{}
		now := s.opt.Now().UTC()
		batch := newBatch(m, now)
		for _, a := range f.ConfirmedArtists {
			if !ValidID(s.platform, a.ID) {
				rep.Malformed++
				continue
			}
			e, ok := m[a.ID]
			switch {
			case ok && e.Status == StatusConfirmed:
				rep.Already++
				continue
			case ok && e.Status == StatusDeclined && e.Source == SourceManual:
				rep.Skipped++
				continue
			case !ok && len(m) >= MaxEntries:
				rep.Skipped++
				continue
			}
			ne := Entry{Status: StatusConfirmed, Source: SourceImport, Handle: cleanText(a.ScreenName, textMax), Name: cleanText(a.Name, textMax), At: now, Batch: batch}
			if ok {
				p := Prev(e) // 整行存下来：撤回时原样放回去
				p.Batch, p.Prev = "", nil
				ne.Prev = &p
				if ne.Handle == "" {
					ne.Handle = e.Handle
				}
				if ne.Name == "" {
					ne.Name = e.Name
				}
				rep.Upgraded++
			} else {
				rep.Added++
			}
			m[a.ID] = ne
		}
		// rejected：用户这次明确说“不是”。同一个号两边都有时以“是”为准；改动都进同一个批次，撤回时一起退回去
		yes := make(map[string]bool, len(f.ConfirmedArtists))
		for _, a := range f.ConfirmedArtists {
			yes[a.ID] = true
		}
		for _, a := range f.Rejected {
			if !ValidID(s.platform, a.ID) {
				rep.Malformed++
				continue
			}
			if yes[a.ID] {
				continue
			}
			e, ok := m[a.ID]
			switch {
			case ok && e.Status == StatusDeclined:
				rep.AlreadyDeclined++
				continue
			case !ok && len(m) >= MaxEntries:
				rep.Skipped++
				continue
			}
			ne := Entry{Status: StatusDeclined, Source: SourceImport, Handle: cleanText(a.ScreenName, textMax), Name: cleanText(a.Name, textMax), At: now, Batch: batch}
			if ok {
				p := Prev(e)
				p.Batch, p.Prev = "", nil
				ne.Prev = &p
				if ne.Handle == "" {
					ne.Handle = e.Handle
				}
				if ne.Name == "" {
					ne.Name = e.Name
				}
			}
			m[a.ID] = ne
			rep.Declined++
		}
		if rep.Added+rep.Upgraded+rep.Declined == 0 {
			return errNoChange
		}
		rep.Batch = batch
		return nil
	})
	if err != nil {
		return ImportReport{}, err
	}
	return rep, nil
}

// newBatch 取批次号；同一秒里已经有了就加后缀
func newBatch(m map[string]Entry, now time.Time) string {
	base := "imp-" + now.Format("20060102150405")
	used := map[string]bool{}
	for _, e := range m {
		if e.Batch != "" {
			used[e.Batch] = true
		}
	}
	batch := base
	for i := 2; used[batch]; i++ {
		batch = fmt.Sprintf("%s-%d", base, i)
	}
	return batch
}

// Undo 撤回一个导入批次：只动还带着这个批次号的行（有 Prev 就恢复 Prev，没有就删行）。
// 之后被 confirm、remove 动过的行批次号已经清空，不受影响。找不到带这个批次号的行报错。
func (s *Store) Undo(batch string) (UndoReport, error) {
	if !validBatch(batch) {
		return UndoReport{}, errors.New("批次号的格式不对（应该是 imp- 开头）")
	}
	var rep UndoReport
	err := s.mutate(func(m map[string]Entry) error {
		rep = UndoReport{}
		for id, e := range m {
			if e.Batch != batch {
				continue
			}
			if e.Prev == nil {
				delete(m, id)
				rep.Deleted++
				continue
			}
			m[id] = Entry(*e.Prev)
			rep.Restored++
		}
		if rep.Restored+rep.Deleted == 0 {
			return fmt.Errorf("没有带这个批次号的行（批次不存在，或它的行之后被改动过）")
		}
		return nil
	})
	return rep, err
}

func validBatch(b string) bool {
	if !strings.HasPrefix(b, "imp-") || len(b) > 40 {
		return false
	}
	for _, r := range b[4:] {
		if !(r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return len(b) > 4
}
