package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/ecosystem"
	"testing"
)

func TestSkillApprovalReadsOpenedFile(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "demo", "public instructions")
	resource := filepath.Join(root, "demo", "resource.txt")
	if err := os.WriteFile(resource, []byte("approved-public-content"), 0600); err != nil {
		t.Fatal(err)
	}
	_, a := skillAccess(t, "skill:root")
	replaced := false
	c, err := New(Options{Enabled: true, Roots: []Root{{ID: "root", Path: root}}, Approve: func(ctx context.Context, a ecosystem.Access, target string) error {
		if target == resource && !replaced {
			replaced = true
			if err := os.Rename(resource, resource+".old"); err != nil {
				return err
			}
			return os.WriteFile(resource, []byte("unapproved-replacement"), 0600)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Scan(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	p, err := c.Read(t.Context(), a, "ctx", Request{Name: "demo", Resource: "resource.txt"})
	if err != nil {
		t.Fatal(err)
	}
	p.Disposition.Discard()
	if !replaced || p.Text != "approved-public-content" || strings.Contains(p.Text, "unapproved") {
		t.Fatalf("审批读的不是同一文件 replaced=%v text=%q", replaced, p.Text)
	}
}
