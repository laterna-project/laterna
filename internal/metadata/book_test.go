package metadata

import (
	"slices"
	"strings"
	"testing"
)

func TestParseOPF(t *testing.T) {
	opf, err := ParseOPF(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:opf="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Le Voyage, tome 2</dc:title>
    <dc:creator opf:role="aut">Autrice Test</dc:creator>
    <dc:creator opf:role="ill">Illustrateur Test</dc:creator>
    <dc:contributor opf:role="bkp">calibre (6.9.0)</dc:contributor>
    <dc:description>&lt;p&gt;Un roman de &lt;b&gt;test&lt;/b&gt;.&lt;/p&gt;&lt;p&gt;Deuxième&amp;nbsp;paragraphe.&lt;/p&gt;</dc:description>
    <dc:publisher>Éditions Test</dc:publisher>
    <dc:date>2023-11-30T23:00:00+00:00</dc:date>
    <dc:language>fr</dc:language>
    <dc:identifier opf:scheme="uuid">3a60f918-1416-43d3-a297-254f100e0880</dc:identifier>
    <dc:identifier>urn:isbn:978-2-1234-5680-3</dc:identifier>
    <dc:subject>Aventure</dc:subject>
    <meta name="calibre:series" content="Le Voyage"/>
    <meta name="calibre:series_index" content="2.5"/>
    <meta name="calibre:title_sort" content="Voyage, tome 2, Le"/>
    <meta name="cover" content="cover"/>
  </metadata>
  <manifest><item id="cover" href="Images/cover.jpg" media-type="image/jpeg"/></manifest>
</package>`))
	if err != nil {
		t.Fatal(err)
	}
	m := opf.Meta
	if m.Title != "Le Voyage, tome 2" || m.SortTitle != "Voyage, tome 2, Le" || m.Series != "Le Voyage" || m.Number != 2.5 || !m.HasNumber ||
		!slices.Equal(m.Authors, []string{"Autrice Test"}) || !slices.Equal(m.Illustrators, []string{"Illustrateur Test"}) ||
		m.Description != "Un roman de test.\n\nDeuxième paragraphe." || m.Publisher != "Éditions Test" ||
		m.Date != "2023-12-01" || m.Year() != 2023 || m.Language != "fr" || m.ISBN != "9782123456803" ||
		!slices.Equal(m.Genres, []string{"Aventure"}) || opf.CoverHref != "Images/cover.jpg" {
		t.Fatalf("OPF: %+v %q", m, opf.CoverHref)
	}
}

func TestParseComicInfo(t *testing.T) {
	ci, err := ParseComicInfo(strings.NewReader(`<?xml version="1.0"?>
<ComicInfo><Title>Le Commencement</Title><Series>Manga Test</Series><Number>1</Number><Volume>9</Volume>
<Summary>Résumé.</Summary><Year>2020</Year><Month>5</Month><Day>12</Day>
<Writer>Scénariste, Autre</Writer><Penciller>Dessinateur</Penciller><Genre>Action, Aventure</Genre>
<LanguageISO>fr</LanguageISO><Manga>YesAndRightToLeft</Manga>
<Pages><Page Image="0"/><Page Image="1" Type="FrontCover"/></Pages></ComicInfo>`))
	if err != nil {
		t.Fatal(err)
	}
	m := ci.Meta
	if m.Title != "Le Commencement" || m.Series != "Manga Test" || m.Number != 1 || m.Date != "2020-05-12" ||
		!slices.Equal(m.Authors, []string{"Scénariste", "Autre"}) || !slices.Equal(m.Illustrators, []string{"Dessinateur"}) ||
		!slices.Equal(m.Genres, []string{"Action", "Aventure"}) || !m.RightToLeft || ci.FrontCover != 1 {
		t.Fatalf("ComicInfo: %+v %d", m, ci.FrontCover)
	}
}

func TestPDFMetaAndMerge(t *testing.T) {
	m := PDFMeta(map[string]string{"Title": "scan0001.pdf", "Author": "A & B", "CreationDate": "D:20220115093000+01'00'"})
	if m.Title != "" || !slices.Equal(m.Authors, []string{"A", "B"}) || m.Date != "2022-01-15" {
		t.Fatalf("PDF: %+v", m)
	}
	names := BookMeta{Series: "Série", Number: 3, HasNumber: true, Date: "2017"}
	got := names.Merge(BookMeta{Title: "Titre", Authors: []string{"Autrice"}})
	if got.Series != "Série" || got.Number != 3 || got.Title != "Titre" || got.Date != "2017" {
		t.Fatalf("merge: %+v", got)
	}
	// A source that gives a series wins along with its number, even a missing one.
	got = names.Merge(BookMeta{Series: "Autre série"})
	if got.Series != "Autre série" || got.HasNumber {
		t.Fatalf("series merge: %+v", got)
	}
}
