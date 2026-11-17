//go:build !windows

package projectcontext

import "os"

type fileIdentity struct{ info os.FileInfo }

func identify(path string) (os.FileInfo, fileIdentity, error) {
	info, err := os.Stat(path)
	return info, fileIdentity{info}, err
}

func sameIdentity(a, b fileIdentity) bool { return os.SameFile(a.info, b.info) }
