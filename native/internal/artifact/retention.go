package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sweep 只由宿主显式维护触发，空闲时没有定时器或目录轮询。
func (s *Store) Sweep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make([]string, 0, len(s.activeTools))
	for tool := range s.activeTools {
		active = append(active, tool)
	}
	if err := s.DB.ReapResultReservations(ctx, s.Now().Add(-24*time.Hour).UnixMilli(), active); err != nil {
		return err
	}
	refs, err := s.DB.ExpiredResults(ctx, s.Now().Add(-time.Duration(s.Limits.UnreferencedDays)*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	for _, ref := range refs {
		deleted, err := s.DB.DeleteResult(ctx, ref)
		if err != nil {
			return err
		}
		if !deleted {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, ref.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(s.verified, ref.ID)
	}
	files, err := s.DB.ResultFiles(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := s.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() || files[e.Name()] || s.active[e.Name()] || (!validID(e.Name()) && !strings.HasPrefix(e.Name(), ".collect-")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
