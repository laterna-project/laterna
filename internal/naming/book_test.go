package naming

import "testing"

func TestParseBook(t *testing.T) {
	tests := []struct {
		rel  string
		want Book
	}{
		// Names seen in the wild.
		{"One Piece [Team Chromatique] - Tome #105 - [V1].cbz", Book{Series: "One Piece", Number: 105, HasNumber: true}},
		{"One.Piece.T104.Oda.FR.[CBZ]-TONER-Paprika+.cbz", Book{Series: "One Piece", Number: 104, HasNumber: true}},
		{"Lakestone T1 - Sarah Rivens.epub", Book{Series: "Lakestone", Number: 1, HasNumber: true}},
		{"Croquemitaines.Livre.1.Salvia.Djet.2017.FR.[PDF]-NOTAG.pdf", Book{Series: "Croquemitaines", Number: 1, HasNumber: true, Year: 2017}},
		// Common layouts.
		{"Les Misérables, Tome 2.epub", Book{Series: "Les Misérables", Number: 2, HasNumber: true}},
		{"Astérix/Astérix - 01 - Astérix le Gaulois.cbz", Book{Series: "Astérix", Number: 1, HasNumber: true}},
		{"Naruto/001.cbz", Book{Series: "Naruto", Number: 1, HasNumber: true}},
		{"Naruto/Naruto 12.cbz", Book{Series: "Naruto", Number: 12, HasNumber: true}},
		{"Berserk/Vol. 3 - The Guardians of Desire.cbz", Book{Series: "Berserk", Number: 3, HasNumber: true}},
		{"Saga/Saga v05 (2015) (Digital).cbz", Book{Series: "Saga", Number: 5, HasNumber: true, Year: 2015}},
		{"Spirou.T12.5.Hors-serie.cbz", Book{Series: "Spirou", Number: 12.5, HasNumber: true}},
		{"Batman #42.cbz", Book{Series: "Batman", Number: 42, HasNumber: true}},
		// Standalone.
		{"Sarah Rivens/Captive.epub", Book{Title: "Captive"}},
		{"Fahrenheit 451.epub", Book{Title: "Fahrenheit 451"}},
		{"1984.epub", Book{Title: "1984"}},
		{"Spider-Man.pdf", Book{Title: "Spider-Man"}},
		{"Le Petit Prince (1943).epub", Book{Title: "Le Petit Prince", Year: 1943}},
	}
	for _, tt := range tests {
		if got := ParseBook(tt.rel); got != tt.want {
			t.Errorf("ParseBook(%q) = %+v, want %+v", tt.rel, got, tt.want)
		}
	}
	if !IsBook("a/b.EPUB") || !IsBook("x.cbz") || IsBook("x.cbr") || IsBook("x.jpg") {
		t.Error("IsBook")
	}
}

func FuzzParseBook(f *testing.F) {
	for _, s := range []string{"One.Piece.T104.Oda.FR.[CBZ]-TONER.cbz", "a/(((.pdf", "#.epub", "T.cbz", "- 1 -.pdf"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		b := ParseBook(rel)
		if b.HasNumber && b.Series == "" {
			t.Errorf("volume without a series: %q -> %+v", rel, b)
		}
	})
}
