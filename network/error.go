// Author:  Niels A.D.
// Project: gowarcraft3 (https://github.com/nielsAD/gowarcraft3)
// License: Mozilla Public License, v2.0

package network

import (
	"io"
	"net"
	"os"
	"syscall"

	"github.com/gorilla/websocket"
)

// AsyncError keeps track of where a non-fatal asynchronous error orignated
type AsyncError struct {
	Src string
	Err error
}

func (e *AsyncError) Error() string {
	if e.Err == nil {
		return e.Src + ":NIL"
	}
	return e.Src + ":" + e.Err.Error()
}

// Temporary error
func (e *AsyncError) Temporary() bool {
	return IsTemporaryError(e.Err)
}

// Timeout occurred
func (e *AsyncError) Timeout() bool {
	return IsTimeoutError(e.Err)
}

// UnnestError retrieves the innermost error
func UnnestError(err error) error {
	switch e := err.(type) {
	case *AsyncError:
		return UnnestError(e.Err)
	case *net.OpError:
		return UnnestError(e.Err)
	case *os.SyscallError:
		return UnnestError(e.Err)
	case *os.PathError:
		return UnnestError(e.Err)
	case *os.LinkError:
		return UnnestError(e.Err)
	default:
		return err
	}
}

// IsUseClosedNetworkError checks if net.error is poll.ErrNetClosed
func IsUseClosedNetworkError(err error) bool {
	return err != nil && err.Error() == "use of closed network connection"
}

// IsSysCallError checks if error is one of syscall.Errno
func IsSysCallError(err error, errno ...syscall.Errno) bool {
	err = UnnestError(err)
	if err == nil {
		return false
	}

	n, ok := err.(syscall.Errno)
	if !ok {
		return false
	}

	for _, e := range errno {
		if e == n {
			return true
		}
	}

	return false
}

// WSAECONNREFUSED is ECONNREFUSED on Windows
const WSAECONNREFUSED = 10061

// IsConnRefusedError checks if err indicates a refused connection
func IsConnRefusedError(err error) bool {
	err = UnnestError(err)

	if IsSysCallError(err, syscall.ECONNREFUSED, WSAECONNREFUSED) {
		return true
	}

	return websocket.IsCloseError(err,
		websocket.CloseTLSHandshake,
		websocket.CloseMandatoryExtension,
	)
}

// WSAECONNABORTED is ECONNABORTED on Windows
const WSAECONNABORTED = 10053

// WSAECONNRESET is ECONNRESET on Windows
const WSAECONNRESET = 10054

// WSAENOTCONN is ENOTCONN on Windows
const WSAENOTCONN = 10057

// WSAESHUTDOWN is ESHUTDOWN on Windows
const WSAESHUTDOWN = 10058

// IsConnClosedError checks if err indicates a closed connection
func IsConnClosedError(err error) bool {
	err = UnnestError(err)
	if err == io.EOF || IsUseClosedNetworkError(err) {
		return true
	}

	if IsSysCallError(err,
		syscall.ECONNABORTED,
		syscall.ECONNRESET,
		syscall.ENOTCONN,
		syscall.ESHUTDOWN,
		syscall.EPIPE,
		WSAECONNABORTED,
		WSAECONNRESET,
		WSAENOTCONN,
		WSAESHUTDOWN,
	) {
		return true
	}

	return err == websocket.ErrCloseSent || websocket.IsUnexpectedCloseError(err)
}

type temporary interface {
	Temporary() bool
}

// IsTemporaryError checks is error is temporary
func IsTemporaryError(err error) bool {
	if err == nil {
		return false
	}

	t, ok := err.(temporary)
	if !ok {
		t, ok = UnnestError(err).(temporary)
	}
	if ok {
		return t.Temporary()
	}

	return IsTimeoutError(err)
}

type timeout interface {
	Timeout() bool
}

// IsTimeoutError checks is error is timeout
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	t, ok := err.(timeout)
	if !ok {
		t, ok = UnnestError(err).(timeout)
	}
	return ok && t.Timeout()
}
