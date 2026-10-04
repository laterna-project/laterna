package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// KidMaxAge is the age limit of a kid profile created without explicit parental control.
const KidMaxAge = 10

// MaxRatingAge is the highest age a rating can map to.
const MaxRatingAge = 21

// ParentalControl restricts content by its rating, converted to an age.
type ParentalControl struct {
	// MaxAge is the highest age allowed; nil means no limit.
	MaxAge *int
	// BlockUnrated also hides content that has no rating.
	BlockUnrated bool
}

// Active reports a control that hides something.
func (c ParentalControl) Active() bool { return c.MaxAge != nil || c.BlockUnrated }

// Combine returns the stricter of the two controls, field by field.
func (c ParentalControl) Combine(o ParentalControl) ParentalControl {
	out := ParentalControl{MaxAge: c.MaxAge, BlockUnrated: c.BlockUnrated || o.BlockUnrated}
	if o.MaxAge != nil && (out.MaxAge == nil || *o.MaxAge < *out.MaxAge) {
		out.MaxAge = o.MaxAge
	}
	return out
}

// Allows reports whether content rated for age is allowed. ok is false for unrated content.
func (c ParentalControl) Allows(age int, ok bool) bool {
	if !ok {
		return !c.BlockUnrated
	}
	return c.MaxAge == nil || age <= *c.MaxAge
}

// KidParentalControl is the default control of a kid profile.
func KidParentalControl() ParentalControl {
	age := KidMaxAge
	return ParentalControl{MaxAge: &age, BlockUnrated: true}
}

// ratingAges maps the ratings commonly found in NFO files (Sonarr, Radarr, Kodi) to an age.
var ratingAges = map[string]int{
	// US television.
	"TV-Y": 0, "TV-G": 0, "TV-Y7": 7, "TV-Y7-FV": 7, "TV-PG": 10, "TV-14": 14, "TV-MA": 17,
	// US movies.
	"G": 0, "PG": 10, "PG-13": 13, "R": 17, "NC-17": 18,
	// France ("-12", "16"... are read as numbers).
	"U": 0, "TP": 0, "TOUS PUBLICS": 0, "TOUT PUBLIC": 0,
	// United Kingdom.
	"12A": 12, "R18": 18,
	// Others.
	"ALL": 0, "E": 0, "EC": 0,
}

var (
	// countryPrefix matches "US:PG-13", "FR:-12" (Kodi).
	countryPrefix = regexp.MustCompile(`^[A-Z]{2,3}\s*:\s*`)
	// countryDash matches "FR-12", "DE-16" (Emby, Jellyfin).
	countryDash = regexp.MustCompile(`^(?:FR|DE|GB|UK|US|JP|ES|IT|BE|CH|CA|NL|AU|BR|KR|SE|NO|DK|FI|IE|NZ)-(.+)$`)
	ageNumber   = regexp.MustCompile(`(?:^|[^0-9])(\d{1,2})(?:[^0-9]|$)`)
)

// RatingAge converts a rating (the "mpaa" field of an NFO: TV-14, PG-13, -12, 16+, FSK 12...) to an
// age. ok is false for an empty or unknown rating (NR, Not Rated...).
func RatingAge(rating string) (age int, ok bool) {
	s := strings.ToUpper(strings.Join(strings.Fields(rating), " "))
	s = strings.TrimPrefix(s, "RATED ")
	s = countryPrefix.ReplaceAllString(s, "")
	if m := countryDash.FindStringSubmatch(s); m != nil {
		if age, ok := ratingAges[m[1]]; ok {
			return age, true
		}
		s = m[1]
	}
	if age, ok := ratingAges[s]; ok {
		return age, true
	}
	if first, _, found := strings.Cut(s, " "); found {
		if age, ok := ratingAges[first]; ok {
			return age, true
		}
	}
	if m := ageNumber.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n <= MaxRatingAge {
			return n, true
		}
	}
	return 0, false
}

// LibraryAccess says which libraries an account may browse.
type LibraryAccess struct {
	// All means every library, including those created later.
	All bool
	// IDs lists the allowed libraries when All is false.
	IDs []ID
}

// AllLibraries grants access to every library.
func AllLibraries() LibraryAccess { return LibraryAccess{All: true} }

// Allows reports whether the library is allowed.
func (l LibraryAccess) Allows(id ID) bool { return l.All || slices.Contains(l.IDs, id) }

// Viewer is a profile browsing the catalog, with what it is allowed to see there: the libraries of
// its account, and the ratings allowed by both the account and the profile.
type Viewer struct {
	ProfileID ID
	// Libraries allowed; nil means all of them.
	Libraries []ID
	Parental  ParentalControl
}

// AllowsLibrary reports whether the library is allowed.
func (v Viewer) AllowsLibrary(id ID) bool {
	return v.Libraries == nil || slices.Contains(v.Libraries, id)
}
