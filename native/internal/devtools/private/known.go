package private

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
	_ "modernc.org/sqlite"
)

// Sources 是名单的来源文件；PrivateJSON 为空表示不读。
type Sources struct{ QQDB, MaibotDB, GroupsJSON, ConfigYML, PrivateJSON string }

// SourceError 是名单读不了：只报是哪个来源、什么类，不带出文件内容（JSON 报错会把名单片段打到终端）。
type SourceError struct{ Msg string }

func (e *SourceError) Error() string { return e.Msg }

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func guard(what string, f func() error) error {
	if err := f(); err != nil {
		if se, ok := err.(*SourceError); ok {
			return se
		}
		return &SourceError{what + "读不了（Error）"}
	}
	return nil
}

type loader struct {
	numbers, names, secrets *oset
}

func (l *loader) addNums(v string) {
	for _, r := range digitRuns(v) {
		if n := r.end - r.start; n >= 5 && n <= 12 {
			l.numbers.add(v[r.start:r.end])
		}
	}
}

func (l *loader) addName(v string) {
	s := jsTrim(v)
	if n := jsLen(s); n >= 3 && n <= 20 && !reAllDigits.MatchString(s) {
		l.names.add(s)
	}
}

// valStr 是 String(v ?? 空串)：库里取出来的值按 JS 的写法转成字符串。
func valStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return jsNum(x)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

func openRO(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
}

func queryRows(db *sql.DB, q string) ([][]any, []string, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		out = append(out, vals)
	}
	return out, cols, rows.Err()
}

func tableSet(db *sql.DB) (map[string]bool, error) {
	rows, _, err := queryRows(db, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	t := map[string]bool{}
	for _, r := range rows {
		t[valStr(r[0])] = true
	}
	return t, nil
}

func colSet(db *sql.DB, table string) (map[string]bool, error) {
	rows, cols, err := queryRows(db, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, c := range cols {
		if c == "name" {
			idx = i
		}
	}
	c := map[string]bool{}
	for _, r := range rows {
		if idx >= 0 {
			c[valStr(r[idx])] = true
		}
	}
	return c, nil
}

// jsonNode 是保持键顺序的 JSON：JS 的 Object.keys 把“整数样子的键”按数值升序排在最前，其余按出现顺序。
type jsonNode struct {
	kind string // str num arr obj other
	s    string
	keys []string
	vals []*jsonNode
}

var reIndexKey = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func decodeJSON(b []byte) (*jsonNode, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	n, err := parseValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("多余内容")
	}
	return n, nil
}

func parseValue(d *json.Decoder) (*jsonNode, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch v := t.(type) {
	case string:
		return &jsonNode{kind: "str", s: v}, nil
	case json.Number:
		f, _ := strconv.ParseFloat(string(v), 64)
		return &jsonNode{kind: "num", s: jsNum(f)}, nil
	case json.Delim:
		n := &jsonNode{kind: "arr"}
		if v == '{' {
			n.kind = "obj"
		}
		for d.More() {
			if v == '{' {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, kt.(string))
			}
			c, err := parseValue(d)
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, c)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		if v == '[' {
			for i := range n.vals {
				n.keys = append(n.keys, strconv.Itoa(i))
			}
		} else {
			n.reorder()
		}
		return n, nil
	}
	return &jsonNode{kind: "other"}, nil
}

// reorder：整数样子的键数值升序在前，其余保持原顺序（JS 对象的属性顺序）；重复键后者覆盖前者但位置留在第一次出现处。
func (n *jsonNode) reorder() {
	type kv struct {
		k string
		v *jsonNode
	}
	var list []kv
	pos := map[string]int{}
	for i, k := range n.keys {
		if p, ok := pos[k]; ok {
			list[p].v = n.vals[i]
			continue
		}
		pos[k] = len(list)
		list = append(list, kv{k, n.vals[i]})
	}
	var ints, rest []kv
	for _, e := range list {
		if reIndexKey.MatchString(e.k) && len(e.k) < 11 {
			ints = append(ints, e)
		} else {
			rest = append(rest, e)
		}
	}
	sort.SliceStable(ints, func(a, b int) bool {
		x, _ := strconv.ParseInt(ints[a].k, 10, 64)
		y, _ := strconv.ParseInt(ints[b].k, 10, 64)
		return x < y
	})
	n.keys, n.vals = nil, nil
	for _, e := range append(ints, rest...) {
		n.keys = append(n.keys, e.k)
		n.vals = append(n.vals, e.v)
	}
}

// LoadKnown 读本机名单（只读）。第二个返回值是缺了哪些来源的说明。
func LoadKnown(src Sources) (*Known, []string, error) {
	l := &loader{newSet(), newSet(), newSet()}
	var notes []string

	if src.PrivateJSON != "" && exists(src.PrivateJSON) {
		var node *jsonNode
		if err := guard("私人字符串名单", func() error {
			b, err := os.ReadFile(src.PrivateJSON)
			if err != nil {
				return err
			}
			node, err = decodeJSON(b)
			return err
		}); err != nil {
			return nil, nil, err
		}
		if node.kind != "arr" {
			return nil, nil, &SourceError{"私人字符串名单不是数组"}
		}
		for _, e := range node.vals {
			if e.kind == "str" {
				if t := jsTrim(e.s); jsUTF16Len(t) >= 4 {
					l.secrets.add(NormMail(t))
				}
			}
		}
	}

	if exists(src.QQDB) {
		if err := guard("Go 聊天库", func() error { return l.loadQQ(src.QQDB) }); err != nil {
			return nil, nil, err
		}
	} else {
		notes = append(notes, "缺 Go 聊天库")
	}

	if exists(src.MaibotDB) {
		if err := guard("MaiBot 库", func() error { return l.loadMaibot(src.MaibotDB, &notes) }); err != nil {
			return nil, nil, err
		}
	} else if src.MaibotDB != "" {
		notes = append(notes, "缺 MaiBot 库") // 生产环境不配这个库（只有测试构建能用环境变量指一个），没配不算缺失
	}

	if exists(src.GroupsJSON) {
		var node *jsonNode
		if err := guard("群名表", func() error {
			b, err := os.ReadFile(src.GroupsJSON)
			if err != nil {
				return err
			}
			node, err = decodeJSON(b)
			return err
		}); err != nil {
			return nil, nil, err
		}
		l.walkGroups(node)
	} else {
		notes = append(notes, "缺群名表")
	}

	if exists(src.ConfigYML) {
		if err := guard("配置文件", func() error {
			b, err := os.ReadFile(src.ConfigYML)
			if err != nil {
				return err
			}
			return l.loadConfig(b)
		}); err != nil {
			return nil, nil, err
		}
	} else {
		notes = append(notes, "缺配置文件")
	}
	return &Known{numbers: l.numbers, names: l.names, secrets: l.secrets}, notes, nil
}

func (l *loader) loadQQ(path string) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	t, err := tableSet(db)
	if err != nil {
		return err
	}
	each := func(q string, f func(r []any)) error {
		rows, _, err := queryRows(db, q)
		if err != nil {
			return err
		}
		for _, r := range rows {
			f(r)
		}
		return nil
	}
	first := func(r []any) { l.addNums(valStr(r[0])) }
	if err := each("SELECT DISTINCT actor_id a FROM events WHERE actor_kind != 'character'", first); err != nil {
		return err
	}
	if err := each("SELECT DISTINCT conversation c FROM events", first); err != nil {
		return err
	}
	// 只取号码不取昵称：聊天库里几百个昵称有不少是常见英文词，加进名字名单全仓误报四千多处；名字用 MaiBot 和群名表
	if err := each("SELECT uid FROM qq_people", first); err != nil {
		return err
	}
	if err := each("SELECT gid, name FROM qq_groups", func(r []any) {
		l.addNums(valStr(r[0]))
		if s, ok := r[1].(string); ok {
			l.addName(s)
		}
	}); err != nil {
		return err
	}
	// 她自己的号：这几张表的 account 列
	for _, tb := range []string{"qq_sync", "outbound_plans", "qq_alerts", "qq_backfill_continuity", "channel_alignment_latest"} {
		if !t[tb] {
			continue
		}
		c, err := colSet(db, tb)
		if err != nil {
			return err
		}
		if c["account"] {
			if err := each("SELECT DISTINCT account a FROM "+tb, first); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *loader) loadMaibot(path string, notes *[]string) error {
	db, err := openRO(path)
	if err != nil {
		return err
	}
	defer db.Close()
	t, err := tableSet(db)
	if err != nil {
		return err
	}
	rows, _, err := queryRows(db, "SELECT user_id, person_name, user_nickname, group_cardname FROM person_info")
	if err != nil {
		return err
	}
	for _, r := range rows {
		l.addNums(valStr(r[0]))
		for _, v := range r[1:] {
			if s, ok := v.(string); ok {
				l.addName(s)
			}
		}
	}
	nums := []struct {
		tb   string
		want []string
	}{
		{"chat_sessions", []string{"group_id", "user_id", "account_id"}},
		{"mai_messages", []string{"user_id", "group_id", "account_id"}},
		{"bot_platform_accounts", []string{"account_id"}},
	}
	for _, n := range nums {
		if !t[n.tb] {
			*notes = append(*notes, "MaiBot 库没有 "+n.tb)
			continue
		}
		c, err := colSet(db, n.tb)
		if err != nil {
			return err
		}
		var have []string
		for _, w := range n.want {
			if c[w] {
				have = append(have, w)
			}
		}
		if len(have) > 0 {
			rs, _, err := queryRows(db, "SELECT DISTINCT "+strings.Join(have, ", ")+" FROM "+n.tb)
			if err != nil {
				return err
			}
			for _, r := range rs {
				for _, v := range r {
					l.addNums(valStr(v))
				}
			}
		}
	}
	if t["chat_sessions"] {
		c, err := colSet(db, "chat_sessions")
		if err != nil {
			return err
		}
		if c["group_name"] {
			rs, _, err := queryRows(db, "SELECT DISTINCT group_name g FROM chat_sessions")
			if err != nil {
				return err
			}
			for _, r := range rs {
				if s, ok := r[0].(string); ok {
					l.addName(s)
				}
			}
		}
	}
	return nil
}

func (l *loader) walkGroups(n *jsonNode) {
	switch n.kind {
	case "str":
		l.addNums(n.s)
		l.addName(n.s)
	case "num":
		l.addNums(n.s)
	case "arr", "obj":
		for _, k := range n.keys {
			l.addNums(k)
		}
		for _, v := range n.vals {
			l.walkGroups(v)
		}
	}
}

var (
	reCfgQQKey = regexp.MustCompile(`qq|uin|owner|group|account|conversation`)
	reNotIDEnd = regexp.MustCompile(`(?:port|timeout|interval|limit|seconds|minutes|delay|ttl|size|after)$`)
	reNotIDBeg = regexp.MustCompile(`^(?:max|min|retr)`)
	reNotIDCS  = regexp.MustCompile(`(?:Count|Ms)$`)

	reYInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reYOct   = regexp.MustCompile(`^0o[0-7]+$`)
	reYHex   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reYFloat = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
)

// notID：qq 段里的端口、超时、上限这类数字不是号（键名认）；只看键名的开头和结尾，中间含 count 的 accounts 是号。
func notID(k string) bool {
	lk := asciiLower(k)
	return reNotIDEnd.MatchString(lk) || reNotIDBeg.MatchString(lk) || reNotIDCS.MatchString(k)
}

// loadConfig 读 data/config/sumika.yml：配置的 qq 子树里的数字全收（她的号在 `- id:` 那行，多行列表也能读到）；别的段只收写在 QQ 相关键上的。
//
// 旧版用 Bun.YAML.parse，标量按 YAML 1.2 核心模式取值：十进制整数（012 读成 12、+12、1.）、0o 八进制、小写 0x 十六进制、浮点（1e3）读成数字再转字符串；
// 别的写法一律是字符串（0b10101010、1234567_8、1_000、0X1F、2026-11-18 都是字符串，按原文里的数字段收）。go-yaml 的解码器认的写法比这宽
// （0b、下划线分隔都会读成整数），照它的值收就会和旧版不一样，所以这里不用它的值，走 AST 取标量节点的原始文本，再按上面的规则自己判断。
// 多文档（---）旧版返回数组，每个文档都收；锚点和别名在别名出现的位置按当时的 qq 上下文收；`<<` 合并键把被合并的映射当成当前这层的内容。
//
// 新版比旧版严的地方（有意保留，方向是“拒绝读”而不是“放过”，退出码 2，提示改配置）：重复键、复杂键（`? [a, b]`）这类 go-yaml 的解码器报错的写法。
// 旧版对重复键后者覆盖前者、复杂键拼成 "a,b" 的键名，都不报错。
func (l *loader) loadConfig(b []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(b), yaml.UseOrderedMap())
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	f, err := parser.ParseBytes(b, 0)
	if err != nil {
		return err
	}
	for _, d := range f.Docs {
		w := &cfgWalker{l: l, anchors: map[string]ast.Node{}}
		w.walk(d.Body, false, 0)
	}
	return nil
}

type cfgWalker struct {
	l       *loader
	anchors map[string]ast.Node
}

// scalarText 是标量节点在旧版里 String(value) 的结果；第二个返回值 false 表示不是标量。
func scalarText(n ast.Node) (string, bool) {
	switch x := n.(type) {
	case *ast.StringNode:
		if t := x.GetToken(); t != nil && (t.Type == token.SingleQuoteType || t.Type == token.DoubleQuoteType) {
			return x.Value, true
		}
		return classifyScalar(rawText(x)), true
	case *ast.IntegerNode, *ast.FloatNode, *ast.InfinityNode, *ast.NanNode, *ast.BoolNode, *ast.NullNode:
		return classifyScalar(rawText(n)), true
	case *ast.LiteralNode:
		if x.Value != nil {
			return x.Value.Value, true
		}
		return "", true
	}
	return "", false
}

func rawText(n ast.Node) string {
	if t := n.GetToken(); t != nil {
		return t.Value
	}
	return ""
}

// classifyScalar 把没加引号的标量原文按 YAML 1.2 核心模式转成旧版得到的字符串。
func classifyScalar(s string) string {
	switch {
	case reYInt.MatchString(s), reYFloat.MatchString(s):
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s
		}
		return jsNum(f)
	case reYOct.MatchString(s):
		if v, err := strconv.ParseUint(s[2:], 8, 64); err == nil {
			return jsNum(float64(v))
		}
	case reYHex.MatchString(s):
		if v, err := strconv.ParseUint(s[2:], 16, 64); err == nil {
			return jsNum(float64(v))
		}
	}
	return s
}

func keyText(k ast.Node) string {
	if t, ok := scalarText(k); ok {
		return t
	}
	return k.String()
}

type cfgEntry struct {
	key   string
	val   ast.Node
	merge bool
}

func (w *cfgWalker) walk(n ast.Node, qq bool, depth int) {
	if n == nil || depth > 64 {
		return
	}
	switch x := n.(type) {
	case *ast.MappingNode:
		var es []cfgEntry
		for _, mv := range x.Values {
			es = append(es, entryOf(mv))
		}
		w.entries(es, qq, depth)
	case *ast.MappingValueNode:
		w.entries([]cfgEntry{entryOf(x)}, qq, depth)
	case *ast.SequenceNode:
		for _, v := range x.Values {
			w.walk(v, qq, depth+1)
		}
	case *ast.AnchorNode:
		w.anchors[keyText(x.Name)] = x.Value
		w.walk(x.Value, qq, depth+1)
	case *ast.AliasNode:
		w.walk(w.anchors[keyText(x.Value)], qq, depth+1)
	case *ast.TagNode:
		w.walk(x.Value, qq, depth+1)
	default:
		if t, ok := scalarText(n); ok && qq {
			w.l.addNums(t)
		}
	}
}

func entryOf(mv *ast.MappingValueNode) cfgEntry {
	if _, ok := mv.Key.(*ast.MergeKeyNode); ok {
		return cfgEntry{val: mv.Value, merge: true}
	}
	return cfgEntry{key: keyText(mv.Key), val: mv.Value}
}

// entries 按 JS 对象的键顺序（整数样子的键数值升序在前，其余按出现顺序）逐个收。
func (w *cfgWalker) entries(es []cfgEntry, qq bool, depth int) {
	var ints, rest []cfgEntry
	for _, e := range es {
		if !e.merge && reIndexKey.MatchString(e.key) && len(e.key) < 11 {
			ints = append(ints, e)
		} else {
			rest = append(rest, e)
		}
	}
	sort.SliceStable(ints, func(a, b int) bool {
		p, _ := strconv.ParseInt(ints[a].key, 10, 64)
		q, _ := strconv.ParseInt(ints[b].key, 10, 64)
		return p < q
	})
	for _, e := range append(ints, rest...) {
		if e.merge {
			w.walk(e.val, qq, depth+1) // 合并键：被合并的内容算当前这层的
		} else if !notID(e.key) {
			w.walk(e.val, qq || reCfgQQKey.MatchString(asciiLower(e.key)), depth+1)
		}
	}
}
