//go:build !linux

package dhcpserver

import (
	"errors"
	"net"
)

func newUnicaster(net.IP) (unicaster, error) {
	return nil, errors.New("direct-to-MAC replies are only supported on Linux")
}
