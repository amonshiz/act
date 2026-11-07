//go:build !windows

// Package sshtest is an in-process SSH server for tests. It runs each exec
// request with /bin/sh -c in a new session, as sshd does, and reports the
// real exit status.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is a test SSH server listening on 127.0.0.1.
type Server struct {
	User     string
	Password string
	Env      []string // environment of commands; nil inherits the test's

	listener net.Listener
	config   *ssh.ServerConfig

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	commands []string
	wg       sync.WaitGroup
}

// New starts a server that accepts user/password and stops it when the
// test ends.
func New(t testing.TB, user, password string) *Server {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{User: user, Password: password, conns: map[net.Conn]struct{}{}}
	s.config = &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == s.User && string(pw) == s.Password {
				return nil, nil
			}
			return nil, errors.New("access denied")
		},
	}
	s.config.AddHostKey(signer)
	s.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Addr is host:port.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Port is the listening port.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

// Commands returns every exec request received so far.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// DropConnections closes every open connection, as if the VM's network
// went away.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// Close stops the server.
func (s *Server) Close() {
	s.listener.Close()
	s.DropConnections()
	s.wg.Wait()
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()
	_, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleSession(ch, chReqs)
	}
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer s.wg.Done()
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		s.mu.Lock()
		s.commands = append(s.commands, payload.Command)
		s.mu.Unlock()
		go ssh.DiscardRequests(reqs)
		s.run(ch, payload.Command)
		return
	}
}

func (s *Server) run(ch ssh.Channel, command string) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = s.Env
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		sendExitStatus(ch, 0)
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := ws.Signal().String()
			if name, ok := signalNames[ws.Signal()]; ok {
				sig = name
			}
			_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(struct {
				Signal     string
				CoreDumped bool
				Msg        string
				Lang       string
			}{Signal: sig}))
			return
		}
		sendExitStatus(ch, ee.ExitCode())
	default:
		_, _ = io.WriteString(ch.Stderr(), err.Error()+"\n")
		sendExitStatus(ch, 127)
	}
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGTERM: "TERM",
	syscall.SIGKILL: "KILL",
	syscall.SIGINT:  "INT",
	syscall.SIGHUP:  "HUP",
}

func sendExitStatus(ch ssh.Channel, code int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	_, _ = ch.SendRequest("exit-status", false, b)
}
