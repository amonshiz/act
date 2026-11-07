package tart

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Scheme prefixes platform images that run in a Tart VM.
const Scheme = "tart://"

const (
	TransportSSH   = "ssh"
	TransportAgent = "agent"
)

// Options are the per-image settings from a tart:// platform URI.
type Options struct {
	// Image is passed to `tart clone`: an OCI reference or a local VM name.
	Image        string
	CPU          int
	MemoryMB     int
	Softnet      bool
	SoftnetAllow string
	Headless     bool
	Pull         bool
	Resolver     string
	Transport    string
}

// HasScheme reports whether uri starts with tart:// (case-insensitive).
func HasScheme(uri string) bool {
	return len(uri) >= len(Scheme) && strings.EqualFold(uri[:len(Scheme)], Scheme)
}

var optionParsers = map[string]func(*Options, string) error{
	"cpu":    func(o *Options, v string) error { return parsePositive(&o.CPU, v) },
	"memory": func(o *Options, v string) error { return parsePositive(&o.MemoryMB, v) },
	"softnet": func(o *Options, v string) error {
		return parseBool(&o.Softnet, v)
	},
	"softnet_allow": func(o *Options, v string) error {
		for _, cidr := range strings.Split(v, ",") {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
				return fmt.Errorf("%q is not a CIDR", cidr)
			}
		}
		o.SoftnetAllow = v
		return nil
	},
	"headless": func(o *Options, v string) error { return parseBool(&o.Headless, v) },
	"pull":     func(o *Options, v string) error { return parseBool(&o.Pull, v) },
	"resolver": func(o *Options, v string) error {
		switch v {
		case "dhcp", "arp", "agent":
			o.Resolver = v
			return nil
		}
		return errors.New("expected dhcp, arp or agent")
	},
	"transport": func(o *Options, v string) error {
		switch v {
		case TransportSSH, TransportAgent:
			o.Transport = v
			return nil
		}
		return errors.New("expected ssh or agent")
	},
}

func parsePositive(dst *int, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return errors.New("expected a positive integer")
	}
	*dst = n
	return nil
}

func parseBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return errors.New("expected 0 or 1")
	}
	*dst = b
	return nil
}

// ParsePlatform parses tart://<image>[?opt=val&...]. Unknown options are an
// error so typos surface.
func ParsePlatform(uri string) (Options, error) {
	o := Options{Headless: true, Resolver: "dhcp", Transport: TransportSSH}
	if !HasScheme(uri) {
		return o, fmt.Errorf("platform %q does not start with %s", uri, Scheme)
	}
	rest := uri[len(Scheme):]
	image, query, _ := strings.Cut(rest, "?")
	if image == "" {
		return o, fmt.Errorf("platform %q names no image", Redact(uri))
	}
	o.Image = image
	values, err := url.ParseQuery(query)
	if err != nil {
		return o, fmt.Errorf("invalid options in %q: %w", uri, err)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parse, ok := optionParsers[k]
		if !ok {
			return o, fmt.Errorf("unknown tart option %q in %q (known: cpu, memory, softnet, softnet_allow, headless, pull, resolver, transport)", k, uri)
		}
		if len(values[k]) != 1 {
			return o, fmt.Errorf("tart option %q is set more than once", k)
		}
		if err := parse(&o, values[k][0]); err != nil {
			return o, fmt.Errorf("invalid tart option %s=%q: %w", k, values[k][0], err)
		}
	}
	if o.SoftnetAllow != "" {
		o.Softnet = true
	}
	return o, nil
}

// Redact returns a tart:// URI without its query, for logging.
func Redact(uri string) string {
	base, _, _ := strings.Cut(uri, "?")
	return base
}

// Redacted returns the platform URI without options, for logging.
func (o Options) Redacted() string {
	return Scheme + o.Image
}

// HostConfig holds host-wide Tart settings that come from flags rather than
// from the platform URI, plus hooks for tests.
type HostConfig struct {
	SSHUser     string
	SSHPassword string
	SSHKeyPath  string
	MaxVMs      int
	KeepFailed  bool

	// TartBinary replaces the tart executable (tests).
	TartBinary string
	// SSHPort replaces port 22 (tests).
	SSHPort int
	// GuestRoot replaces the guest home directory, /Users/<user> (tests).
	GuestRoot string
	// LoginShell replaces /bin/zsh when capturing the login environment.
	LoginShell string
	// SkipPlatformCheck allows running on hosts other than macOS on Apple
	// Silicon (tests).
	SkipPlatformCheck bool
	// SkipMount assumes the shares are already visible under the guest
	// mount point instead of running mount_virtiofs (tests).
	SkipMount bool
}

const (
	DefaultSSHUser     = "admin"
	DefaultSSHPassword = "admin"
	DefaultMaxVMs      = 2
)

func (h HostConfig) withDefaults() HostConfig {
	if h.SSHUser == "" {
		h.SSHUser = DefaultSSHUser
	}
	if h.SSHPassword == "" && h.SSHKeyPath == "" {
		h.SSHPassword = DefaultSSHPassword
	}
	if h.MaxVMs <= 0 {
		h.MaxVMs = DefaultMaxVMs
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if h.LoginShell == "" {
		h.LoginShell = "/bin/zsh"
	}
	return h
}

// String hides the password.
func (h HostConfig) String() string {
	pw := ""
	if h.SSHPassword != "" {
		pw = "***"
	}
	return fmt.Sprintf("{SSHUser:%s SSHPassword:%s SSHKeyPath:%s MaxVMs:%d KeepFailed:%t}", h.SSHUser, pw, h.SSHKeyPath, h.MaxVMs, h.KeepFailed)
}

// GoString hides the password from %#v.
func (h HostConfig) GoString() string {
	return "tart.HostConfig" + h.String()
}
