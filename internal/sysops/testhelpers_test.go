package sysops

import (
	"net"
	"testing"
	"time"
)

const shortTimeout = time.Second

type localListener struct {
	port int
	conn net.Listener
}

func (l localListener) Close() error { return l.conn.Close() }

// listenLocal opens a loopback port the test can probe.
func listenLocal(t *testing.T) localListener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener is not TCP")
	}
	port := tcpAddr.Port
	return localListener{port: port, conn: ln}
}
