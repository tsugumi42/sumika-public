package web

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
)

type flightLifetime struct{ done chan error }
type flightActivity struct{ done chan error }

func (l flightLifetime) Begin(ctx context.Context, _ kernel.Invocation) (context.Context, kernel.Activity, error) {
	return ctx, flightActivity(l), nil
}
func (flightLifetime) Seal()                       {}
func (flightLifetime) Drain(context.Context) error { return nil }
func (a flightActivity) End(err error)             { a.done <- err }

type joinedContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (c *joinedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}
func TestWebSingleflightWaiterCancellation(t *testing.T) {
	s, err := New(Options{Timeout: time.Minute, MaxInflightPerTask: 2, Lifetime: flightLifetime{make(chan error, 1)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := kernel.Scope{CharacterID: "fixture", Conversation: "fixture", ScopeID: "fixture", Platform: "local", Channel: "console", AccountID: "30001", Scene: "private", ActorKind: "owner", ActorID: "30002", AuthorityID: "fixture", AuthorityRevision: 1}
	g, err := ecosystem.NewAuthority().Issue(scope, []string{"web:search"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAccess(g, kernel.Invocation{Scope: scope, RootID: "root", RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	call := func(ctx context.Context) (any, error) {
		close(entered)
		select {
		case <-release:
			return "work-complete", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	firstCtx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() { _, err := s.shared(firstCtx, a, "same", call); firstDone <- err }()
	<-entered
	secondCtx := &joinedContext{Context: t.Context(), joined: make(chan struct{})}
	secondDone := make(chan error, 1)
	go func() {
		v, err := s.shared(secondCtx, a, "same", call)
		if err == nil && v != "work-complete" {
			err = fmt.Errorf("共享结果错误")
		}
		secondDone <- err
	}()
	<-secondCtx.joined
	cancel()
	if err := <-firstDone; err == nil {
		t.Fatal("首等待者取消未生效")
	}
	close(release)
	if err := <-secondDone; err != nil {
		t.Fatal("首等待者取消了共享执行", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
