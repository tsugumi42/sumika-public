//go:build !windows

package secrets

func unprotect(_ []byte) ([]byte, error) {
	return nil, ErrUnsupported
}

func protect(_ []byte) ([]byte, error) {
	return nil, ErrUnsupported
}
