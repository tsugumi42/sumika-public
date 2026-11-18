package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointMaterialForeignFile(t *testing.T) {
	for _, replaceDirectory := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact-name", true: "ordinary-directory-replaced"}[replaceDirectory], func(t *testing.T) {
			f := newFixture(t, nil)
			nested := filepath.Join(f.work, "nested")
			require(t, os.Mkdir(nested, 0700))
			require(t, os.WriteFile(filepath.Join(nested, "a"), []byte("original"), 0600))
			rec := f.capture(t, filepath.Join("nested", "a"), "agent")
			plan, err := f.s.Preview(t.Context(), f.owner, rec.ID, "step")
			require(t, err)
			inverse, err := f.s.Restore(t.Context(), f.owner, f.in(), rec.ID, "step", plan.Version)
			require(t, err)
			if replaceDirectory {
				require(t, os.Rename(nested, filepath.Join(f.work, "moved")))
				require(t, os.Mkdir(nested, 0700))
			}
			neighbor := filepath.Join(nested, materialName(inverse.ID+"-0", true))
			require(t, os.WriteFile(neighbor, []byte("user-file"), 0600))
			_, err = New(f.data, f.s.opts)
			require(t, err)
			checkFile(t, neighbor, []byte("user-file"))
		})
	}
}
