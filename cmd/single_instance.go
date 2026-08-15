package cmd

import (
	"net"

	"aliang.one/nursorgate/internal/singleinstance"
)

func acquireSingleInstanceGuard() (net.Listener, bool, error) {
	return singleinstance.Acquire()
}
