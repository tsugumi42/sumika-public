//go:build !windows

package ctlproto

import (
	"context"
	"errors"
	"net"
)

// ErrUnsupported：本机控制接口走 Windows 命名管道，别的系统先不做
var ErrUnsupported = errors.New("本机控制接口只支持 Windows")

func CurrentSID() (string, error)                    { return "", ErrUnsupported }
func PipeName(demo bool) (string, error)             { return "", ErrUnsupported }
func Dial(context.Context, string) (net.Conn, error) { return nil, ErrUnsupported }
