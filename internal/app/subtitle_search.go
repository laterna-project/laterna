package app

import (
	"cmp"
	"context"
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/bazarr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/store"
)

// Subtitle searches (docs/design/subtitles.md): a profile asks for a subtitle of a movie or an
// episode in a language, Laterna hands the search to Bazarr, which saves what it finds next to the
// video, and the subtitles of the file are read again. Laterna itself talks to no subtitle
// provider.
const (
	// jobSearchSubtitle runs a search; its target names the file and what is wanted.
	jobSearchSubtitle = "subtitle.search"
	// classSubtitleSearch: mostly waiting for Bazarr, two at a time.
	classSubtitleSearch = "subtitle_search"
	// defaultSubtitleSearchWait is how long Bazarr gets to find a subtitle, and
	// defaultSubtitleSearchPoll how often it is asked.
	defaultSubtitleSearchWait = 2 * time.Minute
	defaultSubtitleSearchPoll = 3 * time.Second
	// subtitleSearchKept is how long a finished search is remembered. Within subtitleSearchRetry, one
	// that found nothing is not run again: providers count what they are asked.
	subtitleSearchKept  = 30 * time.Minute
	subtitleSearchRetry = 10 * time.Minute
	// maxSubtitleSearches caps the searches of a profile over an hour.
	maxSubtitleSearches = 20
	// subtitleLanguagesTTL is how long the languages Bazarr offers are kept.
	subtitleLanguagesTTL = 10 * time.Minute
)

// subtitleSearches are the searches under way or just finished, and the languages Bazarr offers.
type subtitleSearches struct {
	mu          sync.Mutex
	byKey       map[string]*domain.SubtitleSearch
	languages   []domain.SubtitleLanguage
	languagesAt time.Time
}

// SubtitleSearchInfo says whether a subtitle can be looked for a file, and where its searches
// stand.
type SubtitleSearchInfo struct {
	// Available is false without Bazarr, or when it does not answer.
	Available bool
	Languages []domain.SubtitleLanguage
	Searches  []domain.SubtitleSearch
}

// subtitleSearchKey names a search: a file and what is wanted. It is also the target of its job.
func subtitleSearchKey(fileID domain.ID, w domain.SubtitleWanted) string {
	flags := ""
	if w.HearingImpaired {
		flags += "h"
	}
	if w.Forced {
		flags += "f"
	}
	return fileID.String() + ":" + w.Language + ":" + flags
}

func parseSubtitleSearchKey(key string) (domain.ID, domain.SubtitleWanted, error) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		return domain.ID{}, domain.SubtitleWanted{}, errors.New("invalid subtitle search target")
	}
	id, err := domain.ParseID(parts[0])
	if err != nil {
		return domain.ID{}, domain.SubtitleWanted{}, err
	}
	return id, domain.SubtitleWanted{
		Language: parts[1], HearingImpaired: strings.Contains(parts[2], "h"), Forced: strings.Contains(parts[2], "f"),
	}, nil
}

// forgetSubtitleLanguages drops the languages read from Bazarr (it was linked, changed or unlinked).
func (a *App) forgetSubtitleLanguages() {
	a.subSearches.mu.Lock()
	defer a.subSearches.mu.Unlock()
	a.subSearches.languages, a.subSearches.languagesAt = nil, time.Time{}
}

// subtitleLanguages lists the languages Bazarr looks subtitles up in. linked is false without
// Bazarr.
func (a *App) subtitleLanguages(ctx context.Context) (langs []domain.SubtitleLanguage, linked bool, err error) {
	s, ok, err := a.loadIntegration(ctx, bazarrKey)
	if err != nil || !ok {
		return nil, false, err
	}
	now := a.now()
	a.subSearches.mu.Lock()
	if a.subSearches.languages != nil && now.Sub(a.subSearches.languagesAt) < subtitleLanguagesTTL {
		langs = slices.Clone(a.subSearches.languages)
		a.subSearches.mu.Unlock()
		return langs, true, nil
	}
	a.subSearches.mu.Unlock()
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := a.bazarrClient(s).Languages(check)
	if err != nil {
		return nil, true, err
	}
	langs = make([]domain.SubtitleLanguage, 0, len(list))
	for _, l := range list {
		langs = append(langs, domain.SubtitleLanguage{Code: strings.ToLower(l.Code), Name: l.Name})
	}
	a.subSearches.mu.Lock()
	a.subSearches.languages, a.subSearches.languagesAt = slices.Clone(langs), now
	a.subSearches.mu.Unlock()
	return langs, true, nil
}

// subtitleItem returns the item of a file, which the profile has to see.
func (a *App) subtitleItem(ctx context.Context, p domain.Principal, fileID domain.ID) (domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.ItemView{}, err
	}
	read := a.store.Read()
	itemID, err := read.FileItem(ctx, fileID)
	if store.IsNotFound(err) {
		return domain.ItemView{}, domain.NotFound("subtitle.file_not_found")
	}
	if err != nil {
		return domain.ItemView{}, err
	}
	item, err := read.View(ctx, v, itemID)
	if store.IsNotFound(err) {
		return domain.ItemView{}, domain.NotFound("subtitle.file_not_found")
	}
	return item, err
}

// searchable reports an item Bazarr can find subtitles for: a movie or an episode.
func searchable(item domain.ItemView) bool {
	return item.Item.Kind == domain.ItemMovie || item.Item.Kind == domain.ItemEpisode
}

// SubtitleSearch says whether a subtitle can be looked for a file the profile sees, in which
// languages, and where the searches for that file stand.
func (a *App) SubtitleSearch(ctx context.Context, p domain.Principal, fileID domain.ID) (SubtitleSearchInfo, error) {
	item, err := a.subtitleItem(ctx, p, fileID)
	if err != nil {
		return SubtitleSearchInfo{}, err
	}
	info := SubtitleSearchInfo{Searches: a.searchesOf(fileID)}
	if !searchable(item) {
		return info, nil
	}
	langs, linked, err := a.subtitleLanguages(ctx)
	if err != nil {
		// Bazarr does not answer: nothing to offer for now, and no reason to fail the screen.
		a.log.WarnContext(ctx, "subtitle search: cannot read the languages of Bazarr", "err", err)
		return info, nil
	}
	info.Available, info.Languages = linked && len(langs) > 0, langs
	return info, nil
}

// searchesOf lists the searches remembered for a file, oldest first (then by language), and
// forgets the old ones.
func (a *App) searchesOf(fileID domain.ID) []domain.SubtitleSearch {
	now := a.now()
	a.subSearches.mu.Lock()
	defer a.subSearches.mu.Unlock()
	var out []domain.SubtitleSearch
	for key, s := range a.subSearches.byKey {
		if s.State != domain.SubtitleSearching && now.Sub(s.StartedAt) > subtitleSearchKept {
			delete(a.subSearches.byKey, key)
			continue
		}
		if s.FileID == fileID {
			out = append(out, *s)
		}
	}
	slices.SortFunc(out, func(x, y domain.SubtitleSearch) int {
		return cmp.Or(x.StartedAt.Compare(y.StartedAt), cmp.Compare(subtitleSearchKey(x.FileID, x.SubtitleWanted), subtitleSearchKey(y.FileID, y.SubtitleWanted)))
	})
	return out
}

// SearchSubtitle asks Bazarr for a subtitle of a file the profile sees. The search runs in the
// background and its end is announced (SubtitleSearchChanged). Asking again for a search that is
// under way, or that just found nothing, returns it as it stands.
func (a *App) SearchSubtitle(ctx context.Context, p domain.Principal, fileID domain.ID, w domain.SubtitleWanted) (domain.SubtitleSearch, error) {
	item, err := a.subtitleItem(ctx, p, fileID)
	if err != nil {
		return domain.SubtitleSearch{}, err
	}
	if !searchable(item) {
		return domain.SubtitleSearch{}, domain.Invalid("subtitle.search_unsupported")
	}
	langs, linked, err := a.subtitleLanguages(ctx)
	if err != nil {
		return domain.SubtitleSearch{}, domain.FromText(domain.ErrPrecondition, integrationProblem(bazarrName, err))
	}
	if !linked {
		return domain.SubtitleSearch{}, domain.Precondition("subtitle.search_unavailable")
	}
	w.Language = strings.ToLower(strings.TrimSpace(w.Language))
	if !slices.ContainsFunc(langs, func(l domain.SubtitleLanguage) bool { return l.Code == w.Language }) {
		return domain.SubtitleSearch{}, domain.Invalid("subtitle.language_unavailable", "language", w.Language)
	}

	now := a.now()
	key := subtitleSearchKey(fileID, w)
	a.subSearches.mu.Lock()
	if a.subSearches.byKey == nil {
		a.subSearches.byKey = map[string]*domain.SubtitleSearch{}
	}
	if s := a.subSearches.byKey[key]; s != nil {
		recent := now.Sub(s.StartedAt) < subtitleSearchRetry
		if s.State == domain.SubtitleSearching || (s.State == domain.SubtitleNotFound && recent) {
			defer a.subSearches.mu.Unlock()
			return *s, nil
		}
	}
	asked := 0
	for _, s := range a.subSearches.byKey {
		if s.ProfileID == p.Profile.ID && now.Sub(s.StartedAt) < time.Hour {
			asked++
		}
	}
	if asked >= maxSubtitleSearches {
		a.subSearches.mu.Unlock()
		return domain.SubtitleSearch{}, domain.Precondition("subtitle.too_many_searches", "max", maxSubtitleSearches)
	}
	search := domain.SubtitleSearch{FileID: fileID, SubtitleWanted: w, State: domain.SubtitleSearching, StartedAt: now, ProfileID: p.Profile.ID}
	a.subSearches.byKey[key] = &search
	a.subSearches.mu.Unlock()

	if err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.Enqueue(ctx, q, jobSearchSubtitle, key, priorityUser)
	}); err != nil {
		a.subSearches.mu.Lock()
		delete(a.subSearches.byKey, key)
		a.subSearches.mu.Unlock()
		return domain.SubtitleSearch{}, err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "subtitle search asked", "item", item.Item.Title, "language", w.Language, "profile", p.Profile.Name)
	a.bus.Publish(domain.SubtitleSearchChanged{ProfileID: p.Profile.ID, FileID: fileID})
	return search, nil
}

// searchSubtitle runs a subtitle search (job subtitle.search): find the video on Bazarr, ask for
// the search, wait for a subtitle to arrive, then read the subtitles of the file again. A search
// that cannot go through fails with its reason and is not tried again on its own: the profile can
// ask again.
func (a *App) searchSubtitle(ctx context.Context, target string) error {
	fileID, wanted, err := parseSubtitleSearchKey(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	finish := func(state domain.SubtitleSearchState, reason *domain.Text) {
		a.subSearches.mu.Lock()
		if a.subSearches.byKey == nil {
			a.subSearches.byKey = map[string]*domain.SubtitleSearch{}
		}
		s := a.subSearches.byKey[target]
		if s == nil {
			// Asked before a restart: nobody is waiting for it, but it is remembered.
			s = &domain.SubtitleSearch{FileID: fileID, SubtitleWanted: wanted, StartedAt: a.now()}
			a.subSearches.byKey[target] = s
		}
		s.State, s.Error = state, reason
		profile := s.ProfileID
		a.subSearches.mu.Unlock()
		a.bus.Publish(domain.SubtitleSearchChanged{ProfileID: profile, FileID: fileID})
	}
	fail := func(reason domain.Text) error {
		a.log.WarnContext(ctx, "subtitle search failed", "file", fileID, "language", wanted.Language, "reason", reason.String())
		finish(domain.SubtitleSearchFailed, &reason)
		return nil
	}

	read := a.store.Read()
	f, err := read.File(ctx, fileID)
	if store.IsNotFound(err) {
		return fail(domain.T("subtitle_search.file_gone"))
	}
	if err != nil {
		return err
	}
	itemID, err := read.FileItem(ctx, fileID)
	if store.IsNotFound(err) {
		return fail(domain.T("subtitle_search.file_gone"))
	}
	if err != nil {
		return err
	}
	s, ok, err := a.loadIntegration(ctx, bazarrKey)
	if err != nil {
		return err
	}
	if !ok {
		return fail(domain.T("error.subtitle.search_unavailable"))
	}
	c := a.bazarrClient(s)
	video, found, err := a.bazarrVideo(ctx, c, f, itemID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fail(integrationProblem(bazarrName, err))
	}
	if !found {
		return fail(domain.T("subtitle_search.not_followed", "name", bazarrName))
	}

	want := bazarr.Wanted{Language: wanted.Language, Forced: wanted.Forced, HearingImpaired: wanted.HearingImpaired}
	had := matching(video.Subtitles, want)
	before, err := a.externalSubtitles(ctx, fileID)
	if err != nil {
		return err
	}
	if video.SeriesID != 0 {
		err = c.SearchEpisode(ctx, video.SeriesID, video.ID, want)
	} else {
		err = c.SearchMovie(ctx, video.ID, want)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fail(integrationProblem(bazarrName, err))
	}
	arrived, err := a.awaitSubtitle(ctx, c, video, want, had)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fail(integrationProblem(bazarrName, err))
	}
	if !arrived {
		a.log.InfoContext(ctx, "subtitle search: nothing found", "path", f.Path, "language", wanted.Language)
		finish(domain.SubtitleNotFound, nil)
		return nil
	}
	// The subtitle is next to the video: read the file's subtitles again, as a scan would.
	if err := a.extractSubtitles(ctx, fileID.String()); err != nil {
		return fail(domain.T("subtitle_search.not_read", "reason", err))
	}
	after, err := a.externalSubtitles(ctx, fileID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(after, func(p string) bool { return !slices.Contains(before, p) }) {
		// Bazarr and Laterna do not see the same folder, or the file is not named after the video.
		return fail(domain.T("subtitle_search.not_visible", "name", bazarrName))
	}
	a.log.InfoContext(ctx, "subtitle found", "path", f.Path, "language", wanted.Language)
	a.itemsChanged(f.LibraryID, itemID)
	finish(domain.SubtitleFound, nil)
	return nil
}

// externalSubtitles lists the external subtitle files Laterna knows for a file.
func (a *App) externalSubtitles(ctx context.Context, fileID domain.ID) ([]string, error) {
	set, _, err := a.store.Read().SubtitleSet(ctx, fileID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range set.Subtitles {
		if s.External() {
			out = append(out, s.Path)
		}
	}
	return out, nil
}

// matching lists the files of the subtitles that are what is wanted.
func matching(subs []bazarr.Subtitle, w bazarr.Wanted) []string {
	var out []string
	for _, s := range subs {
		if s.Path != "" && strings.EqualFold(s.Language, w.Language) && s.Forced == w.Forced && s.HearingImpaired == w.HearingImpaired {
			out = append(out, s.Path)
		}
	}
	return out
}

// awaitSubtitle waits for Bazarr to have a subtitle it did not have. Recent versions search in the
// background, older ones are done when they answer: both are read the same way.
func (a *App) awaitSubtitle(ctx context.Context, c *bazarr.Client, video bazarr.Video, w bazarr.Wanted, had []string) (bool, error) {
	deadline := time.NewTimer(a.subtitleSearchWait)
	defer deadline.Stop()
	tick := time.NewTicker(a.subtitleSearchPoll)
	defer tick.Stop()
	for {
		var now bazarr.Video
		var ok bool
		var err error
		if video.SeriesID != 0 {
			now, ok, err = c.Episode(ctx, video.ID)
		} else {
			now, ok, err = c.Movie(ctx, video.ID)
		}
		if err != nil {
			return false, err
		}
		if ok && slices.ContainsFunc(matching(now.Subtitles, w), func(p string) bool { return !slices.Contains(had, p) }) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-tick.C:
		}
	}
}

// bazarrVideo finds the episode or the movie of a file among what Bazarr follows: by the ID its
// NFO gives (TVDB for the series, IMDb for a movie), else by the names of its folder and file.
// Bazarr may see the files under other folders than Laterna does.
func (a *App) bazarrVideo(ctx context.Context, c *bazarr.Client, f domain.MediaFile, itemID domain.ID) (bazarr.Video, bool, error) {
	read := a.store.Read()
	item, err := read.View(ctx, domain.Viewer{}, itemID)
	if err != nil {
		return bazarr.Video{}, false, err
	}
	file := baseName(f.Path)
	if item.Episode == nil {
		details, err := read.Details(ctx, itemID)
		if err != nil {
			return bazarr.Video{}, false, err
		}
		movies, err := c.Movies(ctx)
		if err != nil {
			return bazarr.Video{}, false, err
		}
		imdb := details.ProviderIDs["imdb"]
		byFile := -1
		for i, m := range movies {
			if imdb != "" && strings.EqualFold(m.ImdbID, imdb) {
				return m, true, nil
			}
			if strings.EqualFold(baseName(m.Path), file) {
				byFile = i
			}
		}
		if byFile >= 0 {
			return movies[byFile], true, nil
		}
		return bazarr.Video{}, false, nil
	}

	details, err := read.Details(ctx, item.Episode.SeriesID)
	if err != nil {
		return bazarr.Video{}, false, err
	}
	all, err := c.Series(ctx)
	if err != nil {
		return bazarr.Video{}, false, err
	}
	folders := strings.Split(strings.ToLower(slashed(f.Path)), "/")
	tvdb := details.ProviderIDs["tvdb"]
	series := -1
	for i, s := range all {
		if tvdb != "" && tvdb == strconv.FormatInt(s.TvdbID, 10) {
			series = i
			break
		}
		if series < 0 && slices.Contains(folders, strings.ToLower(baseName(s.Path))) {
			series = i
		}
	}
	if series < 0 {
		return bazarr.Video{}, false, nil
	}
	episodes, err := c.Episodes(ctx, all[series].ID)
	if err != nil {
		return bazarr.Video{}, false, err
	}
	byNumber := -1
	for i, e := range episodes {
		if strings.EqualFold(baseName(e.Path), file) {
			return e, true, nil
		}
		if e.Season == item.Episode.SeasonNumber && e.Episode == item.Episode.Number {
			byNumber = i
		}
	}
	if byNumber >= 0 {
		return episodes[byNumber], true, nil
	}
	return bazarr.Video{}, false, nil
}

// slashed writes a path with forward slashes, whichever system wrote it.
func slashed(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// baseName is the last element of a path, whichever system wrote it.
func baseName(p string) string {
	if p = strings.TrimRight(slashed(p), "/"); p == "" {
		return ""
	}
	return path.Base(p)
}
