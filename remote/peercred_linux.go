package remote

import (
	"net"
	"syscall"
)

// peerUID returns the UID of the process on the other end of c via
// SO_PEERCRED — see ListenUnix's doc comment for why this exists as a
// belt-and-suspenders check on top of the socket file's own permissions.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			sockErr = err
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	return uid, sockErr
}
