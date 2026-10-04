package api

import (
	"net/url"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/playback"
)

// A converted stream's start and height come from the query (ADR-0071):
// whole seconds and one of the allowlisted heights, or a refusal.
func TestConvertOptionsAreParsedOrRefused(t *testing.T) {
	got, problem := convertOptions(url.Values{"start": {"4271"}, "height": {"720"}})
	if problem != "" || got != (playback.StreamOptions{Start: 4271 * time.Second, Height: 720}) {
		t.Errorf("got %+v, %q", got, problem)
	}
	if got, _ := convertOptions(url.Values{"hevc": {"1"}}); !got.HEVC {
		t.Errorf("hevc=1 was not read (ADR-0072)")
	}
	if got, problem := convertOptions(url.Values{}); problem != "" || got != (playback.StreamOptions{}) {
		t.Errorf("nothing asked: %+v, %q", got, problem)
	}
	for _, q := range []url.Values{
		{"start": {"-5"}}, {"start": {"1.5"}}, {"start": {"soon"}},
		{"height": {"2160"}}, {"height": {"720;x"}},
	} {
		if _, problem := convertOptions(q); problem == "" {
			t.Errorf("%v was accepted", q)
		}
	}
}
