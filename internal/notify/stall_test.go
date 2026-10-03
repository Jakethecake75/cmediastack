package notify

import (
	"testing"

	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// A download that stopped moving names a release, so it is Library news: off
// unless the operator chose titles (ADR-0034, ADR-0032).
func TestAStalledDownloadIsLibraryNews(t *testing.T) {
	cat, title, ok, _ := classify(audit.Record{Event: audit.Event{Action: audit.ActionDownloadStalled,
		Outcome: audit.OutcomeSuccess, ActorLabel: "system:download"}})
	if !ok || cat != Library || title != "A download stopped moving" {
		t.Errorf("classified as %q %q %v, want Library", cat, title, ok)
	}
}
