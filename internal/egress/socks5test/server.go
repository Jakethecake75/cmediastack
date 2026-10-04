// Package socks5test is a SOCKS5 server for tests: username/password
// authentication and UDP ASSOCIATE (RFC 1928 §7), recording how every datagram
// it relays was addressed. Domain destinations are resolved from a map the test
// supplies, so a test can prove a name reached the proxy unresolved.
package socks5test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
)

// Server is a running test proxy.
type Server struct {
	Addr string
	// RefuseUDP answers UDP ASSOCIATE with "command not supported".
	RefuseUDP bool
	// Names resolves domain destinations: "host" → "ip:port" of a local
	// listener; the destination port is replaced by the mapped one.
	Names map[string]string

	// Connects are the CONNECT destinations relayed.
	Connects []string

	mu   sync.Mutex
	sent []Datagram
}

// Datagram is one datagram the proxy relayed, as the client addressed it.
type Datagram struct {
	Domain string // set when addressed by name
	Addr   string // "ip:port" when addressed by IP
	Port   int
}

// Start runs a server until the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), Names: map[string]string{}}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// Sent returns what was relayed so far.
func (s *Server) Sent() []Datagram {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Datagram(nil), s.sent...)
}

func read(c net.Conn, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func (s *Server) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	head, err := read(c, 2)
	if err != nil {
		return
	}
	if _, err := read(c, int(head[1])); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 2})
	ver, err := read(c, 2)
	if err != nil || ver[0] != 1 {
		return
	}
	if _, err := read(c, int(ver[1])); err != nil {
		return
	}
	plen, err := read(c, 1)
	if err != nil {
		return
	}
	if _, err := read(c, int(plen[0])); err != nil {
		return
	}
	_, _ = c.Write([]byte{1, 0})

	req, err := read(c, 4)
	if err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		var a []byte
		if a, err = read(c, 4); err == nil {
			host = net.IP(a).String()
		}
	case 4:
		var a []byte
		if a, err = read(c, 16); err == nil {
			host = net.IP(a).String()
		}
	case 3:
		var n []byte
		if n, err = read(c, 1); err == nil {
			var a []byte
			if a, err = read(c, int(n[0])); err == nil {
				host = string(a)
			}
		}
	}
	if err != nil {
		return
	}
	pb, err := read(c, 2)
	if err != nil {
		return
	}
	if req[1] == 1 { // CONNECT: relay TCP
		s.connect(c, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
		return
	}
	if req[1] != 3 || s.RefuseUDP {
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}

	relay, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		return
	}
	defer func() { _ = relay.Close() }()
	ra, ok := relay.LocalAddr().(*net.UDPAddr)
	if !ok {
		return
	}
	reply := []byte{5, 0, 0, 1}
	reply = append(reply, ra.IP.To4()...)
	reply = binary.BigEndian.AppendUint16(reply, uint16(ra.Port)) // #nosec G115 -- a UDP port
	_, _ = c.Write(reply)

	go s.relay(relay)
	_, _ = io.Copy(io.Discard, c) // the association lives as long as this connection
}

// connect relays a CONNECT to its destination, both ways, until either side
// closes.
func (s *Server) connect(c net.Conn, dest string) {
	up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", dest)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = up.Close() }()
	s.mu.Lock()
	s.Connects = append(s.Connects, dest)
	s.mu.Unlock()
	_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// relay forwards the client's datagrams to their destinations and the answers
// back, wrapped in the SOCKS5 UDP header.
func (s *Server) relay(relay net.PacketConn) {
	var client net.Addr
	buf := make([]byte, 65536)
	for {
		n, from, err := relay.ReadFrom(buf)
		if err != nil {
			return
		}
		b := buf[:n]
		if client == nil || from.String() == client.String() {
			client = from
			if len(b) < 4 || b[2] != 0 {
				continue
			}
			var d Datagram
			var dest string
			rest := b[4:]
			switch b[3] {
			case 1:
				if len(rest) < 6 {
					continue
				}
				d.Port = int(binary.BigEndian.Uint16(rest[4:6]))
				d.Addr = net.JoinHostPort(net.IP(rest[:4]).String(), strconv.Itoa(d.Port))
				dest, rest = d.Addr, rest[6:]
			case 3:
				l := int(rest[0])
				if len(rest) < 1+l+2 {
					continue
				}
				d.Domain = string(rest[1 : 1+l])
				d.Port = int(binary.BigEndian.Uint16(rest[1+l : 3+l]))
				dest, rest = s.Names[d.Domain], rest[3+l:]
			default:
				continue
			}
			s.mu.Lock()
			s.sent = append(s.sent, d)
			s.mu.Unlock()
			if dest == "" {
				continue
			}
			to, err := net.ResolveUDPAddr("udp", dest)
			if err != nil {
				continue
			}
			_, _ = relay.WriteTo(rest, to)
			continue
		}
		// An answer from a destination: back to the client with its source.
		ua, ok := from.(*net.UDPAddr)
		if !ok || client == nil {
			continue
		}
		out := []byte{0, 0, 0, 1}
		out = append(out, ua.IP.To4()...)
		out = binary.BigEndian.AppendUint16(out, uint16(ua.Port)) // #nosec G115 -- a UDP port
		_, _ = relay.WriteTo(append(out, b...), client)
	}
}
