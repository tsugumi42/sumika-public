//go:build !windows

package ctlserver

import (
	"net"

	"sumika-native/internal/ctlproto"
)

// ListenPipe 非 Windows 先不做
func ListenPipe(string) (net.Listener, error) { return nil, ctlproto.ErrUnsupported }
