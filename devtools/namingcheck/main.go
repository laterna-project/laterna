// Command namingcheck runs the files of a real library through name parsing (internal/naming) and
// reports what looks doubtful, without writing anything. It is used to try the parser on a real
// corpus (network share, media disk).
//
//	go run ./devtools/namingcheck -movies /media/movies -shows /media/tv -all
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/naming"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var movies, shows list
	flag.Var(&movies, "movies", "root of a movie library (repeatable)")
	flag.Var(&shows, "shows", "root of a series library (repeatable)")
	all := flag.Bool("all", false, "print every file, not only the doubtful ones")
	flag.Parse()
	if len(movies)+len(shows) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	doubtful := 0
	for _, root := range movies {
		doubtful += check(ctx, root, *all, func(rel string) (string, string) {
			m := naming.ParseMovie(rel)
			out := fmt.Sprintf("%s (%d)", m.Title, m.Year)
			if m.Version != "" {
				out += " [version " + m.Version + "]"
			}
			if m.Part > 0 {
				out += fmt.Sprintf(" [partie %d]", m.Part)
			}
			switch {
			case m.Title == "":
				return out, "titre vide"
			case m.Year == 0:
				return out, "unknown year"
			case len(m.Version) > 20:
				return out, "version suspecte"
			}
			return out, ""
		})
	}
	for _, root := range shows {
		groups := map[string]int{}
		doubtful += check(ctx, root, *all, func(rel string) (string, string) {
			e, ok := naming.ParseEpisode(rel)
			if !ok {
				return "?", "episode not recognized"
			}
			out := fmt.Sprintf("%s — S%02dE%02d", e.SeriesTitle, e.Season, e.Episode)
			if e.EpisodeEnd > 0 {
				out += fmt.Sprintf("-E%02d", e.EpisodeEnd)
			}
			if e.Absolute {
				out += " (absolu)"
			}
			groups[e.SeriesTitle]++
			switch {
			case e.SeriesTitle == "":
				return out, "series without a title"
			case e.Season < 0:
				return out, "saison inconnue"
			case e.Episode == 0:
				return out, "episode 0"
			}
			return out, ""
		})
		names := make([]string, 0, len(groups))
		for n := range groups {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("\n%s: %d series\n", root, len(names))
		for _, n := range names {
			fmt.Printf("  %4d  %s\n", groups[n], n)
		}
	}
	fmt.Printf("\n%d doubtful cases\n", doubtful)
}

// check walks root and prints the result of parse for each file (or only the doubtful ones). It
// returns the number of doubtful cases.
func check(ctx context.Context, root string, all bool, parse func(rel string) (string, string)) int {
	walked, err := library.Walk(ctx, []string{root}, naming.IsVideo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, r := range append(walked.Unavailable, walked.Unreadable...) {
		fmt.Printf("UNREACHABLE %s\n", r)
	}
	n := 0
	fmt.Printf("== %s: %d files\n", root, len(walked.Entries))
	for _, e := range walked.Entries {
		got, problem := parse(e.Rel)
		if problem != "" {
			n++
		}
		if all || problem != "" {
			mark := "  "
			if problem != "" {
				mark = "!!"
			}
			fmt.Printf("%s %-60s ← %s", mark, got, path.Base(e.Rel))
			if problem != "" {
				fmt.Printf("   [%s]", problem)
			}
			fmt.Println()
		}
	}
	return n
}
