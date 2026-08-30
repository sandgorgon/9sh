//go:build !linux

package remote

import (
	"net"
	"os"
)

// peerUID is the non-Linux fallback: SO_PEERCRED-style peer-credential
// lookup has no portable stdlib equivalent (Darwin's LOCAL_PEERCRED and
// BSD's LOCAL_PEERCRED/getpeereid use different APIs entirely), so
// ListenUnix's UID check degrades to a no-op here rather than failing
// every connection outright — the socket file's own 0600 permissions
// (still enforced on every platform) remain the trust boundary, matching
// the original design baseline before SO_PEERCRED was added as an
// additional, Linux-only layer on top of it. Returning the caller's own
// UID unconditionally means peerCredListener's UID comparison always
// passes here, i.e. this platform gets exactly the trust model dirfs's
// local-directory bind already relies on everywhere, no more, no less.
func peerUID(c *net.UnixConn) (uint32, error) {
	return uint32(os.Getuid()), nil
}
