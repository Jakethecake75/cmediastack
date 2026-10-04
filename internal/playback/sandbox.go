// Package playback owns everything between a file on disk and a stream a
// browser can play: probing what a file contains, deciding what can be done
// with it, and serving the bytes.
//
// # The parser is the threat
//
// This software downloads files chosen by strangers and then points ffmpeg at
// them. ffmpeg is indispensable and is also millions of lines of C whose whole
// job is parsing untrusted container data, with a CVE history to match. Every
// media server has this exposure; ADR-0020 is about its blast radius.
//
// Two things bound it here, and both are properties of how the child is
// started rather than of how carefully it is called:
//
//   - It runs in a network namespace containing nothing but a down loopback,
//     so a successful exploit cannot call out, cannot reach the LAN, and cannot
//     reach the NFS server the media is mounted from.
//   - It is handed an open FILE DESCRIPTOR and never a path, so it cannot open
//     anything it was not given. The os.Root containment of ADR-0015 therefore
//     extends into the parser instead of stopping at the process boundary.
package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ErrSandboxUnavailable means this kernel would not create the jail.
var ErrSandboxUnavailable = errors.New("playback: the sandbox could not be created")

// nobody is the uid and gid the child is mapped to inside its user namespace.
// The number is conventional; what matters is that it is not the uid the
// application runs as.
const nobody = 65534

// maxOutput caps what a child may write back. ffprobe's JSON for a normal file
// is a few kilobytes; a file crafted to produce megabytes of stream entries
// would otherwise be a memory-exhaustion vector against the parent, which is a
// cheaper attack than exploiting the parser.
const maxOutput = 1 << 20 // 1 MiB

// sandboxAttr returns the process attributes that build the jail.
//
// CLONE_NEWUSER is what makes the rest available to an unprivileged process:
// without it, an ordinary user may not create a network or PID namespace at
// all. The uid mapping is what the kernel requires to accompany it.
//
// GidMappingsEnableSetgroups stays false. Writing "deny" to setgroups is a
// precondition for an unprivileged gid mapping, and it also removes setgroups
// from the child — one fewer way for a compromised child to alter its own
// group membership.
func sandboxAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: nobody, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: nobody, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
		// The child dies with the parent. A probe outliving the process that
		// wanted it is a leak; one that outlives a crash is a mystery.
		Pdeathsig: syscall.SIGKILL,
	}
}

// Sandbox runs media tools in the jail described above.
type Sandbox struct {
	log *slog.Logger
	// available records whether the jail could be built. Determined once, by
	// trying, because the answer depends on the kernel and the container
	// runtime rather than on anything this program can read.
	available bool
	// permitUnsandboxed allows running without the jail on a host that cannot
	// build one. ADR-0020 takes this trade deliberately: refusing would make
	// the library unplayable on a host where every other media server works.
	// The warning is per call, not once at startup, because a warning nobody
	// sees again is a warning nobody acts on.
	permitUnsandboxed bool
	// tools are the media tools that could not be found at startup.
	tools []string
}

// NewSandbox probes the kernel's willingness to build the jail.
//
// By trying it, not by reading a sysctl. user.max_user_namespaces being
// non-zero does not mean a seccomp profile will allow the clone flags, and the
// only answer that matters is what happens when this process actually asks.
func NewSandbox(log *slog.Logger, permitUnsandboxed bool) *Sandbox {
	if log == nil {
		log = slog.Default()
	}
	s := &Sandbox{log: log, permitUnsandboxed: permitUnsandboxed}
	s.available = canSandbox()
	s.tools = findTools()

	// Said FIRST, and separately from the jail.
	//
	// The order matters because of how this was missed. The image shipped with
	// no ffmpeg in it at all, and the only line about media at boot was
	// "media parser sandbox is available" — which is true, and reads as "media
	// parsing works". The library browsed, the app looked healthy, and every
	// playback request answered 500 with nothing in the log. A guarantee about
	// a jail says nothing about whether the thing it jails exists.
	if len(s.tools) > 0 {
		log.Error("media tools are MISSING; nothing can be played",
			slog.String("missing", strings.Join(s.tools, ", ")),
			slog.String("detail", "probing, converting and subtitles all shell "+
				"out to these and will fail until they are on PATH. In the "+
				"container image they are copied in at /usr/local/bin"))
	}

	switch {
	case s.available:
		log.Info("media parser sandbox is available",
			slog.String("detail", "ffprobe and ffmpeg will run with no network, "+
				"in their own PID namespace, as an unprivileged user"))
	case permitUnsandboxed:
		log.Warn("media parser sandbox is NOT available: this kernel refused to " +
			"create a user namespace. Media parsing will run WITHOUT network " +
			"isolation, so a malicious file that exploits ffmpeg can reach the " +
			"network from this host")
	default:
		log.Error("media parser sandbox is NOT available and unsandboxed parsing " +
			"is not permitted: media will not be probed or transcoded")
	}
	return s
}

// Available reports whether the jail can be built on this host.
func (s *Sandbox) Available() bool { return s.available }

// MissingTools names the media tools that are not on PATH, if any.
func (s *Sandbox) MissingTools() []string {
	if s == nil {
		return nil
	}
	return s.tools
}

// ErrToolMissing means the media tool this operation needs is not installed.
//
// Distinct from a tool that ran and failed, because the two need different
// answers: one is a broken file, the other is a broken deployment, and telling
// an operator "internal error" for the second is how a container ships for a
// whole phase with no ffmpeg in it.
var ErrToolMissing = errors.New("playback: a required media tool is not installed")

// requireTool refuses before starting anything when the tool is not installed.
//
// Checked against the startup scan rather than by looking again: the answer
// does not change while the process runs, and this way the error an operator
// sees and the line in the boot log describe the same condition.
func (s *Sandbox) requireTool(tool string) error {
	for _, missing := range s.tools {
		if missing == tool {
			return fmt.Errorf("%w: %s is not on PATH. Nothing can be probed, "+
				"converted or subtitled until it is", ErrToolMissing, tool)
		}
	}
	return nil
}

// findTools reports which of the media tools cannot be found.
//
// LookPath rather than running them: the question is whether the file is there
// and executable, and executing an unknown binary to find out is a worse way to
// ask. Checked once at startup, not per request — a tool does not appear
// halfway through a process's life, and a PATH lookup per probe is a syscall
// nobody needs.
func findTools() []string {
	var missing []string
	for _, tool := range []string{FFprobePath, FFmpegPath} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

// canSandbox asks the kernel whether it would build the jail, by trying to
// build one around a program that does not exist.
//
// The trick is that Go performs the clone and the exec in that order, and
// reports both as a start error — so the ERRNO says which stage failed:
//
//	ENOENT  the namespaces were created and only the exec failed → available
//	EPERM / EINVAL / EACCES   the clone itself was refused       → unavailable
//
// Running a real child was tried first and rejected. The obvious candidate is
// /proc/self/exe with an argument it will reject, and that is a trap: it
// depends on this binary's flag parser continuing to treat unknown flags as
// fatal. The day somebody makes parsing lenient, a capability check would
// quietly start a second copy of the application — inside a network namespace,
// where its failures would be baffling. Nothing can be started here, so nothing
// can be started by mistake.
func canSandbox() bool {
	// A path that cannot exist, rather than one that merely does not. The
	// leading NUL-free nonsense is not special; what matters is that it is
	// absolute, so no PATH lookup happens before the fork.
	// The start fails at exec, at once; the background context is only what
	// CommandContext asks for.
	cmd := exec.CommandContext(context.Background(), "/nonexistent/cmediastack-sandbox-capability-probe")
	cmd.SysProcAttr = sandboxAttr()
	cmd.Env = []string{}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	err := cmd.Start()
	if err == nil {
		// Should not happen — but if it somehow did, the clone plainly worked.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return true
	}
	return errors.Is(err, fs.ErrNotExist)
}

// Result is what a sandboxed run produced.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	// Sandboxed reports whether the jail was actually used, so a caller records
	// the weaker guarantee rather than assuming the stronger one.
	Sandboxed bool
}

// Run executes tool with args, with file available to it as /dev/fd/3.
//
// The file is passed as a descriptor, so args refer to it as "/dev/fd/3" and
// contain no path into the library. That is not a convention the caller is
// asked to remember: a library path in args would not resolve to anything
// useful inside the child, because the child is never told where the library
// is.
func (s *Sandbox) Run(ctx context.Context, timeout time.Duration,
	file *os.File, tool string, args ...string) (Result, error) {

	if !s.available && !s.permitUnsandboxed {
		return Result{}, ErrSandboxUnavailable
	}
	if err := s.requireTool(tool); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- requireTool above admits only the ffprobe and ffmpeg this
	// sandbox resolved at startup; args are built by this package and name the
	// file as /dev/fd/3; there is no shell.
	cmd := exec.CommandContext(ctx, tool, args...)
	if file != nil {
		// ExtraFiles[0] is the child's fd 3. Below 3 is the standard trio,
		// which is why the convention is /dev/fd/3.
		cmd.ExtraFiles = []*os.File{file}
	}
	// An empty environment, not the parent's. The parent's holds the master
	// key's location, proxy credentials, and whatever else the operator
	// exported; a media parser has no use for any of it.
	cmd.Env = []string{}
	cmd.Dir = "/"

	if s.available {
		cmd.SysProcAttr = sandboxAttr()
	} else {
		s.log.Warn("running a media parser WITHOUT the sandbox",
			slog.String("tool", tool))
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, remaining: maxOutput}
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: maxOutput}

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(),
		Duration: time.Since(start), Sandboxed: s.available,
	}

	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("playback: %s did not finish within %s: %w",
			tool, timeout, ctx.Err())
	}
	if err != nil {
		return res, fmt.Errorf("playback: %s failed: %w (%s)",
			tool, err, firstLine(res.Stderr))
	}
	return res, nil
}

// limitedWriter stops accepting after n bytes and records the truncation,
// rather than allocating whatever the child felt like producing.
type limitedWriter struct {
	w         io.Writer
	remaining int
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		l.truncated = true
		// Claim the write. Returning a short count makes the child see a
		// broken pipe, which turns "output too long" into "the tool crashed".
		return len(p), nil
	}
	if len(p) > l.remaining {
		l.truncated = true
		n, err := l.w.Write(p[:l.remaining])
		l.remaining = 0
		if err != nil {
			return n, err
		}
		return len(p), nil
	}
	n, err := l.w.Write(p)
	l.remaining -= n
	return n, err
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	const max = 200
	if len(b) > max {
		b = b[:max]
	}
	return string(bytes.TrimSpace(b))
}

// copyAhead copies src to dst through up to max bytes that a second goroutine
// reads ahead of dst. After dst fails, the rest of src is drained, so the
// reader never blocks; the caller stops whatever is writing src.
func copyAhead(dst io.Writer, src io.Reader, max int) (int64, error) {
	const chunk = 64 << 10
	ch := make(chan []byte, max/chunk)
	go func() {
		defer close(ch)
		for {
			b := make([]byte, chunk)
			k, err := src.Read(b)
			if k > 0 {
				ch <- b[:k]
			}
			if err != nil {
				return
			}
		}
	}()
	var n int64
	for b := range ch {
		k, err := dst.Write(b)
		n += int64(k)
		if err != nil {
			go func() {
				for range ch {
				}
			}()
			return n, err
		}
	}
	return n, nil
}

// RemuxTimeout bounds a single conversion.
//
// Long, because it covers a whole film being watched: the process lives for as
// long as somebody is watching, not for as long as the conversion takes. The
// request context is what normally ends it; this is the backstop for a client
// that vanished without the connection closing.
const RemuxTimeout = 6 * time.Hour

// ConvertAhead is how much of a converted stream may wait in memory for the
// browser (ADR-0072). A browser reads a piped stream only a few seconds ahead
// of the picture, so without this ffmpeg idles until asked and any dip in a
// transcode's speed is a stall; with it, ffmpeg keeps working into this much
// lead. At most this per conversion, and conversions are admission-limited.
const ConvertAhead = 64 << 20

// Pipe runs a tool in the jail and copies its stdout to w as it is produced.
//
// Separate from Run because the two have opposite shapes: Run collects a small
// answer into memory, and this one produces gigabytes that must never be held
// whole: at most ConvertAhead of it waits for the reader.
func (s *Sandbox) Pipe(ctx context.Context, w io.Writer, file *os.File,
	tool string, args ...string) (int64, error) {

	if !s.available && !s.permitUnsandboxed {
		return 0, ErrSandboxUnavailable
	}

	if err := s.requireTool(tool); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, RemuxTimeout)
	defer cancel()

	// #nosec G204 -- as in Run: an allowlisted tool, arguments built here, no shell.
	cmd := exec.CommandContext(ctx, tool, args...)
	if file != nil {
		cmd.ExtraFiles = []*os.File{file}
	}
	cmd.Env = []string{}
	cmd.Dir = "/"
	if s.available {
		cmd.SysProcAttr = sandboxAttr()
	} else {
		s.log.Warn("converting a stream WITHOUT the sandbox", slog.String("tool", tool))
	}

	// Stderr is collected and capped; stdout goes straight through.
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: maxOutput}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("playback: %s stdout: %w", tool, err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("playback: starting %s: %w", tool, err)
	}

	n, copyErr := copyAhead(w, out, ConvertAhead)
	viewerGone := ctx.Err() == context.Canceled
	if copyErr != nil {
		// Nothing is reading any more: stop the tool rather than let it
		// finish the film into the drain.
		cancel()
	}
	waitErr := cmd.Wait()

	switch {
	case viewerGone:
		// The viewer went away. Not a failure.
		return n, nil
	case copyErr != nil:
		// Usually the client disconnecting mid-film, which is also not a
		// failure — but it is worth distinguishing from ffmpeg dying.
		return n, fmt.Errorf("playback: writing a converted stream: %w", copyErr)
	case waitErr != nil:
		return n, fmt.Errorf("playback: %s failed: %w (%s)",
			tool, waitErr, firstLine(stderr.Bytes()))
	}
	return n, nil
}
