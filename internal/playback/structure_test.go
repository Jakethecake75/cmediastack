package playback

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The structural half of ADR-0020, in the same style as
// egress.TestNoPackageDialsDirectly and library.TestNothingWritesOutsideAVault:
// a rule that cannot be checked at runtime, checked by reading the source and
// failing the build.
//
// The rule is that a media parser is handed a DESCRIPTOR and never a path. It
// holds today because Prober.Probe takes an *os.File. It will stop holding the
// first time somebody writes a transcoder, because "just pass the filename" is
// the obvious way to write one and nothing about it looks wrong. By then the
// reasoning will be a paragraph in an ADR nobody is reading.

// sandboxCall matches a call that starts a media tool, and captures its
// argument list.
//
// BOTH ways in. This matched only `.Run(` until the remuxer arrived and reached
// ffmpeg through `.Pipe(` — at which point the rule this file exists to enforce
// silently stopped applying to the one caller that hands a tool the most
// arguments. A structural test that covers some of the entry points is a
// structural test that will be right until somebody adds another.
var sandboxCall = regexp.MustCompile(`(?s)\.(?:Run|Pipe)\(\s*(.*?)\n\t\)`)

// quotedPath matches a string literal that looks like a filesystem path.
var quotedPath = regexp.MustCompile(`"(/[^"]*)"`)

// pathFormat matches a format string that builds something path-shaped.
//
// Refined from "any fmt.Sprintf", which was too blunt: the remuxer legitimately
// builds `-map 0:%d` from a stream index, and that is not a path by any reading.
// A format containing a slash is the thing worth flagging, because that is what
// assembling a path looks like.
var pathFormat = regexp.MustCompile(`fmt\.Sprintf\(\s*"[^"]*/[^"]*"`)

// joinCall matches path assembly, whichever package it comes from.
var joinCall = regexp.MustCompile(`\b(?:file)?path\.Join\(`)

func sourceFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

// A media tool may be told about exactly one file, and only as /dev/fd/N.
//
// Passing a path would put path handling back into a shared responsibility
// between a Go program that is careful and a C program that is not — every
// symlink, every "..", and every race between checking a path and opening it
// returns to scope. Passing a descriptor deletes the question: the child cannot
// open a file it was never told the location of.
func TestNoMediaToolIsEverGivenAPath(t *testing.T) {
	const allowed = "/dev/fd/3"

	// Paths that are deliberately in this package and are not media inputs.
	// Short, and each needs a reason, because an allowlist is where a real
	// violation goes to hide.
	permitted := map[string]string{
		allowed: "the descriptor the parent hands over",
		"/":     `cmd.Dir — the child gets no working directory of its own`,
		"/nonexistent/cmediastack-sandbox-capability-probe": "a program that " +
			"cannot exist, used to ask the kernel whether it would build the jail",
	}

	for name, src := range sourceFiles(t) {
		// Only files that actually start a media tool.
		if !strings.Contains(src, "FFmpegPath") && !strings.Contains(src, "FFprobePath") &&
			!sandboxCall.MatchString(src) {
			continue
		}

		// The WHOLE FILE, not the call site.
		//
		// This checked only the arguments written inside the call, and that was
		// worth almost nothing: the remuxer builds `args := []string{...}`
		// above and passes `args...` on one line, so the regex saw an empty
		// argument list and passed. A hardcoded library path in those args went
		// undetected — verified by putting one there.
		//
		// The failure was this test's, and it was the exact shape it exists to
		// prevent: a check that reads as a guarantee and inspects nothing.
		for _, lit := range quotedPath.FindAllStringSubmatch(src, -1) {
			if _, ok := permitted[lit[1]]; ok {
				continue
			}
			t.Errorf("%s contains the path %q. A media tool is handed a "+
				"descriptor, never a path (ADR-0020) — open the file through "+
				"the caller's os.Root and pass it as %s", name, lit[1], allowed)
		}

		// A path assembled at runtime is the more likely mistake, because that
		// is how a path arrives.
		if pathFormat.MatchString(src) {
			t.Errorf("%s formats a path-shaped string in a file that starts a "+
				"media tool. Whatever it produces, it is not %s", name, allowed)
		}
		// "filepath.Join" contains "path.Join", so a naive Contains for both
		// reports the same line twice. One regex, alternating on the qualifier.
		if joinCall.MatchString(src) {
			t.Errorf("%s builds paths with a Join in a file that starts a media "+
				"tool. If one reaches the argument list, ADR-0020's containment "+
				"stops at that line", name)
		}
	}
}

// inputFlag captures whatever follows ffmpeg's -i.
var inputFlag = regexp.MustCompile(`"-i",\s*([^,\n]+)`)

// Whatever a media tool is told to open, it is the descriptor.
//
// The checks above look for path-SHAPED things: a literal, a Join, a Sprintf
// with a slash in it. This one checks the place a path would have to arrive at
// to do any harm, which is narrower and therefore worth having separately: an
// input that is a bare variable passes every other check in this file and is
// exactly the mistake mistake #18 was.
//
// The rule is mechanical. If the token after "-i" is not the literal
// /dev/fd/3, something other than the parent's descriptor is being opened.
func TestTheOnlyInputAMediaToolGetsIsTheDescriptor(t *testing.T) {
	const allowed = `"/dev/fd/3"`

	for name, src := range sourceFiles(t) {
		if !strings.Contains(src, "FFmpegPath") && !strings.Contains(src, "FFprobePath") {
			continue
		}
		found := false
		for _, m := range inputFlag.FindAllStringSubmatch(src, -1) {
			found = true
			if got := strings.TrimSpace(m[1]); got != allowed {
				t.Errorf("%s passes %s as a media tool's input. The only thing "+
					"a parser opens is the descriptor the parent handed it "+
					"(ADR-0020); anything else is a name it can follow", name, got)
			}
		}
		// A file that starts ffmpeg and never says -i is reading stdin or
		// nothing, and either way this test proved nothing about it. Said, so
		// that a silent zero is not mistaken for a pass.
		if !found && strings.Contains(src, "FFmpegPath") {
			t.Logf("%s starts ffmpeg with no -i; nothing to check here", name)
		}
	}
}

// The descriptor reaches the child exactly one way, and it is the way that
// makes it fd 3.
//
// A second entry in ExtraFiles would silently renumber nothing — fd 3 stays fd
// 3 — but it would hand the parser a descriptor nobody accounted for, which is
// the same class of mistake as an extra dialer: invisible, and only wrong later.
func TestOnlyOneDescriptorIsHandedToAParser(t *testing.T) {
	for name, src := range sourceFiles(t) {
		for _, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, "ExtraFiles") || strings.HasPrefix(
				strings.TrimSpace(line), "//") {
				continue
			}
			if !strings.Contains(line, "[]*os.File{file}") {
				t.Errorf("%s sets ExtraFiles to something other than the single "+
					"file being probed:\n\t%s", name, strings.TrimSpace(line))
			}
		}
	}
}

// The jail's flags are the guarantee. A future edit that drops one — most
// plausibly CLONE_NEWNET, because removing it makes nothing fail visibly —
// should not be a quiet change.
//
// The behavioural tests (TestTheProbeSandboxCannotDialOut) are the real proof
// and they SKIP on a kernel that will not build the jail. On such a host this
// is the only check left, which is exactly when it matters.
func TestTheSandboxStillAsksForEveryNamespaceItClaims(t *testing.T) {
	src, ok := sourceFiles(t)["sandbox.go"]
	if !ok {
		t.Fatal("sandbox.go is gone")
	}
	for _, flag := range []string{
		"syscall.CLONE_NEWUSER", // without it an unprivileged process gets none of the others
		"syscall.CLONE_NEWNET",  // the one that stops an exploited parser calling out
		"syscall.CLONE_NEWPID",
	} {
		if !strings.Contains(src, flag) {
			t.Errorf("the sandbox no longer requests %s — ADR-0020's confinement "+
				"claim is narrower than it says", flag)
		}
	}
	if !strings.Contains(src, "GidMappingsEnableSetgroups: false") {
		t.Error("setgroups is no longer denied in the child")
	}
}

// The parser gets an empty environment. The parent's holds the master key's
// location, proxy credentials, and whatever else the operator exported.
func TestAParserInheritsNoEnvironment(t *testing.T) {
	src := sourceFiles(t)["sandbox.go"]
	if !strings.Contains(src, "cmd.Env = []string{}") {
		t.Error("sandbox.go no longer clears the child's environment; a media " +
			"parser has no use for the parent's and every reason not to have it")
	}
}

// The image must contain the programs this package shells out to.
//
// # Why this test exists
//
// Because the shipped container did not contain them, and nothing noticed.
//
// The runtime base is distroless/static — no shell, no package manager, and no
// ffmpeg. The image built, started, browsed a library and answered every
// playback request with a bare 500. The only line about media at boot said
// "media parser sandbox is available", which is true and reads as "media
// parsing works": the jail was fine, the thing it jails was absent.
//
// Every test in this package passed throughout, because they all run on a host
// where ffmpeg happens to be installed. That is the shape of gap a structural
// test is for: the code is right, the deployment is not, and the difference is
// invisible to `go test`.
func TestTheContainerImageProvidesTheMediaTools(t *testing.T) {
	root := repoRootDir(t)
	body, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// Only the runtime stage. The build stage has a whole Debian in it, so
	// searching the file as a whole would pass on the wrong evidence.
	i := strings.LastIndex(src, "\nFROM ")
	if i < 0 {
		t.Fatal("the Dockerfile has no FROM; this test is reading the wrong file")
	}
	runtime := src[i:]

	for _, tool := range []string{FFmpegPath, FFprobePath} {
		if !strings.Contains(runtime, "/"+tool) {
			t.Errorf("the runtime image never places %s. Probing, converting "+
				"and subtitles all shell out to it, and a distroless base has "+
				"no package manager to install it with — so every playback "+
				"request in the shipped container fails", tool)
		}
	}

	// Copied from a pinned stage rather than installed. `apt-get install
	// ffmpeg` would work and would discard the reason the base is distroless.
	if strings.Contains(runtime, "apt-get") || strings.Contains(runtime, "apk add") {
		t.Error("the runtime stage installs packages. The distroless base is " +
			"there so that code execution inside the container has nothing to " +
			"build on; a package manager in the final image gives it one")
	}
}

// Every base image is pinned by digest.
//
// The Dockerfile has always CLAIMED this — "pinned base images by digest so a
// rebuild is reproducible" was in its header comment while all three bases were
// floating tags. A comment is not a build step.
func TestEveryBaseImageIsPinnedByDigest(t *testing.T) {
	root := repoRootDir(t)
	body, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "FROM ") {
			continue
		}
		found++
		if !strings.Contains(line, "@sha256:") {
			t.Errorf("unpinned base image, so a rebuild can silently get "+
				"different bytes:\n\t%s", line)
		}
	}
	if found == 0 {
		t.Fatal("no FROM lines found; this test is reading the wrong file")
	}
}

// repoRootDir walks up to the module root.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
