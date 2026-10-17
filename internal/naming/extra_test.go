package naming

import (
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestParseExtra(t *testing.T) {
	tests := []struct {
		path string
		want Extra
		ok   bool
	}{
		{"Big Movie (2020)/Extras/Making of.mkv", Extra{Kind: domain.ExtraOther, Title: "Making of", Owner: "Big Movie (2020)"}, true},
		{"Big Movie (2020)/Trailers/Teaser.mp4", Extra{Kind: domain.ExtraTrailer, Title: "Teaser", Owner: "Big Movie (2020)"}, true},
		{"Big Movie (2020)/Behind The Scenes/On.set.mkv", Extra{Kind: domain.ExtraBehindTheScenes, Title: "On set", Owner: "Big Movie (2020)"}, true},
		{"Big Movie (2020)/Deleted Scenes/Alternate ending.mkv", Extra{Kind: domain.ExtraDeletedScene, Title: "Alternate ending", Owner: "Big Movie (2020)"}, true},
		// The deepest folder of extras decides.
		{"Big Movie (2020)/Extras/Interviews/Director.mkv", Extra{Kind: domain.ExtraInterview, Title: "Director", Owner: "Big Movie (2020)/Extras"}, true},
		// A series and one of its seasons.
		{"Show (2019)/Featurettes/Cast.mkv", Extra{Kind: domain.ExtraFeaturette, Title: "Cast", Owner: "Show (2019)"}, true},
		{"Show (2019)/Season 01/Extras/Bloopers.mkv", Extra{Kind: domain.ExtraOther, Title: "Bloopers", Owner: "Show (2019)/Season 01"}, true},
		// Named after the movie.
		{"Big Movie (2020)/Big Movie (2020)-trailer.mkv", Extra{Kind: domain.ExtraTrailer, Title: "Big Movie (2020)-trailer", Owner: "Big Movie (2020)", OwnerName: "Big Movie (2020)"}, true},
		{"Big Movie (2020)/Big Movie (2020)-Trailer2.mp4", Extra{Kind: domain.ExtraTrailer, Title: "Big Movie (2020)-Trailer2", Owner: "Big Movie (2020)", OwnerName: "Big Movie (2020)"}, true},
		{"Big.Movie.2020-featurette.mkv", Extra{Kind: domain.ExtraFeaturette, Title: "Big Movie 2020-featurette", Owner: ".", OwnerName: "Big.Movie.2020"}, true},
		{"Big Movie (2020)/Big Movie-behindthescenes.mkv", Extra{Kind: domain.ExtraBehindTheScenes, Title: "Big Movie-behindthescenes", Owner: "Big Movie (2020)", OwnerName: "Big Movie"}, true},
		// Not extras.
		{"Big Movie (2020)/Big Movie (2020).mkv", Extra{}, false},
		{"The.Big.Short.2015.1080p.mkv", Extra{}, false},
		{"The Big Short (2015)/The Big Short (2015).mkv", Extra{}, false},
		{"Show/Season 1/Show S01E01 - Trailer Park Boys.mkv", Extra{}, false},
		{"Show/Season 1/Show.S01E05.The.Interview.mkv", Extra{}, false},
		{"Scenes From a Marriage (1973)/Scenes From a Marriage (1973).mkv", Extra{}, false},
		{"Trailer Park (2010)/Trailer Park (2010).mkv", Extra{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseExtra(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseExtra(%q) = %+v, %v; want %+v, %v", tt.path, got, ok, tt.want, tt.ok)
		}
		if IsExtra(tt.path) != tt.ok {
			t.Errorf("IsExtra(%q) != %v", tt.path, tt.ok)
		}
	}
}

func FuzzParseExtra(f *testing.F) {
	for _, s := range []string{"Movie (2020)/Trailers/x.mkv", "Movie-trailer.mkv", "a/b/c", "-trailer.mkv", "Extras/x.mkv"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		x, ok := ParseExtra(p)
		if ok && (x.Kind == "" || x.Owner == "") {
			t.Errorf("ParseExtra(%q) = %+v", p, x)
		}
	})
}
