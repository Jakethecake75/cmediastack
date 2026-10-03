package release

import "testing"

var benchNames = []string{
	"The.Matrix.1999.1080p.BluRay.x264-SWTYBLZ",
	"Breaking.Bad.S05E14.Ozymandias.1080p.BluRay.x264-DEMAND",
	"Oppenheimer.2023.REMUX.2160p.BluRay.HDR.DV.TrueHD.7.1.Atmos-FraMeSToR",
	"Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-TERMiNAL",
	"The.Daily.Show.2024.03.14.1080p.WEB.h264-GROUP",
}

func BenchmarkParse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Parse(benchNames[i%len(benchNames)])
	}
}

func BenchmarkAcceptAndRank(b *testing.B) {
	p := DefaultProfiles()[0]
	if err := p.Compile(); err != nil {
		b.Fatal(err)
	}
	parsed := make([]Parsed, len(benchNames))
	for i, n := range benchNames {
		parsed[i] = Parse(n)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := parsed[i%len(parsed)]
		p.Accepts(r)
		p.Rank(r)
		p.Score(r)
	}
}
