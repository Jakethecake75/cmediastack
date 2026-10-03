package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietSandbox(t *testing.T) *Sandbox {
	t.Helper()
	s := NewSandbox(slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	if !s.Available() {
		t.Skip("this kernel will not create a user namespace, so there is no " +
			"sandbox to test; see ADR-0020's known limitations")
	}
	return s
}

// The claim ADR-0020 is built on: a parser that is successfully exploited
// cannot call out, cannot reach the LAN, and cannot reach the NFS server the
// media is mounted from.
//
// Asserted against a REAL dial rather than against the clone flags, because the
// flags are what was asked for and this is what was granted.
func TestTheProbeSandboxCannotDialOut(t *testing.T) {
	s := quietSandbox(t)

	// A tiny program is the dialer, so this does not depend on curl or ip
	// existing in the image.
	dir := t.TempDir()
	src := filepath.Join(dir, "dial.go")
	const prog = `package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	c, err := net.DialTimeout("tcp", "1.1.1.1:443", 5*time.Second)
	if err != nil {
		fmt.Println("DIAL-FAILED:", err)
		return
	}
	_ = c.Close()
	fmt.Println("DIAL-OK")
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "dial")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("could not build the dialer (%v): %s", err, out)
	}

	// Establish that the dial WOULD succeed from here. Without this the test
	// passes on a host with no network at all, proving nothing.
	unjailed, err := exec.CommandContext(context.Background(), bin).CombinedOutput()
	if err != nil {
		t.Fatalf("running the dialer: %v", err)
	}
	if !strings.Contains(string(unjailed), "DIAL-OK") {
		t.Skipf("this host has no outbound network (%s), so confinement "+
			"cannot be distinguished from absence", strings.TrimSpace(string(unjailed)))
	}

	res, err := s.Run(context.Background(), 30*time.Second, nil, bin)
	if err != nil {
		t.Fatalf("running the dialer in the sandbox: %v (%s)", err, res.Stderr)
	}
	got := string(res.Stdout)
	t.Logf("unsandboxed: %s", strings.TrimSpace(string(unjailed)))
	t.Logf("sandboxed:   %s", strings.TrimSpace(got))

	if !strings.Contains(got, "DIAL-FAILED") {
		t.Errorf("a sandboxed process reached the network: %q — the network "+
			"namespace is not confining anything", got)
	}
}

// The same property from the other side: the interfaces a process can see ARE
// its network namespace.
func TestTheProbeSandboxHasNoNetworkInterfaces(t *testing.T) {
	s := quietSandbox(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "ifs.go")
	const prog = `package main

import (
	"fmt"
	"net"
)

func main() {
	ifs, err := net.Interfaces()
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	for _, i := range ifs {
		fmt.Println(i.Name)
	}
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "ifs")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("could not build the lister (%v): %s", err, out)
	}

	host, err := exec.CommandContext(context.Background(), bin).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	hostIfs := lines(string(host))
	if len(hostIfs) < 2 {
		t.Skipf("this host sees only %v, so there is nothing to confine", hostIfs)
	}

	res, err := s.Run(context.Background(), 30*time.Second, nil, bin)
	if err != nil {
		t.Fatalf("listing interfaces in the sandbox: %v (%s)", err, res.Stderr)
	}
	jailIfs := lines(string(res.Stdout))
	t.Logf("host: %v", hostIfs)
	t.Logf("jail: %v", jailIfs)

	for _, name := range jailIfs {
		if name != "lo" {
			t.Errorf("a sandboxed process can see %q; the only interface in an "+
				"empty network namespace is a down loopback", name)
		}
	}
}

// It is also not the user the application runs as. A child that somehow leaves
// its namespace is still not the process that holds the master key.
func TestTheSandboxDropsToAnUnprivilegedUser(t *testing.T) {
	s := quietSandbox(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "whoami.go")
	if err := os.WriteFile(src, []byte(
		"package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n"+
			"func main() { fmt.Println(os.Getuid()) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "whoami")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("could not build (%v): %s", err, out)
	}

	res, err := s.Run(context.Background(), 30*time.Second, nil, bin)
	if err != nil {
		t.Fatalf("%v (%s)", err, res.Stderr)
	}
	got := strings.TrimSpace(string(res.Stdout))
	if got != "65534" {
		t.Errorf("sandboxed uid = %q, want 65534", got)
	}
}

// A child that will not finish must not hold a worker forever.
func TestASandboxedChildIsKilledWhenItOverrunsItsTimeout(t *testing.T) {
	s := quietSandbox(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "spin.go")
	if err := os.WriteFile(src, []byte(
		"package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "spin")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("could not build (%v): %s", err, out)
	}

	start := time.Now()
	_, err := s.Run(context.Background(), 2*time.Second, nil, bin)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a child that sleeps for an hour returned without error")
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %v, want one naming the timeout", err)
	}
	if elapsed > 20*time.Second {
		t.Errorf("took %s to give up on a 2s timeout", elapsed)
	}
}

// A file crafted to produce enormous output must not be able to exhaust the
// parent's memory — a cheaper attack than exploiting the parser.
func TestOutputFromAChildIsCapped(t *testing.T) {
	s := quietSandbox(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "flood.go")
	const prog = `package main

import (
	"os"
	"strings"
)

func main() {
	chunk := strings.Repeat("A", 1<<16)
	for i := 0; i < 200; i++ {
		if _, err := os.Stdout.WriteString(chunk); err != nil {
			return
		}
	}
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "flood")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("could not build (%v): %s", err, out)
	}

	// 200 × 64 KiB = 12.5 MiB offered against a 1 MiB cap.
	res, err := s.Run(context.Background(), 30*time.Second, nil, bin)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(res.Stdout) > maxOutput {
		t.Errorf("kept %d bytes from a child, cap is %d", len(res.Stdout), maxOutput)
	}
	if len(res.Stdout) == 0 {
		t.Error("kept nothing at all; the cap should truncate, not discard")
	}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// A missing media tool is a broken DEPLOYMENT, and has to be said as one.
//
// This is the test that would have made the container's missing ffmpeg visible
// without building the image. Every other test in this package runs on a host
// where ffmpeg happens to be installed, which is exactly why none of them
// noticed that the shipped image had none.
func TestAMissingMediaToolIsNamedRatherThanBecomingAnInternalError(t *testing.T) {
	// A PATH with nothing on it. LookPath reads the environment on each call,
	// so this is enough to make the tools genuinely unfindable.
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))

	var logged bytes.Buffer
	s := NewSandbox(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})), true)

	missing := s.MissingTools()
	if len(missing) != 2 {
		t.Fatalf("MissingTools = %v, want both ffprobe and ffmpeg", missing)
	}

	// Said at ERROR, and said about the TOOLS rather than about the jail. The
	// bug was that the only media line at boot was "sandbox is available",
	// which is true and reads as "media parsing works".
	out := logged.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("a deployment that cannot play anything logged no error:\n%s", out)
	}
	if !strings.Contains(out, "ffprobe") || !strings.Contains(out, "ffmpeg") {
		t.Errorf("the log does not name what is missing:\n%s", out)
	}

	// And a caller gets a distinguishable error rather than whatever exec says.
	f, err := os.CreateTemp(t.TempDir(), "media")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	_, runErr := s.Run(context.Background(), time.Second, f, FFprobePath, "-version")
	if !errors.Is(runErr, ErrToolMissing) {
		t.Errorf("Run = %v, want ErrToolMissing so the API can answer 503 with "+
			"the reason instead of a bare 500", runErr)
	}
	if !strings.Contains(runErr.Error(), FFprobePath) {
		t.Errorf("the error does not name the tool: %v", runErr)
	}

	_, pipeErr := s.Pipe(context.Background(), io.Discard, f, FFmpegPath, "-version")
	if !errors.Is(pipeErr, ErrToolMissing) {
		t.Errorf("Pipe = %v, want ErrToolMissing", pipeErr)
	}
}

// On a host that HAS them, nothing is reported missing and nothing is refused.
// Without this the test above passes just as well against a Sandbox that always
// claims everything is missing.
func TestAHostWithTheToolsReportsNoneMissing(t *testing.T) {
	haveFFmpeg(t)
	s := quietSandbox(t)
	if got := s.MissingTools(); len(got) != 0 {
		t.Errorf("MissingTools = %v on a host that has them", got)
	}
}
