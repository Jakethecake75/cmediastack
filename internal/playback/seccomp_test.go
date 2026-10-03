package playback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The shipped seccomp profile, checked by evaluating it the way the kernel
// would rather than by reading it.
//
// # Why this test exists
//
// ADR-0007 and ADR-0020 both said the container's `seccomp:unconfined` should
// be tightened before the transcoder ships. Doing that turned up a direct
// conflict: Docker's DEFAULT profile blocks precisely the clone this package
// depends on.
//
//	allow clone when (arg0 & 0x7E020000) == 0    [without CAP_SYS_ADMIN]
//	deny  clone3 outright
//	defaultAction: SCMP_ACT_ERRNO
//
// 0x7E020000 is the sum of all seven CLONE_NEW* flags, so the rule permits
// clone only when NO namespace is being created. The jail asks for three.
//
// Switching the container to the default profile would therefore have SILENTLY
// disabled the sandbox — NewSandbox would find the clone refused, log its
// warning, and parse media with no network isolation at all. A generic control
// traded for a specific one, in the wrong direction, discoverable only by
// reading a log line.
//
// So the repository ships Docker's default plus one narrowly-scoped allowance,
// and this test evaluates it: the profile is only as good as the decision it
// produces for the exact flags sandboxAttr() passes.

// seccompProfile is the JSON Docker consumes. Only the fields that affect the
// decision are modelled.
type seccompProfile struct {
	DefaultAction string `json:"defaultAction"`
	Syscalls      []struct {
		Names   []string `json:"names"`
		Action  string   `json:"action"`
		Comment string   `json:"comment"`
		Args    []struct {
			Index    int    `json:"index"`
			Value    uint64 `json:"value"`
			ValueTwo uint64 `json:"valueTwo"`
			Op       string `json:"op"`
		} `json:"args"`
		Excludes struct {
			Caps   []string `json:"caps"`
			Arches []string `json:"arches"`
		} `json:"excludes"`
		Includes struct {
			Caps   []string `json:"caps"`
			Arches []string `json:"arches"`
		} `json:"includes"`
	} `json:"syscalls"`
}

func loadProfile(t *testing.T) seccompProfile {
	t.Helper()
	// Up two from internal/playback to the module root.
	path := filepath.Join("..", "..", "deploy", "seccomp-cmediastack.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the shipped profile: %v", err)
	}
	var p seccompProfile
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("the shipped profile is not valid JSON, so Docker would refuse "+
			"to start the container: %v", err)
	}
	return p
}

// decide evaluates the profile for one syscall and one first argument, for a
// container holding NO capabilities — which is what docker-compose.yml
// configures (`cap_drop: ALL`).
//
// Rules are evaluated in order and the first match wins, which is how libseccomp
// composes them.
func decide(p seccompProfile, name string, arg0 uint64) string {
	for _, s := range p.Syscalls {
		// This container has no capabilities, so a rule that INCLUDES a
		// capability never applies to it...
		if len(s.Includes.Caps) > 0 {
			continue
		}
		// ...and one that EXCLUDES a capability always does.
		if len(s.Includes.Arches) > 0 {
			// Architecture-specific rules (s390) are not this deployment.
			continue
		}
		matchedName := false
		for _, n := range s.Names {
			if n == name {
				matchedName = true
				break
			}
		}
		if !matchedName {
			continue
		}
		if len(s.Args) == 0 {
			return s.Action
		}
		ok := true
		for _, a := range s.Args {
			if a.Index != 0 {
				ok = false
				break
			}
			switch a.Op {
			case "SCMP_CMP_MASKED_EQ":
				if arg0&a.Value != a.ValueTwo {
					ok = false
				}
			case "SCMP_CMP_EQ":
				if arg0 != a.Value {
					ok = false
				}
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			return s.Action
		}
	}
	return p.DefaultAction
}

// sandboxFlags is what sandboxAttr() actually passes, read from the constant
// rather than retyped — so a change to the jail breaks this test rather than
// quietly outgrowing the profile.
const sandboxFlags = uint64(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID)

// The profile must permit the jail. Without this the container starts, media
// parsing silently loses its network isolation, and the only evidence is a log
// line nobody reads twice.
func TestTheShippedSeccompProfilePermitsTheMediaSandbox(t *testing.T) {
	p := loadProfile(t)

	if got := decide(p, "clone", sandboxFlags); got != "SCMP_ACT_ALLOW" {
		t.Fatalf("clone(0x%X) -> %s, want SCMP_ACT_ALLOW. The media parser jail "+
			"cannot be created under this profile, so parsing would fall back "+
			"to no isolation", sandboxFlags, got)
	}
}

// And it must be a TIGHTENING, not `unconfined` wearing a filename.
func TestTheShippedSeccompProfileStillDeniesByDefault(t *testing.T) {
	p := loadProfile(t)

	if p.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Errorf("defaultAction = %q; a profile that allows by default is "+
			"unconfined with extra steps", p.DefaultAction)
	}

	// Syscalls Docker's default denies, which this profile must go on denying.
	// If these started passing, the profile would have been replaced by
	// something permissive rather than extended.
	//
	// The list was checked against the shipped file rather than written from
	// memory, and two entries came out of it: ptrace and process_vm_readv are
	// ALLOWED by Docker's default. That surprised the first draft of this test,
	// and the profile was right — Docker unblocked ptrace once the kernel
	// hardened it against the setuid-binary attacks that made it dangerous.
	// Asserting they were denied would have been a test that failed for being
	// wrong about the thing it was checking.
	for _, name := range []string{
		"kexec_load",        // load a new kernel
		"init_module",       // load a kernel module
		"finit_module",      // the same, by descriptor
		"bpf",               // attach BPF programs
		"mount",             // not the container's business
		"umount2",           // nor is this
		"pivot_root",        // nor this
		"setns",             // join somebody else's namespace
		"unshare",           // the other route to a namespace; clone is the one allowed
		"clone3",            // denied by the default, so clone is the only path
		"perf_event_open",   // a long history of kernel bugs
		"open_by_handle_at", // escape a bind mount by file handle
		"keyctl",            // kernel keyring
		"reboot",
		"swapon",
	} {
		if got := decide(p, name, 0); got == "SCMP_ACT_ALLOW" {
			t.Errorf("%s is allowed; the profile is not a tightening of Docker's "+
				"default", name)
		}
	}
}

// The allowance is narrow: exactly the three namespaces the jail creates, and
// no more. A profile permitting every CLONE_NEW* flag would hand a compromised
// process a mount namespace and a UTS namespace it has no use for.
func TestTheSeccompAllowanceIsNarrowerThanEveryNamespace(t *testing.T) {
	p := loadProfile(t)

	const (
		newns     = uint64(syscall.CLONE_NEWNS)
		newuts    = uint64(syscall.CLONE_NEWUTS)
		newipc    = uint64(syscall.CLONE_NEWIPC)
		newcgroup = uint64(0x02000000) // CLONE_NEWCGROUP
	)

	for _, tc := range []struct {
		name  string
		flags uint64
	}{
		{"a mount namespace as well", sandboxFlags | newns},
		{"a UTS namespace as well", sandboxFlags | newuts},
		{"an IPC namespace as well", sandboxFlags | newipc},
		{"a cgroup namespace as well", sandboxFlags | newcgroup},
		{"a mount namespace on its own", newns},
		{"every namespace at once", sandboxFlags | newns | newuts | newipc | newcgroup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(p, "clone", tc.flags); got == "SCMP_ACT_ALLOW" {
				t.Errorf("clone(0x%X) is allowed. The allowance should cover the "+
					"jail's three namespaces and nothing else", tc.flags)
			}
		})
	}

	// And an ordinary clone — every thread the Go runtime starts — still works.
	if got := decide(p, "clone", 0); got != "SCMP_ACT_ALLOW" {
		t.Errorf("an ordinary clone(0) -> %s; the process could not start a "+
			"goroutine, let alone serve a request", got)
	}
}

// The profile has to explain itself where an operator will read it: in the file.
func TestTheSeccompAllowanceSaysWhyItIsThere(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "seccomp-cmediastack.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"CMediaStack", "CLONE_NEWUSER", "0020-playback"} {
		if !strings.Contains(body, want) {
			t.Errorf("the profile does not mention %q; an operator diffing it "+
				"against Docker's default deserves to find out why it differs "+
				"from the file rather than from a commit message", want)
		}
	}
}

// The compose file must actually use it. A profile in the repository that
// nothing loads is documentation pretending to be a control.
func TestComposeLoadsTheShippedSeccompProfile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)

	if strings.Contains(body, "seccomp:unconfined") {
		t.Error("docker-compose.yml still sets seccomp:unconfined")
	}
	if !strings.Contains(body, "deploy/seccomp-cmediastack.json") {
		t.Error("docker-compose.yml does not load the shipped profile")
	}
}
