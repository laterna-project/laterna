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
    <dc:title>The Voyage, Book 2</dc:title>
    <dc:creator opf:role="aut">Author Test</dc:creator>
    <dc:creator opf:role="ill">Illustrator Test</dc:creator>
    <dc:contributor opf:role="bkp">calibre (6.9.0)</dc:contributor>
    <dc:description>&lt;p&gt;A &lt;b&gt;test&lt;/b&gt; novel.&lt;/p&gt;&lt;p&gt;Second&amp;nbsp;paragraph.&lt;/p&gt;</dc:description>
    <dc:publisher>Test Press</dc:publisher>
    <dc:date>2023-11-30T23:00:00+00:00</dc:date>
    <dc:language>fr</dc:language>
    <dc:identifier opf:scheme="uuid">3a60f918-1416-43d3-a297-254f100e0880</dc:identifier>
    <dc:identifier>urn:isbn:978-2-1234-5680-3</dc:identifier>
    <dc:subject>Adventure</dc:subject>
    <meta name="calibre:series" content="The Voyage"/>
    <meta name="calibre:series_index" content="2.5"/>
    <meta name="calibre:title_sort" content="Voyage, Book 2, The"/>
    <meta name="cover" content="cover"/>
  </metadata>
  <manifest><item id="cover" href="Images/cover.jpg" media-type="image/jpeg"/></manifest>
</package>`))
	if err != nil {
		t.Fatal(err)
	}
	m := opf.Meta
	if m.Title != "The Voyage, Book 2" || m.SortTitle != "Voyage, Book 2, The" || m.Series != "The Voyage" || m.Number != 2.5 || !m.HasNumber ||
		!slices.Equal(m.Authors, []string{"Author Test"}) || !slices.Equal(m.Illustrators, []string{"Illustrator Test"}) ||
		m.Description != "A test novel.\n\nSecond\u00a0paragraph." || m.Publisher != "Test Press" ||
		m.Date != "2023-12-01" || m.Year() != 2023 || m.Language != "fr" || m.ISBN != "9782123456803" ||
		!slices.Equal(m.Genres, []string{"Adventure"}) || opf.CoverHref != "Images/cover.jpg" {
		t.Fatalf("OPF: %+v %q", m, opf.CoverHref)
	}
}

func TestParseComicInfo(t *testing.T) {
	ci, err := ParseComicInfo(strings.NewReader(`<?xml version="1.0"?>
<ComicInfo><Title>The Beginning</Title><Series>Manga Test</Series><Number>1</Number><Volume>9</Volume>
<Summary>Summary.</Summary><Year>2020</Year><Month>5</Month><Day>12</Day>
<Writer>Writer, Other</Writer><Penciller>Penciller</Penciller><Genre>Action, Adventure</Genre>
<LanguageISO>fr</LanguageISO><Manga>YesAndRightToLeft</Manga>
<Pages><Page Image="0"/><Page Image="1" Type="FrontCover"/></Pages></ComicInfo>`))
	if err != nil {
		t.Fatal(err)
	}
	m := ci.Meta
	if m.Title != "The Beginning" || m.Series != "Manga Test" || m.Number != 1 || m.Date != "2020-05-12" ||
		!slices.Equal(m.Authors, []string{"Writer", "Other"}) || !slices.Equal(m.Illustrators, []string{"Penciller"}) ||
		!slices.Equal(m.Genres, []string{"Action", "Adventure"}) || !m.RightToLeft || ci.FrontCover != 1 {
		t.Fatalf("ComicInfo: %+v %d", m, ci.FrontCover)
	}
}

func TestPDFMetaAndMerge(t *testing.T) {
	m := PDFMeta(map[string]string{"Title": "scan0001.pdf", "Author": "A & B", "CreationDate": "D:20220115093000+01'00'"})
	if m.Title != "" || !slices.Equal(m.Authors, []string{"A", "B"}) || m.Date != "2022-01-15" {
		t.Fatalf("PDF: %+v", m)
	}
	names := BookMeta{Series: "Series", Number: 3, HasNumber: true, Date: "2017"}
	got := names.Merge(BookMeta{Title: "Title", Authors: []string{"Author"}})
	if got.Series != "Series" || got.Number != 3 || got.Title != "Title" || got.Date != "2017" {
		t.Fatalf("merge: %+v", got)
	}
	// A source that gives a series wins along with its number, even a missing one.
	got = names.Merge(BookMeta{Series: "Other series"})
	if got.Series != "Other series" || got.HasNumber {
		t.Fatalf("series merge: %+v", got)
	}
}
