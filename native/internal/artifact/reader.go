package artifact

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

func allowed(scope kernel.Scope, name string) bool {
	spec, ok := policy.Specs[name]
	if !ok || name == "result_read" {
		return false
	}
	if spec.Audience == "owner-console" {
		return scope.Channel == "console" && scope.Scene == "private" && scope.ActorKind == "owner" && !strings.Contains(scope.Conversation, ":group:")
	}
	if name == "remember" || name == "memory_forget" {
		return scope.Scene == "private" && scope.ActorKind == "owner"
	}
	return spec.Audience == "any"
}
func (s *Store) Read(ctx context.Context, owner kernel.Scope, id string, offset, limit int64) (kernel.ResultPage, error) {
	var page kernel.ResultPage
	if !ValidScope(owner) || !validID(id) {
		return page, ErrUnavailable
	}
	ref, name, err := s.DB.ResultRef(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return page, ErrUnavailable
		}
		return page, err
	}
	if ref.Owner != owner || !allowed(owner, name) || s.SourceAllowed != nil && !s.SourceAllowed(owner, name) {
		return page, ErrUnavailable
	}
	if offset < 0 || offset > ref.Bytes || limit <= 0 || limit > int64(s.Limits.ReadPageBytes) {
		return page, fmt.Errorf("offset/limit 超出结果字节范围或读取页上限")
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return page, fmt.Errorf("结果 unavailable：%w", err)
	}
	defer root.Close()
	f, err := root.Open(ref.ID)
	if err != nil {
		return page, fmt.Errorf("结果 unavailable：%w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return page, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != ref.Bytes {
		return page, fmt.Errorf("结果 unavailable：文件大小或类型不符")
	}
	version := fileVersion{fi.Size(), fi.ModTime()}
	s.mu.Lock()
	verified := s.verified[id] == version
	s.mu.Unlock()
	if !verified {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return page, err
		}
		if hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
			return page, fmt.Errorf("结果 unavailable：SHA256 不符")
		}
		s.mu.Lock()
		s.verified[id] = version
		s.mu.Unlock()
	}
	// 页的预算含最终工具包装；给固定字段留 512 字节，剩余全给正文。
	size := min(limit-512, ref.Bytes-offset)
	if size < 0 {
		return page, fmt.Errorf("limit 太小，无法容纳结果包装")
	}
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
		return page, err
	}
	if len(buf) > 0 && !utf8.RuneStart(buf[0]) {
		return page, fmt.Errorf("offset 不是 UTF-8 字符边界")
	}
	for len(buf) > 0 && !utf8.Valid(buf) {
		buf = buf[:len(buf)-1]
	}
	if len(buf) == 0 && offset < ref.Bytes {
		return page, fmt.Errorf("limit 装不下首字符，请增大读取页")
	}
	page = kernel.ResultPage{Ref: ref, Text: string(buf), Start: offset, End: offset + int64(len(buf)), Next: offset + int64(len(buf))}
	page.EOF = page.End == ref.Bytes
	return page, nil
}
