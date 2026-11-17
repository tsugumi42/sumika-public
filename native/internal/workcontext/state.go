// Package workcontext 从持久来源序列构建工作状态；不会写聊天或记忆。
package workcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"sumika-native/internal/artifact"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

type Delta struct {
	Goal                                     string
	Constraints, Completed, Pending, Unknown []string
	ReplaceTodo                              bool
	Evidence                                 []kernel.Evidence
	Next                                     string
}
type Entry struct {
	Message  json.RawMessage
	Delta    Delta
	Tokens   int
	Boundary bool
	Group    string
	Calls    int
	Result   bool
	// RunID 是写这一条的那次运行，来自 work_entries.run_id，不进条目本身（digest 不变）。
	RunID string `json:"-"`
}
type Service struct {
	DB      *store.Store
	Results kernel.ResultStore
	Limits  config.Work
}

func New(db *store.Store, results kernel.ResultStore, limits config.Work) *Service {
	if limits == (config.Work{}) {
		limits = config.DefaultWork()
	}
	return &Service{db, results, limits}
}
func (s *Service) Open(ctx context.Context, inv kernel.Invocation, stamp string) error {
	if !artifact.ValidScope(inv.Scope) || inv.RootID == "" {
		return artifact.ErrUnavailable
	}
	if _, err := s.DB.OpenWork(ctx, inv, stamp); err != nil {
		return err
	}
	return s.DB.SetWorkStamp(ctx, inv, stamp)
}
func (s *Service) Record(ctx context.Context, inv kernel.Invocation, e Entry) error {
	if !artifact.ValidScope(inv.Scope) {
		return artifact.ErrUnavailable
	}
	if _, err := s.DB.LoadWork(ctx, inv); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	return s.DB.AppendWork(ctx, inv, b, hex.EncodeToString(h[:]))
}
func apply(state *kernel.WorkState, d Delta) {
	if state.Goal == "" {
		state.Goal = d.Goal
	}
	state.Constraints = append(state.Constraints, d.Constraints...)
	state.Completed = append(state.Completed, d.Completed...)
	state.Unknown = append(state.Unknown, d.Unknown...)
	if d.ReplaceTodo {
		state.Pending = d.Pending
	} else {
		state.Pending = append(state.Pending, d.Pending...)
	}
	state.Evidence = append(state.Evidence, d.Evidence...)
	if d.Next != "" {
		state.Next = d.Next
	}
}
func (s *Service) validateEvidence(ctx context.Context, owner kernel.Scope, e kernel.Evidence) error {
	if e.Ref.Owner != owner || !e.Ref.Complete || e.Start < 0 || e.End < e.Start || e.End > e.Ref.Bytes {
		return fmt.Errorf("工作证据归属、完整性或字节范围不符")
	}
	page, err := s.Results.Read(ctx, owner, e.Ref.ID, e.Start, int64(s.Limits.ReadPageBytes))
	if err != nil {
		return err
	}
	if page.Ref != e.Ref {
		return fmt.Errorf("工作证据不是已登记原文")
	}
	if e.End < e.Ref.Bytes {
		if _, err := s.Results.Read(ctx, owner, e.Ref.ID, e.End, int64(s.Limits.ReadPageBytes)); err != nil {
			return err
		}
	}
	return nil
}
