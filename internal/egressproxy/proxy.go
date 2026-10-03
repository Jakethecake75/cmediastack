// Package egressproxy holds the SOCKS5 proxy an administrator sets from the
// web (ADR-0065): stored beside the other web-set credentials, checked by the
// boot's own security lint before it is kept, and laid over the config file's
// egress profiles when the process starts.
//
// Only the proxy. The tunnel, the namespace guard and the kill switch stay in
// the file (ADR-0013).
package egressproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// Setting is the proxy. Password is write-only: Load fills it, and nothing
// that is shown or logged carries it.
type Setting struct {
	Address  string   `json:"address"`
	Username string   `json:"username"`
	Password string   `json:"-"`
	Profiles []string `json:"profiles"`
}

// Offered are the profiles the proxy may carry. Notifications and updates are
// never offered: a kill switch that silences its own alarm is worse than none.
var Offered = []string{"download", "indexer", "metadata", "subtitle"}

// Where it is kept.
const (
	PlainSetting  = "egress.proxy"          // JSON: address, username, profiles
	SealedSetting = "egress.proxy.password" // base64 of the sealed password
	SealedContext = "egress:proxy"
)

// SettingStore is the setting table.
type SettingStore interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Store keeps the proxy.
type Store struct {
	settings SettingStore
	cipher   *secrets.Cipher
}

// NewStore builds the store.
func NewStore(s SettingStore, c *secrets.Cipher) *Store { return &Store{settings: s, cipher: c} }

func (s *Store) read(ctx context.Context, key string) (string, error) {
	v, err := s.settings.Setting(ctx, key)
	if errors.Is(err, identity.ErrNotFound) {
		return "", nil
	}
	return strings.TrimSpace(v), err
}

// Load reads the proxy; false when none is set.
func (s *Store) Load(ctx context.Context) (Setting, bool, error) {
	raw, err := s.read(ctx, PlainSetting)
	if err != nil || raw == "" {
		return Setting{}, false, err
	}
	var out Setting
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Setting{}, false, errors.New("egressproxy: the stored proxy is damaged; set it again")
	}
	enc, err := s.read(ctx, SealedSetting)
	if err != nil {
		return Setting{}, false, err
	}
	if enc != "" {
		sealed, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return Setting{}, false, errors.New("egressproxy: the stored proxy password is damaged; set it again")
		}
		if out.Password, err = s.cipher.DecryptString(sealed, SealedContext); err != nil {
			return Setting{}, false, errors.New("egressproxy: the stored proxy password does not open " +
				"with this master key; set it again")
		}
	}
	return out, true, nil
}

// Save stores the proxy. An empty address clears it, password included.
func (s *Store) Save(ctx context.Context, in Setting) error {
	if strings.TrimSpace(in.Address) == "" {
		if err := s.settings.SetSetting(ctx, PlainSetting, ""); err != nil {
			return err
		}
		return s.settings.SetSetting(ctx, SealedSetting, "")
	}
	plain, err := json.Marshal(in)
	if err != nil {
		return err
	}
	enc := ""
	if in.Password != "" {
		sealed, err := s.cipher.EncryptString(in.Password, SealedContext)
		if err != nil {
			return err
		}
		enc = base64.StdEncoding.EncodeToString(sealed)
	}
	if err := s.settings.SetSetting(ctx, SealedSetting, enc); err != nil {
		return err
	}
	return s.settings.SetSetting(ctx, PlainSetting, string(plain))
}

// Status is what the screen shows. Passwords are blanked.
type Status struct {
	Stored  Setting  `json:"stored"`
	InForce Setting  `json:"in_force"`
	Differs bool     `json:"differs"`
	FileSet []string `json:"file_set"`
	Problem string   `json:"problem,omitempty"`
}

// overlay lays the proxy over the file's direct profiles. It returns the
// result, the profiles it changed, and the ticked ones the file had set.
func overlay(base config.Config, s Setting) (config.Config, []string, []string) {
	out := base
	out.Egress.Profiles = make(map[string]config.EgressProfile, len(base.Egress.Profiles))
	for k, v := range base.Egress.Profiles {
		out.Egress.Profiles[k] = v
	}
	if strings.TrimSpace(s.Address) == "" {
		return out, nil, nil
	}
	var changed, fileSet []string
	for _, name := range s.Profiles {
		p, present := out.Egress.Profiles[name]
		if present && p.Mode != config.EgressDirect {
			fileSet = append(fileSet, name) // the file decided; it wins
			continue
		}
		out.Egress.Profiles[name] = config.EgressProfile{Mode: config.EgressSOCKS5, Address: s.Address,
			Username: s.Username, Password: s.Password, RemoteDNS: true}
		changed = append(changed, name)
	}
	return out, changed, fileSet
}

// Apply is the boot's overlay. When the result fails the lint, the profiles
// the proxy would have carried are blocked instead — never left direct — and
// the lint's words are the status's Problem, so the app still starts and the
// screen that fixes it stays reachable.
func Apply(base config.Config, s Setting, getenv func(string) string) (config.Config, Status) {
	out, changed, fileSet := overlay(base, s)
	st := Status{Stored: blank(s), InForce: blank(s), FileSet: fileSet}
	if err := config.Lint(out, getenv); err != nil {
		for _, name := range changed {
			out.Egress.Profiles[name] = config.EgressProfile{Mode: config.EgressBlocked}
		}
		st.Problem = err.Error()
		st.InForce = Setting{}
	}
	return out, st
}

// Validate checks a proxy before it is stored: the profiles it may carry, then
// the boot's lint on the result — which also refuses an address that is not
// host:port — so a combination the boot would refuse is refused now.
func Validate(base config.Config, s Setting, getenv func(string) string) error {
	if strings.TrimSpace(s.Address) == "" {
		return nil // clearing
	}
	if len(s.Profiles) == 0 {
		return errors.New("tick at least one profile for the proxy to carry")
	}
	for _, name := range s.Profiles {
		if !slices.Contains(Offered, name) {
			return fmt.Errorf("%q is not a profile the proxy may carry (%s)", name, strings.Join(Offered, ", "))
		}
	}
	if _, st := Apply(base, s, getenv); st.Problem != "" {
		return errors.New(st.Problem)
	}
	return nil
}

func blank(s Setting) Setting {
	s.Password = ""
	return s
}
