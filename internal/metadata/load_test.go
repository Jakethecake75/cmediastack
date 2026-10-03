package metadata

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/identity"
)

type settingsAnswer struct {
	value string
	err   error
}

func (s settingsAnswer) Setting(context.Context, string) (string, error) { return s.value, s.err }
func (s settingsAnswer) SetSetting(context.Context, string, string) error {
	return errors.New("not in this test")
}

// No key set is a state the instance runs in; a key that could not be READ is
// a failure the caller must hear about, not a silent "no provider".
func TestLoadTellsAnUnsetKeyFromAnUnreadableOne(t *testing.T) {
	for name, c := range map[string]struct {
		answer  settingsAnswer
		wantErr bool
	}{
		"never set":  {settingsAnswer{err: identity.ErrNotFound}, false},
		"cleared":    {settingsAnswer{value: "  "}, false},
		"unreadable": {settingsAnswer{err: errors.New("database is locked")}, true},
	} {
		svc := NewService(c.answer, nil, nil, func() *http.Client { return http.DefaultClient }, time.Now)
		err := svc.Load(t.Context())
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Load = %v, want an error: %v", name, err, c.wantErr)
		}
	}
}
