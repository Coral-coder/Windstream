package netx

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
)

// TCPFree reports whether a TCP port can be bound on all interfaces.
func TCPFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// UDPFree reports whether a UDP port can be bound on all interfaces.
func UDPFree(port int) bool {
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	pc.Close()
	return true
}

// LoopbackTCPFree reports whether a TCP port can be bound on 127.0.0.1.
func LoopbackTCPFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// PickPort returns the first preferred port that is free, else a random free
// port in 20000-49151 (below Windows' dynamic range, so it will not collide
// with outgoing connections). Ports in avoid are skipped.
func PickPort(free func(int) bool, preferred []int, avoid ...int) (int, error) {
	skip := map[int]bool{}
	for _, a := range avoid {
		skip[a] = true
	}
	for _, p := range preferred {
		if p > 0 && p < 65536 && !skip[p] && free(p) {
			return p, nil
		}
	}
	for i := 0; i < 300; i++ {
		p := 20000 + rand.IntN(29152)
		if !skip[p] && free(p) {
			return p, nil
		}
	}
	return 0, errors.New("no free port found")
}
