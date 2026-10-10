package arr

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// Titles: searching for a series or a movie, adding it, following its download (requests,
// docs/design/requests.md).

// Title is a series (Sonarr) or a movie (Radarr), found by a search or tracked by the instance.
type Title struct {
	// ExternalID is the TVDB ID of a series, the TMDB ID of a movie.
	ExternalID int64
	Title      string
	Year       int
	Overview   string
	// Poster is the address of its poster on TVDB or TMDB; empty if none.
	Poster string
	// Network of a series, studio of a movie.
	Network string
	// Seasons of a series, specials excluded.
	Seasons []int
	// ArrID is the series or movie on the instance; 0 if the instance does not have it.
	ArrID     int
	Monitored bool
	// Series: episodes with a file, and monitored episodes that aired. Movie: HasFile.
	EpisodeFiles   int
	EpisodesWanted int
	HasFile        bool
}

// titleJSON is what Title reads of a series or a movie. Adding one sends back the whole resource
// the search returned instead (lookup).
type titleJSON struct {
	ID        int    `json:"id"`
	TvdbID    int64  `json:"tvdbId"`
	TmdbID    int64  `json:"tmdbId"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Overview  string `json:"overview"`
	Network   string `json:"network"`
	Studio    string `json:"studio"`
	Monitored bool   `json:"monitored"`
	HasFile   bool   `json:"hasFile"`
	Images    []struct {
		CoverType string `json:"coverType"`
		RemoteURL string `json:"remoteUrl"`
	} `json:"images"`
	Seasons []struct {
		SeasonNumber int `json:"seasonNumber"`
	} `json:"seasons"`
	Statistics struct {
		EpisodeFileCount int `json:"episodeFileCount"`
		EpisodeCount     int `json:"episodeCount"`
	} `json:"statistics"`
}

func (c *Client) title(t titleJSON) Title {
	out := Title{
		ExternalID: t.TvdbID, Title: t.Title, Year: t.Year, Overview: t.Overview, Network: t.Network,
		ArrID: t.ID, Monitored: t.Monitored, EpisodeFiles: t.Statistics.EpisodeFileCount,
		EpisodesWanted: t.Statistics.EpisodeCount, HasFile: t.HasFile,
	}
	if c.kind == Radarr {
		out.ExternalID, out.Network = t.TmdbID, t.Studio
	}
	for _, img := range t.Images {
		if img.CoverType == "poster" && img.RemoteURL != "" {
			out.Poster = img.RemoteURL
			break
		}
	}
	for _, s := range t.Seasons {
		if s.SeasonNumber > 0 {
			out.Seasons = append(out.Seasons, s.SeasonNumber)
		}
	}
	slices.Sort(out.Seasons)
	return out
}

func (c *Client) path(kind string) string {
	if c.kind == Radarr {
		return "/movie" + kind
	}
	return "/series" + kind
}

// Search looks a title up (TVDB through Sonarr, TMDB through Radarr), best match first.
func (c *Client) Search(ctx context.Context, term string) ([]Title, error) {
	var list []titleJSON
	if err := c.do(ctx, http.MethodGet, c.path("/lookup")+"?term="+url.QueryEscape(term), nil, &list); err != nil {
		return nil, err
	}
	out := make([]Title, len(list))
	for i, t := range list {
		out[i] = c.title(t)
	}
	return out, nil
}

// lookup finds a title by its external ID, as the search returns it (the body adding it needs).
func (c *Client) lookup(ctx context.Context, id int64) (resource, error) {
	term := c.term(id)
	var list []resource
	if err := c.do(ctx, http.MethodGet, c.path("/lookup")+"?term="+url.QueryEscape(term), nil, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.externalID(c.kind) == id {
			return r, nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Message: "unknown title " + term}
}

// term is the search term that finds a title by its external ID.
func (c *Client) term(id int64) string {
	if c.kind == Radarr {
		return "tmdb:" + strconv.FormatInt(id, 10)
	}
	return "tvdb:" + strconv.FormatInt(id, 10)
}

// Find looks a title up by its external ID, as a search returns it; false if there is none.
func (c *Client) Find(ctx context.Context, id int64) (Title, bool, error) {
	found, err := c.Search(ctx, c.term(id))
	if err != nil {
		return Title{}, false, err
	}
	for _, t := range found {
		if t.ExternalID == id {
			return t, true, nil
		}
	}
	return Title{}, false, nil
}

func (r resource) externalID(k Kind) int64 {
	field := "tvdbId"
	if k == Radarr {
		field = "tmdbId"
	}
	n, _ := r[field].(float64)
	return int64(n)
}

// Tracked returns the title the instance has with that external ID; false if it has none.
func (c *Client) Tracked(ctx context.Context, id int64) (Title, bool, error) {
	param := "?tvdbId="
	if c.kind == Radarr {
		param = "?tmdbId="
	}
	var list []titleJSON
	if err := c.do(ctx, http.MethodGet, c.path("")+param+strconv.FormatInt(id, 10), nil, &list); err != nil {
		return Title{}, false, err
	}
	for _, t := range list {
		if out := c.title(t); out.ExternalID == id && out.ArrID > 0 {
			return out, true, nil
		}
	}
	return Title{}, false, nil
}

// Get reads a title of the instance by its ID there.
func (c *Client) Get(ctx context.Context, arrID int) (Title, error) {
	var t titleJSON
	if err := c.do(ctx, http.MethodGet, c.path("/"+strconv.Itoa(arrID)), nil, &t); err != nil {
		return Title{}, err
	}
	return c.title(t), nil
}

// RootFolder is a root folder of the instance.
type RootFolder struct {
	Path      string `json:"path"`
	FreeSpace int64  `json:"freeSpace"`
}

// RootFolders lists the root folders of the instance.
func (c *Client) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var out []RootFolder
	return out, c.do(ctx, http.MethodGet, "/rootfolder", nil, &out)
}

// QualityProfile is a quality profile of the instance.
type QualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// QualityProfiles lists the quality profiles of the instance.
func (c *Client) QualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	var out []QualityProfile
	return out, c.do(ctx, http.MethodGet, "/qualityprofile", nil, &out)
}

// AddOptions says where and how a title is added.
type AddOptions struct {
	RootFolder       string
	QualityProfileID int
	// SeriesType is "standard", "anime" or "daily" (Sonarr).
	SeriesType string
	// Seasons is "all", "first", "latest" or "chosen" (SeasonNumbers) for a series.
	Seasons       string
	SeasonNumbers []int
}

// Add adds a title to the instance, monitored, and searches for it. A title the instance already
// has is monitored and searched instead (Monitor). It returns the title's ID on the instance.
func (c *Client) Add(ctx context.Context, id int64, o AddOptions) (int, error) {
	if t, ok, err := c.Tracked(ctx, id); err != nil {
		return 0, err
	} else if ok {
		return t.ArrID, c.Monitor(ctx, t.ArrID, o)
	}
	r, err := c.lookup(ctx, id)
	if err != nil {
		return 0, err
	}
	r["qualityProfileId"] = o.QualityProfileID
	r["rootFolderPath"] = o.RootFolder
	r["monitored"] = true
	if c.kind == Radarr {
		r["minimumAvailability"] = "released"
		r["addOptions"] = map[string]any{"searchForMovie": true, "monitor": "movieOnly"}
	} else {
		r["seriesType"] = o.SeriesType
		r["seasonFolder"] = true
		r["monitorNewItems"] = "all"
		monitor := map[string]string{"first": "firstSeason", "latest": "latestSeason", "chosen": "none"}[o.Seasons]
		if monitor == "" {
			monitor = "all"
		}
		r["addOptions"] = map[string]any{
			"monitor": monitor, "searchForMissingEpisodes": o.Seasons != "chosen", "searchForCutoffUnmetEpisodes": false,
		}
	}
	var added struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, c.path(""), r, &added); err != nil {
		return 0, err
	}
	if c.kind == Sonarr && o.Seasons == "chosen" {
		// Added without monitoring anything (Sonarr then leaves the series itself unmonitored):
		// monitor the chosen seasons, then search them.
		return added.ID, c.Monitor(ctx, added.ID, o)
	}
	return added.ID, nil
}

// Monitor monitors a title the instance has (for a series, the seasons of o), then searches for
// what is missing.
func (c *Client) Monitor(ctx context.Context, arrID int, o AddOptions) error {
	var r resource
	if err := c.do(ctx, http.MethodGet, c.path("/"+strconv.Itoa(arrID)), nil, &r); err != nil {
		return err
	}
	if r == nil {
		return &Error{Status: http.StatusNotFound, Message: "unknown title " + strconv.Itoa(arrID)}
	}
	r["monitored"] = true
	if c.kind == Sonarr {
		seasons, _ := r["seasons"].([]any)
		var numbers []int
		for _, s := range seasons {
			if m, ok := s.(map[string]any); ok {
				if n, ok := m["seasonNumber"].(float64); ok && n > 0 {
					numbers = append(numbers, int(n))
				}
			}
		}
		slices.Sort(numbers)
		wanted := seasonsWanted(o, numbers)
		for _, s := range seasons {
			if m, ok := s.(map[string]any); ok {
				n, _ := m["seasonNumber"].(float64)
				m["monitored"] = slices.Contains(wanted, int(n))
			}
		}
	}
	if err := c.do(ctx, http.MethodPut, c.path("/"+strconv.Itoa(arrID))+"?moveFiles=false", r, nil); err != nil {
		return err
	}
	cmd := map[string]any{"name": "SeriesSearch", "seriesId": arrID}
	if c.kind == Radarr {
		cmd = map[string]any{"name": "MoviesSearch", "movieIds": []int{arrID}}
	}
	return c.do(ctx, http.MethodPost, "/command", cmd, nil)
}

// seasonsWanted lists the seasons a request monitors among those of the series.
func seasonsWanted(o AddOptions, seasons []int) []int {
	if len(seasons) == 0 {
		return nil
	}
	switch o.Seasons {
	case "first":
		return seasons[:1]
	case "latest":
		return seasons[len(seasons)-1:]
	case "chosen":
		return slices.DeleteFunc(slices.Clone(o.SeasonNumbers), func(n int) bool { return !slices.Contains(seasons, n) })
	}
	return seasons
}

// Download is a title being downloaded by the instance.
type Download struct {
	// ArrID is the series, movie or artist on the instance; AlbumID the album (Lidarr), 0 otherwise.
	ArrID   int
	AlbumID int
	// Size and Left are in bytes.
	Size, Left float64
}

// Queue lists what the instance is downloading, title by title (episodes of a series added up,
// tracks of an album).
func (c *Client) Queue(ctx context.Context) ([]Download, error) {
	var page struct {
		Records []struct {
			SeriesID int     `json:"seriesId"`
			MovieID  int     `json:"movieId"`
			ArtistID int     `json:"artistId"`
			AlbumID  int     `json:"albumId"`
			Size     float64 `json:"size"`
			Left     float64 `json:"sizeleft"`
		} `json:"records"`
	}
	if err := c.do(ctx, http.MethodGet, "/queue?page=1&pageSize=500", nil, &page); err != nil {
		return nil, err
	}
	type key struct{ arr, album int }
	index := map[key]int{}
	out := []Download{}
	for _, r := range page.Records {
		k := key{arr: r.SeriesID}
		switch c.kind {
		case Radarr:
			k.arr = r.MovieID
		case Lidarr:
			k = key{arr: r.ArtistID, album: r.AlbumID}
		case Sonarr:
		}
		if k.arr == 0 {
			continue
		}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, Download{ArrID: k.arr, AlbumID: k.album})
		}
		out[i].Size += r.Size
		out[i].Left += r.Left
	}
	return out, nil
}
