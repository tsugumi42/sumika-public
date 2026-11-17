package tokenizer

import (
	"encoding/json"
	"errors"
	"fmt"
)

// topLevelFields 找出 JSON 对象顶层每个键的值在 raw 里的字节范围 [start, end)，不解析值的内容。
// 为的是只读几百字节的 normalizer、post_processor，不去解 16 MB 的词表；拍平 normalizer 也靠它做字节拼接，
// 其余字节一个不碰。输入是已经核过哈希的 tokenizer.json，所以这里只认合法的 JSON，不合法就报错，不努力纠错。
func topLevelFields(raw []byte) (map[string][2]int, error) {
	s := scanner{b: raw}
	s.skipSpace()
	if !s.eat('{') {
		return nil, s.errorf("顶层不是对象")
	}
	fields := map[string][2]int{}
	s.skipSpace()
	if s.eat('}') {
		return fields, nil
	}
	for {
		s.skipSpace()
		keyStart := s.i
		if err := s.skipString(); err != nil {
			return nil, err
		}
		var key string
		if err := json.Unmarshal(raw[keyStart:s.i], &key); err != nil {
			return nil, s.errorf("键不是合法的字符串：%v", err)
		}
		s.skipSpace()
		if !s.eat(':') {
			return nil, s.errorf("键 %q 后面没有冒号", key)
		}
		s.skipSpace()
		start := s.i
		if err := s.skipValue(); err != nil {
			return nil, err
		}
		if _, dup := fields[key]; dup {
			return nil, s.errorf("键 %q 出现了两次", key)
		}
		fields[key] = [2]int{start, s.i}
		s.skipSpace()
		if s.eat(',') {
			continue
		}
		if s.eat('}') {
			break
		}
		return nil, s.errorf("键 %q 的值后面既不是逗号也不是右花括号", key)
	}
	s.skipSpace()
	if s.i != len(raw) {
		return nil, s.errorf("顶层对象后面还有内容")
	}
	return fields, nil
}

type scanner struct {
	b []byte
	i int
}

func (s *scanner) errorf(format string, a ...any) error {
	return fmt.Errorf("tokenizer.json 第 %d 字节：%s", s.i, fmt.Sprintf(format, a...))
}

func (s *scanner) skipSpace() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

func (s *scanner) eat(c byte) bool {
	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++
		return true
	}
	return false
}

// skipString 跳过一个字符串（s.i 在开头的引号上），转义只认反斜杠加一个字符
func (s *scanner) skipString() error {
	if !s.eat('"') {
		return s.errorf("这里该是字符串")
	}
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '\\':
			s.i += 2
		case '"':
			s.i++
			return nil
		default:
			s.i++
		}
	}
	return s.errorf("字符串没有结尾")
}

// skipValue 跳过一个值：字符串、对象、数组，或者数字 / true / false / null
func (s *scanner) skipValue() error {
	if s.i >= len(s.b) {
		return s.errorf("值不见了")
	}
	switch s.b[s.i] {
	case '"':
		return s.skipString()
	case '{', '[':
		depth := 0
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case '"':
				if err := s.skipString(); err != nil {
					return err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return nil
				}
			}
			s.i++
		}
		return s.errorf("括号没有配平")
	default:
		start := s.i
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				if s.i == start {
					return s.errorf("这里该是一个值")
				}
				return nil
			}
			s.i++
		}
		return nil
	}
}

var errNoField = errors.New("没有这个键")

// field 取顶层键的原始字节；没有这个键返回 errNoField
func field(raw []byte, fields map[string][2]int, key string) ([]byte, error) {
	r, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("%w：%s", errNoField, key)
	}
	return raw[r[0]:r[1]], nil
}
