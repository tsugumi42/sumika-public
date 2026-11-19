package config

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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"sumika-native/internal/fileatomic"
)

var ErrConfigVersion = errors.New("配置文件在外面改过，已重新读取")
var configWriteMu sync.Mutex

// 所有设置页共用文件级协调和备份序号，不会互相覆盖旧版本。
var configHistoryName struct {
	prefix string
	next   int
}

func ContentVersion(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// SaveModel 只授权模型键；文件写入协调与其他本地设置页共用。
func SaveModel(ctx context.Context, file, history, base string, ch ModelChange, now time.Time) (Config, error) {
	return saveConfig(ctx, file, history, base, now, func(raw []byte) ([]byte, Config, error) { return PatchModel(raw, file, ch) })
}
func saveConfig(ctx context.Context, file, history, base string, now time.Time, patch func([]byte) ([]byte, Config, error)) (Config, error) {
	return saveConfigWithIO(ctx, file, history, base, now, patch, configFileIO{read: os.ReadFile, replace: fileatomic.Replace})
}

type configFileIO struct {
	read    func(string) ([]byte, error)
	replace func(string, string, time.Duration) error
}

func saveConfigWithIO(ctx context.Context, file, history, base string, now time.Time, patch func([]byte) ([]byte, Config, error), io configFileIO) (Config, error) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if file == "" {
		return Config{}, errors.New("还没有配置文件，请先复制 sumika.example.yml")
	}
	raw, err := io.read(file)
	if err != nil {
		return Config{}, err
	}
	if ContentVersion(raw) != base {
		return Config{}, ErrConfigVersion
	}
	out, c, err := patch(raw)
	if err != nil {
		return Config{}, err
	}
	if err = ctx.Err(); err != nil {
		return Config{}, err
	}
	if bytes.Equal(out, raw) {
		return c, nil
	}
	if err = os.MkdirAll(history, 0700); err != nil {
		return Config{}, err
	}
	prefix := filepath.Join(history, now.Format("20060102-150405"))
	if configHistoryName.prefix != prefix {
		configHistoryName.prefix, configHistoryName.next = prefix, 0
	}
	for n := configHistoryName.next; ; n++ {
		suffix := ""
		if n > 0 {
			suffix = fmt.Sprintf("-%d", n)
		}
		f, e := os.OpenFile(prefix+suffix+"-sumika.yml", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(e, os.ErrExist) {
			continue
		}
		if e != nil {
			return Config{}, e
		}
		configHistoryName.next = n + 1
		_, e = f.Write(raw)
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return Config{}, e
		}
		break
	}
	temp, err := configTemp(file, out)
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(temp)
	candidateInfo, err := os.Stat(temp)
	if err != nil {
		return Config{}, err
	}
	// Windows 的 Stat 延迟读取文件 ID；重命名前强制固定临时文件身份。
	if !os.SameFile(candidateInfo, candidateInfo) {
		return Config{}, errors.New("无法读取配置临时文件身份")
	}
	// 等待备份和 Sync 期间外部编辑也不能被覆盖。
	current, err := io.read(file)
	if err != nil {
		return Config{}, err
	}
	if !bytes.Equal(current, raw) {
		return Config{}, ErrConfigVersion
	}
	if err = ctx.Err(); err != nil {
		return Config{}, err
	}
	if err = io.replace(temp, file, 2*time.Second); err != nil {
		return Config{}, err
	}
	written, readErr := io.read(file)
	if readErr == nil && bytes.Equal(written, out) {
		if checked, parseErr := Parse(written, file); parseErr == nil {
			return checked, nil
		}
	}
	verifyErr := errors.New("配置落盘核对失败")
	if readErr != nil {
		verifyErr = fmt.Errorf("配置落盘读取失败: %w", readErr)
	}
	// 只能识别原子替换的外部文件；原地写入与磁盘损坏无法区分。
	// Stat/读取与替换之间仍有窗口，这不是跨进程原子 CAS。
	checkIdentity := func() error {
		info, e := os.Stat(file)
		if e != nil {
			return e
		}
		if !os.SameFile(candidateInfo, info) {
			return ErrConfigVersion
		}
		return nil
	}
	if e := checkIdentity(); e != nil {
		if errors.Is(e, ErrConfigVersion) {
			return Config{}, errors.Join(verifyErr, e)
		}
		return Config{}, errors.Join(verifyErr, fmt.Errorf("配置回滚失败: %w", e))
	}
	if readErr != nil {
		written, err = io.read(file)
		if err != nil {
			return Config{}, errors.Join(verifyErr, fmt.Errorf("配置回滚失败: %w", err))
		}
		if e := checkIdentity(); e != nil {
			if errors.Is(e, ErrConfigVersion) {
				return Config{}, errors.Join(verifyErr, e)
			}
			return Config{}, errors.Join(verifyErr, fmt.Errorf("配置回滚失败: %w", e))
		}
	}
	rollback, rollbackErr := configTemp(file, raw)
	if rollbackErr == nil {
		defer os.Remove(rollback)
		rollbackErr = checkIdentity()
	}
	if rollbackErr == nil {
		var current []byte
		current, rollbackErr = io.read(file)
		if rollbackErr == nil && !bytes.Equal(current, written) {
			rollbackErr = ErrConfigVersion
		}
	}
	if rollbackErr == nil {
		rollbackErr = checkIdentity()
	}
	if errors.Is(rollbackErr, ErrConfigVersion) {
		return Config{}, errors.Join(verifyErr, rollbackErr)
	}
	if rollbackErr == nil {
		rollbackErr = io.replace(rollback, file, 2*time.Second)
	}
	if rollbackErr == nil {
		restored, e := io.read(file)
		if e != nil {
			rollbackErr = e
		} else if !bytes.Equal(restored, raw) {
			rollbackErr = errors.New("回滚后内容不一致")
		}
	}
	if rollbackErr != nil {
		return Config{}, errors.Join(verifyErr, fmt.Errorf("配置回滚失败: %w", rollbackErr))
	}
	return Config{}, verifyErr
}

func configTemp(file string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(file), ".sumika-config-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// PatchModel 只补丁目标字节；语义自检连未知键也比较，不会放过配置结构体未声明的键。
func PatchModel(raw []byte, file string, ch ModelChange) ([]byte, Config, error) {
	path, value, model, err := ch.Target()
	if err != nil {
		return nil, Config{}, err
	}
	// 去掉覆盖：写 0 就是删掉这一项，不留一个 0 在文件里
	removeOverride := (ch.Field == ModelContextOverride || ch.Field == ModelOutputOverride) && ch.Int != nil && *ch.Int == 0
	return patchSettings(raw, file, []configEdit{{path: path, value: value, remove: model && value == "" || removeOverride}})
}

type configEdit struct {
	path   []string
	value  any
	remove bool
}

func patchSettings(raw []byte, file string, edits []configEdit) ([]byte, Config, error) {
	if _, err := Parse(raw, file); err != nil {
		return nil, Config{}, err
	}
	var expected map[string]any
	if err := yaml.Unmarshal(raw, &expected); err != nil {
		return nil, Config{}, err
	}
	out := raw
	for _, edit := range edits {
		// 仅定位副本转 LF，补丁仍作用于原字节。
		tree, err := parser.ParseBytes(bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n")), parser.ParseComments)
		if err != nil {
			return nil, Config{}, err
		}
		if len(tree.Docs) != 1 {
			return nil, Config{}, errors.New("设置只支持单文档 YAML")
		}
		literal, err := yaml.Marshal(edit.value)
		if err != nil {
			return nil, Config{}, err
		}
		switch v := edit.value.(type) {
		case []string, []any, map[string]any:
			literal, err = json.Marshal(edit.value)
			if err != nil {
				return nil, Config{}, err
			}
		case string:
			// 多行文字（模板）：YAML 的块标量要缩进对齐，嵌套键下容易写坏；JSON 字符串就是合法的双引号标量，换行写成转义
			if strings.ContainsAny(v, "\n\r") {
				literal, err = json.Marshal(v)
				if err != nil {
					return nil, Config{}, err
				}
			}
		}
		out, err = patchMapping(out, tree.Docs[0].Body, edit.path, strings.TrimSpace(string(literal)), edit.remove)
		if err != nil {
			return nil, Config{}, err
		}
		m := expected
		for _, key := range edit.path[:len(edit.path)-1] {
			nested, ok := m[key].(map[string]any)
			if !ok && edit.remove {
				// 要删的键连上层都没有：文件不会变，预期里也不能凭空多出一层空表（delete 对 nil 表什么都不做）
				m = nil
				break
			}
			if !ok {
				nested = map[string]any{}
				m[key] = nested
			}
			m = nested
		}
		key := edit.path[len(edit.path)-1]
		if edit.remove {
			delete(m, key)
		} else {
			m[key] = edit.value
		}
	}
	c, err := Parse(out, file)
	if err != nil {
		return nil, Config{}, err
	}
	var after, normalized map[string]any
	if err = yaml.Unmarshal(out, &after); err != nil {
		return nil, Config{}, err
	}
	encoded, err := yaml.Marshal(expected)
	if err != nil {
		return nil, Config{}, err
	}
	if err = yaml.Unmarshal(encoded, &normalized); err != nil {
		return nil, Config{}, err
	}
	if !reflect.DeepEqual(normalized, after) {
		return nil, Config{}, errors.New("配置自检失败：目标以外的键发生变化")
	}
	return out, c, nil
}

func byteAt(raw []byte, line, col int) int {
	at := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(raw[at:], '\n')
		if i < 0 {
			return len(raw)
		}
		at += i + 1
	}
	for n := 1; n < col && at < len(raw); n++ {
		_, size := utf8.DecodeRune(raw[at:])
		at += size
	}
	return at
}
func splice(raw []byte, from, to int, text string) []byte {
	out := make([]byte, 0, len(raw)+len(text)-(to-from))
	out = append(out, raw[:from]...)
	out = append(out, text...)
	return append(out, raw[to:]...)
}
func scalarEnd(raw []byte, at int, flow bool) (int, error) {
	if at >= len(raw) {
		return at, errors.New("缺少标量")
	}
	quote := raw[at]
	if quote == '\'' || quote == '"' {
		for i := at + 1; i < len(raw); i++ {
			if raw[i] == '\n' || raw[i] == '\r' {
				break
			}
			if quote == '"' && raw[i] == '\\' {
				i++
				continue
			}
			if raw[i] == quote {
				if quote == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
					i++
					continue
				}
				return i + 1, nil
			}
		}
		return 0, errors.New("不支持跨行标量")
	}
	end := at
	for end < len(raw) {
		b := raw[end]
		if b == '\n' || b == '\r' || (flow && (b == ',' || b == '}')) || (b == '#' && (end == at || raw[end-1] == ' ' || raw[end-1] == '\t')) {
			break
		}
		end++
	}
	for end > at && (raw[end-1] == ' ' || raw[end-1] == '\t') {
		end--
	}
	return end, nil
}
func patchMapping(raw []byte, node ast.Node, path []string, literal string, remove bool) ([]byte, error) {
	m, ok := node.(*ast.MappingNode)
	if !ok {
		return nil, modelError(path)
	}
	var entry *ast.MappingValueNode
	for _, e := range m.Values {
		if e.Key.IsMergeKey() {
			return nil, modelError(path)
		}
		if s, ok := e.Key.(*ast.StringNode); ok && s.Value == path[0] {
			entry = e
			break
		}
	}
	nl := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		nl = "\r\n"
	}
	if entry != nil {
		if len(path) > 1 {
			return patchMapping(raw, entry.Value, path[1:], literal, remove)
		}
		if _, ok := entry.Value.(*ast.SequenceNode); ok && !remove {
			return patchContainer(raw, entry.Value, literal, path)
		}
		if _, ok := entry.Value.(*ast.MappingNode); ok && !remove {
			return patchContainer(raw, entry.Value, literal, path)
		}
		switch entry.Value.Type() {
		case ast.StringType, ast.IntegerType, ast.FloatType, ast.BoolType, ast.NullType:
		default:
			return nil, modelError(path)
		}
		p := entry.Value.GetToken().Position
		at := byteAt(raw, p.Line, p.Column)
		end, err := scalarEnd(raw, at, m.IsFlowStyle)
		if err != nil {
			return nil, err
		}
		if !remove {
			return splice(raw, at, end, literal), nil
		}
		kp := entry.Key.GetToken().Position
		start := byteAt(raw, kp.Line, kp.Column)
		if m.IsFlowStyle {
			right := end
			for right < len(raw) && (raw[right] == ' ' || raw[right] == '\t') {
				right++
			}
			if right < len(raw) && raw[right] == ',' {
				right++
				return splice(raw, start, right, ""), nil
			}
			left := start
			for left > 0 && (raw[left-1] == ' ' || raw[left-1] == '\t') {
				left--
			}
			if left > 0 && raw[left-1] == ',' {
				left--
			}
			return splice(raw, left, end, ""), nil
		}
		valueEnd := end
		start = byteAt(raw, kp.Line, 1)
		tail := bytes.IndexByte(raw[end:], '\n')
		if tail < 0 {
			end = len(raw)
		} else {
			end += tail + 1
		}
		// 删除值的同时保留独立注释，避免丢掉用户说明。
		comment := ""
		lineEnd := end
		if lineEnd > 0 && raw[lineEnd-1] == '\n' {
			lineEnd--
		}
		if i := bytes.IndexByte(raw[valueEnd:lineEnd], '#'); i >= 0 {
			comment = strings.Repeat(" ", kp.Column-1) + string(raw[valueEnd+i:lineEnd])
			comment = strings.TrimSuffix(comment, "\r") + nl
		}
		if len(m.Values) == 1 {
			return splice(raw, start, end, strings.Repeat(" ", kp.Column-1)+"{}"+nl+comment), nil
		}
		return splice(raw, start, end, comment), nil
	}
	if remove {
		return append([]byte(nil), raw...), nil
	}
	keyLiteral := func(s string) string { return strconv.Quote(s) }
	// 缺少的层只加在当前映射末尾；空 flow 改成块写法，其余 flow 在花括号内插入。
	if m.IsFlowStyle && len(m.Values) > 0 {
		nested := literal
		for i := len(path) - 1; i >= 1; i-- {
			nested = "{" + keyLiteral(path[i]) + ": " + nested + "}"
		}
		p := m.End.Position
		at := byteAt(raw, p.Line, p.Column)
		return splice(raw, at, at, ", "+keyLiteral(path[0])+": "+nested), nil
	}
	indent := 0
	at := len(raw)
	if len(m.Values) > 0 {
		first := m.Values[0].Key.GetToken().Position
		indent = first.Column - 1
		last := m.Values[len(m.Values)-1]
		// AST 中最后一个后代的 token 给出末行；不把后面同级项或注释搬走。
		endLine := last.Key.GetToken().Position.Line
		var walk func(ast.Node)
		walk = func(n ast.Node) {
			if p := n.GetToken().Position; p.Line > endLine {
				endLine = p.Line
			}
			if mm, ok := n.(*ast.MappingNode); ok {
				for _, v := range mm.Values {
					walk(v.Key)
					walk(v.Value)
				}
			}
			if ss, ok := n.(*ast.SequenceNode); ok {
				for _, v := range ss.Values {
					walk(v)
				}
			}
		}
		walk(last.Value)
		at = byteAt(raw, endLine, 1)
		if i := bytes.IndexByte(raw[at:], '\n'); i >= 0 {
			at += i + 1
		} else {
			at = len(raw)
		}
	} else if m.IsFlowStyle {
		p := m.Start.Position
		at = byteAt(raw, p.Line, p.Column)
		// 只有 \r 换行的文件里 goccy 的行号和 byteAt 的 \n 定位对不上，End 会落到文件末尾之后；
		// 夹到末尾，按空映射替换，别让 patchMapping panic（fuzz 输入 "{\r}"）。
		end := min(byteAt(raw, m.End.Position.Line, m.End.Position.Column)+1, len(raw))
		// 冒号后的空映射不保留花括号，行尾注释留在父键上。
		lineStart := byteAt(raw, p.Line, 1)
		indent = 0
		for lineStart+indent < len(raw) && raw[lineStart+indent] == ' ' {
			indent++
		}
		indent += 2
		tail := end
		for tail < len(raw) && raw[tail] != '\n' {
			tail++
		}
		suffix := string(raw[end:tail])
		suffix = strings.TrimSuffix(suffix, "\r")
		prefix := at
		for prefix > lineStart && raw[prefix-1] == ' ' {
			prefix--
		}
		text := suffix + nl
		for i, k := range path {
			text += strings.Repeat(" ", indent+2*i) + keyLiteral(k) + ":"
			if i == len(path)-1 {
				text += " " + literal
			}
			text += nl
		}
		if tail < len(raw) {
			tail++
		}
		if prefix < 0 || prefix > tail || tail > len(raw) {
			return nil, modelError(path)
		}
		return splice(raw, prefix, tail, text), nil
	}
	text := ""
	if at > 0 && raw[at-1] != '\n' {
		text = nl
	}
	for i, k := range path {
		text += strings.Repeat(" ", indent+2*i) + keyLiteral(k) + ":"
		if i == len(path)-1 {
			text += " " + literal
		}
		text += nl
	}
	return splice(raw, at, at, text), nil
}
