package release

import "testing"

// The import judges "better" by the default ladder, not by a title's profile
// (ADR-0035, ADR-0036). For an upgrade the profile chose to be imported, the
// two must agree: every built-in profile ranks the qualities it allows in the
// ladder's order.
func TestTheBuiltinProfilesRankAsTheImportDoes(t *testing.T) {
	for _, p := range DefaultProfiles() {
		if err := p.Compile(); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		for i := 1; i < len(p.Allowed); i++ {
			lo, hi := p.Allowed[i-1], p.Allowed[i]
			if DefaultRank(lo) < 0 || DefaultRank(hi) < 0 {
				t.Errorf("%s allows %q or %q, which the ladder does not know", p.Name, lo, hi)
				continue
			}
			if DefaultRank(lo) >= DefaultRank(hi) {
				t.Errorf("%s ranks %s above %s; the import's ladder ranks them the other way, "+
					"so an upgrade it fetched would not be imported", p.Name, hi, lo)
			}
		}
	}
}
