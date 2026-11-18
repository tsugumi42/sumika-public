package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type materialIdentity struct {
	Token, Parent, File string
}

type materialJournal struct {
	file *os.File
	mu   sync.Mutex
}

func newMaterialJournal(dir *os.Root) (*materialJournal, error) {
	f, err := dir.OpenFile("materials.next", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return nil, err
	}
	if err = durableRename(dir, "materials.next", "materials.jsonl"); err != nil {
		return nil, err
	}
	f, err = dir.OpenFile("materials.jsonl", os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &materialJournal{file: f}, nil
}

// 正文写入或墓碑改名前，先持久登记实际创建/打开的对象；名字本身不能证明归属。
func (j *materialJournal) register(parent, file *os.File, token string) (materialIdentity, error) {
	p, err := checkHandle(parent, parent.Name())
	if err != nil {
		return materialIdentity{}, err
	}
	f, err := checkHandle(file, file.Name())
	if err != nil {
		return materialIdentity{}, err
	}
	identity := materialIdentity{token, p, f}
	body, err := json.Marshal(identity)
	if err != nil {
		return materialIdentity{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err = j.file.Write(append(body, '\n'))
	return identity, errors.Join(err, j.file.Sync())
}

func materialName(token string, replacement bool) string {
	suffix := ".delete"
	if replacement {
		suffix = ".tmp"
	}
	return ".sumika-checkpoint-" + token + suffix
}

func cleanupRestoreMaterials(rec *Record, storage *os.Root) (err error) {
	if rec.InverseOf == "" {
		return nil
	}
	identities := map[string]materialIdentity{}
	journal, err := storage.Open(filepath.Join(rec.ID, "materials.jsonl"))
	if err == nil {
		if _, err := checkHandle(journal, filepath.Join(storage.Name(), rec.ID, "materials.jsonl")); err != nil {
			return errors.Join(err, journal.Close())
		}
		decoder := json.NewDecoder(io.LimitReader(journal, metadataLimit+1))
		for count := 0; ; count++ {
			var identity materialIdentity
			decodeErr := decoder.Decode(&identity)
			if errors.Is(decodeErr, io.EOF) {
				break
			}
			if decodeErr != nil {
				err = decodeErr
				break
			}
			if count >= len(rec.After) || identity.Parent == "" || identity.File == "" || identities[identity.Token].Token != "" {
				err = ErrUnavailable
				break
			}
			identities[identity.Token] = identity
		}
		err = errors.Join(err, journal.Close())
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	work, identity, err := openWorkspace(rec.CWD)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, work.Close()) }()
	if identity != rec.RootIdentity {
		return ErrLink
	}
	paths := make([]string, 0, len(rec.After))
	for path := range rec.After {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for start := 0; start < len(paths); {
		folder := filepath.Dir(paths[start])
		end := start + 1
		for end < len(paths) && filepath.Dir(paths[end]) == folder {
			end++
		}
		dir, err := work.OpenRoot(folder)
		if errors.Is(err, os.ErrNotExist) {
			start = end
			continue
		}
		if err != nil {
			return err
		}
		for i := start; i < end; i++ {
			token := fmt.Sprintf("%s-%d", rec.ID, i)
			if err = removeRestoreMaterial(dir, materialName(token, rec.After[paths[i]].Exists), identities[token]); err != nil {
				break
			}
		}
		err = errors.Join(err, dir.Close())
		if err != nil {
			return err
		}
		start = end
	}
	return nil
}

var deleteRestoreMaterial = deleteMaterialHandle

func removeRestoreMaterial(dir *os.Root, name string, identity materialIdentity) (err error) {
	parent, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	f, err := openMaterialFile(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	actual, err := checkHandle(f, filepath.Join(dir.Name(), name))
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	p, err := checkHandle(parent, dir.Name())
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || identity.File == "" || actual != identity.File || p != identity.Parent {
		return fmt.Errorf("材料身份不符，保留 %s：%w", name, ErrConflict)
	}
	return deleteRestoreMaterial(f)
}
