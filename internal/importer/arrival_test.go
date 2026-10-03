package importer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

type recordingAuditor struct {
	mu     sync.Mutex
	events []audit.Event
	err    error
}

func (a *recordingAuditor) Write(_ context.Context, e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return a.err
}

// A file arriving in the library is written to the audit log — what it is, the
// quality, whether it replaced one — by whoever imported it; a skip is not an
// arrival (ADR-0032).
func TestAnArrivalIsAudited(t *testing.T) {
	r := newRig(t)
	rec := &recordingAuditor{}
	r.imp.SetAuditor(rec)
	sys := authz.SystemPrincipal(context.Background(), authz.TaskImport)

	first := r.download(hash(0), "Film.2019.720p.WEB-DL.x264-GRP", map[string]int64{
		"Film.2019.720p.WEB-DL.x264-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(sys, first); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("import: %v %+v", err, res)
	}
	better := r.download(hash(1), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 30 * mib,
	})
	if res, err := r.imp.Import(r.ctx, better); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("upgrade: %v %+v", err, res)
	}
	worse := r.download(hash(2), "Film.2019.SDTV.x264-GRP", map[string]int64{
		"Film.2019.SDTV.x264-GRP.mkv": 10 * mib,
	})
	if res, err := r.imp.Import(r.ctx, worse); err != nil || res.Outcome != OutcomeSkipped {
		t.Fatalf("downgrade: %v %+v", err, res)
	}
	ep := r.download(hash(3), "Show.S02E03E04.1080p.WEB-DL.x264-GRP", map[string]int64{
		"Show.S02E03E04.1080p.WEB-DL.x264-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(sys, ep); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("episode: %v %+v", err, res)
	}

	if len(rec.events) != 3 {
		t.Fatalf("%d lines for two arrivals, one upgrade and one skip: %+v", len(rec.events), rec.events)
	}
	a, b, c := rec.events[0], rec.events[1], rec.events[2]
	if a.Action != audit.ActionMediaImported || a.ActorUserID != nil || a.ActorLabel != "system:import" ||
		a.TargetKind != "media_item" || a.Detail != "Film (2019), WEBDL-720p" {
		t.Errorf("the first arrival reads %+v", a)
	}
	if b.ActorUserID == nil || *b.ActorUserID != 1 || b.ActorLabel != "jacob" ||
		b.Detail != "Film (2019), Bluray-1080p, replacing the file it had" || b.TargetID != a.TargetID {
		t.Errorf("the upgrade reads %+v", b)
	}
	if !strings.HasPrefix(c.Detail, "Show S02E03–E04") {
		t.Errorf("the double episode reads %q", c.Detail)
	}
	after, _ := b.After.(map[string]any)
	if after["release"] != "Film.2019.1080p.BluRay.x264-GRP" || after["info_hash"] != hash(1) ||
		!strings.Contains(after["path"].(string), "Film (2019)") {
		t.Errorf("the upgrade's after: %v", b.After)
	}

	// A log that cannot be written does not fail the import.
	rec.err = errors.New("disk full")
	again := r.download(hash(4), "Other.Film.2020.1080p.WEB-DL.x264-GRP", map[string]int64{
		"Other.Film.2020.1080p.WEB-DL.x264-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(sys, again); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("an unwritable log failed the import: %v %+v", err, res)
	}
}
