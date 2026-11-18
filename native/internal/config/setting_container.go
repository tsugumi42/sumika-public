package config

import (
	"bytes"
	"errors"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

// patchContainer replaces a complete collection while retaining its comments as
// standalone comments. Scalars, aliases and merge keys must have unambiguous spans.
func patchContainer(raw []byte, node ast.Node, literal string, path []string) ([]byte, error) {
	startPos := node.GetToken().Position
	if m, ok := node.(*ast.MappingNode); ok && !m.IsFlowStyle && len(m.Values) > 0 {
		startPos = m.Values[0].Key.GetToken().Position
	}
	start := byteAt(raw, startPos.Line, startPos.Column)
	var endOf func(ast.Node) (int, error)
	endOf = func(n ast.Node) (int, error) {
		switch v := n.(type) {
		case *ast.MappingNode:
			if v.IsFlowStyle {
				return byteAt(raw, v.End.Position.Line, v.End.Position.Column) + 1, nil
			}
			if len(v.Values) == 0 {
				return 0, modelError(path)
			}
			for _, entry := range v.Values {
				if entry.Key.IsMergeKey() {
					return 0, modelError(path)
				}
				if _, err := endOf(entry.Value); err != nil {
					return 0, err
				}
			}
			return endOf(v.Values[len(v.Values)-1].Value)
		case *ast.SequenceNode:
			if v.IsFlowStyle {
				return byteAt(raw, v.End.Position.Line, v.End.Position.Column) + 1, nil
			}
			if len(v.Values) == 0 {
				return 0, modelError(path)
			}
			for _, item := range v.Values {
				if _, err := endOf(item); err != nil {
					return 0, err
				}
			}
			return endOf(v.Values[len(v.Values)-1])
		case *ast.StringNode, *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.NullNode:
			p := n.GetToken().Position
			return scalarEnd(raw, byteAt(raw, p.Line, p.Column), false)
		default:
			return 0, modelError(path)
		}
	}
	end, err := endOf(node)
	if err != nil || end < start || end > len(raw) {
		return nil, modelError(path)
	}
	nl := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		nl = "\r\n"
	}
	var comments strings.Builder
	for _, tok := range lexer.Tokenize(strings.ReplaceAll(string(raw), "\r\n", "\n")) {
		if tok.Type != token.CommentType {
			continue
		}
		at := byteAt(raw, tok.Position.Line, tok.Position.Column)
		if at < start || at >= end {
			continue
		}
		lineEnd := bytes.IndexByte(raw[at:], '\n')
		if lineEnd < 0 {
			lineEnd = len(raw)
		} else {
			lineEnd += at
		}
		if lineEnd > end {
			return nil, errors.New("无法安全保留容器注释")
		}
		comments.Write(bytes.TrimSuffix(raw[at:lineEnd], []byte("\r")))
		comments.WriteString(nl)
		comments.WriteString(strings.Repeat(" ", startPos.Column-1))
	}
	return splice(raw, start, end, comments.String()+literal), nil
}
