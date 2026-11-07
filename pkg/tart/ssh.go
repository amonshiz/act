package tart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/kballard/go-shellquote"
	"golang.org/x/crypto/ssh"
)

// errConnectionLost is returned when the SSH connection drops while a
// command runs, so its result is unknown.
var errConnectionLost = errors.New("connection to VM lost")

const (
	sshDialAttemptTimeout = 5 * time.Second
	sshRedialTimeout      = 30 * time.Second
	// cancelGrace is how long Run waits for a cancelled command to exit
	// (the pgid kill sends SIGKILL after 5 s) before closing the session.
	cancelGrace = 8 * time.Second
)

// sshClientConfig builds the client config from the host settings.
//
// Host keys are not checked: the target is a VM act cloned moments ago, on
// the host-local virtual network, whose key is new on every clone, so
// there is nothing to pin it to.
func sshClientConfig(h HostConfig) (*ssh.ClientConfig, error) {
	var auth []ssh.AuthMethod
	if h.SSHKeyPath != "" {
		key, err := os.ReadFile(h.SSHKeyPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read --tart-ssh-key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("cannot parse --tart-ssh-key (keys with a passphrase are not supported): %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if h.SSHPassword != "" {
		pw := h.SSHPassword
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = pw
			}
			return answers, nil
		}))
	}
	return &ssh.ClientConfig{
		User:            h.SSHUser,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // see the function comment
		Timeout:         sshDialAttemptTimeout,
	}, nil
}

// sshTransport runs commands over one SSH connection, opening a session per
// command.
type sshTransport struct {
	addr   string
	config *ssh.ClientConfig

	mu     sync.Mutex
	client *ssh.Client
}

func dialOnce(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: sshDialAttemptTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(sshDialAttemptTimeout * 2))
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// dialWithRetry dials until it succeeds or ctx ends, backing off between
// attempts; sshd may not be listening yet while the guest boots.
func dialWithRetry(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	backoff := 250 * time.Millisecond
	var lastErr error
	for {
		c, err := dialOnce(ctx, addr, config)
		if err == nil {
			return c, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cannot connect to the VM over SSH at %s: %w", addr, lastErr)
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// newSSHTransport connects to addr, retrying until ctx ends.
func newSSHTransport(ctx context.Context, addr string, config *ssh.ClientConfig) (*sshTransport, error) {
	c, err := dialWithRetry(ctx, addr, config)
	if err != nil {
		return nil, err
	}
	return &sshTransport{addr: addr, config: config, client: c}, nil
}

// session opens a session, redialling once if the connection is gone.
// Redialling is safe here because no command has started yet.
func (t *sshTransport) session(ctx context.Context) (*ssh.Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		s, err := t.client.NewSession()
		if err == nil {
			return s, nil
		}
		t.client.Close()
		t.client = nil
	}
	dctx, cancel := context.WithTimeout(ctx, sshRedialTimeout)
	defer cancel()
	c, err := dialWithRetry(dctx, t.addr, t.config)
	if err != nil {
		return nil, err
	}
	t.client = c
	return c.NewSession()
}

func (t *sshTransport) Run(ctx context.Context, argv []string, out io.Writer) (int, error) {
	sess, err := t.session(ctx)
	if err != nil {
		return -1, err
	}
	defer sess.Close()
	w := &lockedWriter{w: out}
	sess.Stdout = w
	sess.Stderr = w
	if err := sess.Start(shellquote.Join(argv...)); err != nil {
		return -1, fmt.Errorf("cannot start command in the VM: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return sshExit(err)
	case <-ctx.Done():
	}
	select {
	case err := <-done:
		code, _ := sshExit(err)
		return code, ctx.Err()
	case <-time.After(cancelGrace):
		sess.Close()
		<-done
		return -1, ctx.Err()
	}
}

// sshExit maps a session result to an exit code.
func sshExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		if sig := ee.Signal(); sig != "" {
			return -1, fmt.Errorf("command was terminated by signal SIG%s", sig)
		}
		return ee.ExitStatus(), nil
	}
	var em *ssh.ExitMissingError
	if errors.As(err, &em) || errors.Is(err, io.EOF) {
		return -1, errConnectionLost
	}
	return -1, fmt.Errorf("%w: %v", errConnectionLost, err)
}

func (t *sshTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client == nil {
		return nil
	}
	err := t.client.Close()
	t.client = nil
	return err
}
