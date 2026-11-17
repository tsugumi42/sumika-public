package dbguard

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

const (
	markerCompatible = "-- sumika: compatible"
	markerBreaking   = "-- sumika: breaking"
)

// Track 是一条迁移轨：一组按号排的 goose 迁移文件、它的版本表，以及“护栏起点号”。
//
// FS 必须已经 Sub 到迁移目录（里面直接是 0017_xxx.sql 这类文件）。起点号及以前的文件是存量迁移，
// 不要求标记、一律算 breaking；之后的文件第一行必须是 markerCompatible 或 markerBreaking 之一。
//
// FS 为 nil 时这条轨没有迁移文件，只有一个版本号（记在 Version）：旁路索引 memory-v2.db 的
// memory-index 轨就是这种，版本现在是 1，没有 goose 表，也没有东西可跑。
type Track struct {
	Name  string
	FS    fs.FS
	Table string // goose 版本表名；空 = goose_db_version
	Floor int64  // 护栏起点号：这个号及以前没有标记的老迁移都算 breaking
	// Version 只在 FS 为 nil 时有意义：没有迁移文件的轨用这个号当 latest / min_reader。
	Version int64
	// AcceptNoGoose 表示这个库允许一张 goose 表都没有（Bun 建的老记忆库）：有用户表、却没有
	// goose 表时按 0 版迁移，而不是判成 unknown_schema。没有迁移文件的轨用不到这个开关。
	AcceptNoGoose bool
}

func (t Track) table() string {
	if t.Table == "" {
		return DefaultTable
	}
	return t.Table
}

type migrationFile struct {
	Version int64
	Name    string
}

// migrations 列出这条轨的迁移文件，按号排好。文件名的数字前缀就是版本号；.sql 文件解析不了就报错，
// 不把它悄悄跳过（否则漏掉的迁移看起来像不存在）。
func (t Track) migrations() ([]migrationFile, error) {
	if t.FS == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(t.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("读迁移轨 %s：%w", t.Name, err)
	}
	var out []migrationFile
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		head, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("迁移轨 %s 的文件 %s 没有数字前缀", t.Name, e.Name())
		}
		n, err := strconv.ParseInt(head, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("迁移轨 %s 的文件 %s 版本号不是正整数", t.Name, e.Name())
		}
		if prev, dup := seen[n]; dup {
			return nil, fmt.Errorf("迁移轨 %s 的 %d 号有两份文件：%s、%s", t.Name, n, prev, e.Name())
		}
		seen[n] = e.Name()
		out = append(out, migrationFile{Version: n, Name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Latest 是这条轨程序认识的最大号。没有迁移文件的轨返回 Version。
func (t Track) Latest() (int64, error) {
	if t.FS == nil {
		if t.Version <= 0 {
			return 0, fmt.Errorf("迁移轨 %s 没有迁移文件，版本号也没给", t.Name)
		}
		return t.Version, nil
	}
	files, err := t.migrations()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("迁移轨 %s 没有迁移文件", t.Name)
	}
	return files[len(files)-1].Version, nil
}

// MinReader 是能读这条轨的最低程序版本：最大的 breaking 号；一个 breaking 都没有时是这条轨的第一个号。
// 起点号及以前的文件一律算 breaking；之后的文件按第一行标记判断。
func (t Track) MinReader() (int64, error) {
	if t.FS == nil {
		if t.Version <= 0 {
			return 0, fmt.Errorf("迁移轨 %s 没有迁移文件，版本号也没给", t.Name)
		}
		return t.Version, nil
	}
	files, err := t.migrations()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("迁移轨 %s 没有迁移文件", t.Name)
	}
	var min int64
	for _, f := range files {
		breaking := f.Version <= t.Floor
		if !breaking {
			line, err := firstLine(t.FS, f.Name)
			if err != nil {
				return 0, err
			}
			switch strings.TrimSpace(line) {
			case markerCompatible:
				breaking = false
			case markerBreaking:
				breaking = true
			default:
				return 0, fmt.Errorf("迁移轨 %s 的 %s 第一行不是 %q 或 %q（是 %q）", t.Name, f.Name, markerCompatible, markerBreaking, strings.TrimSpace(line))
			}
		}
		if breaking && f.Version > min {
			min = f.Version
		}
	}
	if min == 0 {
		min = files[0].Version
	}
	return min, nil
}

func firstLine(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", fmt.Errorf("读 %s：%w", name, err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSuffix(line, "\r"), nil
}

// 违规种类（检查迁移文件的测试用）。
const (
	ProblemMissingMarker = "missing_marker" // 起点号之后的文件第一行没有标记
	ProblemBadMarker     = "bad_marker"     // 第一行有东西，但不是两种标记之一
	ProblemDown          = "down"           // 带 -- +goose Down 且下面有语句
)

// Violation 是一个迁移文件的问题。File 是文件名，测试和报错都靠它定位。
type Violation struct {
	Track   string
	File    string
	Problem string
}

func (v Violation) String() string {
	switch v.Problem {
	case ProblemMissingMarker:
		return fmt.Sprintf("迁移轨 %s 的 %s 缺第一行标记（%s 或 %s）", v.Track, v.File, markerCompatible, markerBreaking)
	case ProblemBadMarker:
		return fmt.Sprintf("迁移轨 %s 的 %s 第一行不是合法标记（%s 或 %s）", v.Track, v.File, markerCompatible, markerBreaking)
	case ProblemDown:
		return fmt.Sprintf("迁移轨 %s 的 %s 带 -- +goose Down 和语句；回退靠快照，新迁移不写 Down", v.Track, v.File)
	default:
		return fmt.Sprintf("迁移轨 %s 的 %s 有问题：%s", v.Track, v.File, v.Problem)
	}
}

// Check 检查一组迁移轨：起点号之后的文件要有合法标记，且不能带非空的 Down 段。
// 返回所有违规（不是遇到第一个就停）；读文件系统本身出错才返回 error。
func Check(tracks []Track) ([]Violation, error) {
	var out []Violation
	for _, t := range tracks {
		if t.FS == nil {
			continue
		}
		files, err := t.migrations()
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Version <= t.Floor {
				continue
			}
			b, err := fs.ReadFile(t.FS, f.Name)
			if err != nil {
				return nil, fmt.Errorf("读 %s：%w", f.Name, err)
			}
			line, rest, _ := strings.Cut(string(b), "\n")
			line = strings.TrimSuffix(line, "\r")
			switch trimmed := strings.TrimSpace(line); trimmed {
			case markerCompatible, markerBreaking:
			default:
				// 第一行是 SQL、空行或别的注释 = 没写标记；像是标记但拼错 = 标记写错。
				problem := ProblemMissingMarker
				if strings.HasPrefix(trimmed, "-- sumika:") {
					problem = ProblemBadMarker
				}
				out = append(out, Violation{Track: t.Name, File: f.Name, Problem: problem})
			}
			if hasDownStatements(rest) {
				out = append(out, Violation{Track: t.Name, File: f.Name, Problem: ProblemDown})
			}
		}
	}
	return out, nil
}

// hasDownStatements 看 -- +goose Down 之后有没有语句。空行和纯注释不算语句。
// 注解的认法照 goose v3.29.0 的 sqlparser：行首（去空白后）以 -- 开头且含 +goose，
// 去掉所有 --、去掉第一个 +goose，再 TrimSpace、EqualFold 比 Down。`--+goose down`、
// `--   +goose   Down`、`-- +goose DOWN` 都算，`-- +goose Up` 不算。
func hasDownStatements(src string) bool {
	inDown := false
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !inDown {
			if isGooseAnnotation(line, "Down") {
				inDown = true
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return true
	}
	return false
}

// isGooseAnnotation 按 goose 的规则判断这一行是不是某个注解（Up、Down 等）。
func isGooseAnnotation(line, name string) bool {
	if !strings.HasPrefix(line, "--") || !strings.Contains(line, "+goose") {
		return false
	}
	cmd := strings.ReplaceAll(line, "--", "")
	cmd = strings.Replace(cmd, "+goose", "", 1)
	if strings.Contains(cmd, "+goose") { // goose 认多个 +goose 是非法注解，不当成 Down
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cmd), name)
}
