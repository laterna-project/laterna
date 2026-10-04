package domain

import "time"

// ImageKind is what an image is used for.
type ImageKind string

// Image kinds.
const (
	// ImagePoster is a portrait poster (2:3). Also the square cover of an album and the picture of
	// an artist.
	ImagePoster ImageKind = "poster"
	// ImageBackdrop is a landscape background (16:9) without text.
	ImageBackdrop ImageKind = "backdrop"
	// ImageLogo is the title logo on a transparent background.
	ImageLogo ImageKind = "logo"
	// ImageThumb is a landscape thumbnail (episode, landscape card).
	ImageThumb ImageKind = "thumb"
	// ImageBanner is a very wide banner.
	ImageBanner ImageKind = "banner"
	// ImagePhoto is the photo itself, for photo items, already rotated.
	ImagePhoto ImageKind = "photo"
)

// ImageKinds lists the kinds in display preference order.
var ImageKinds = []ImageKind{ImagePoster, ImageBackdrop, ImageLogo, ImageThumb, ImageBanner, ImagePhoto}

// ImageSource says where an image comes from.
type ImageSource string

// Image sources.
const (
	// ImageLocal is a file next to the media, read in place. It is never copied or changed.
	ImageLocal ImageSource = "local"
	// ImageRemote was downloaded from a URL found in an NFO and is kept in the metadata folder.
	ImageRemote ImageSource = "remote"
	// ImageEmbedded is a cover pulled out of an audio file and kept in the metadata folder.
	ImageEmbedded ImageSource = "embedded"
	// ImageUpload was uploaded by an administrator (theme logo or background) and is kept in the
	// metadata folder.
	ImageUpload ImageSource = "upload"
)

// Image belongs to an item, a person or a theme, exactly one of them.
type Image struct {
	ID       ID
	ItemID   *ID
	PersonID *ID
	// ThemeID is the theme this image is the logo or background of.
	ThemeID *ID
	Kind    ImageKind
	Source  ImageSource
	// Path is the file on the server.
	Path string
	// RemoteURL is where a downloaded image came from.
	RemoteURL string
	Width     int
	Height    int
	// BlurHash is a tiny blurred preview shown while the image loads.
	BlurHash string
	// Hash identifies the content. It changes when the image does, which lets clients cache
	// forever.
	Hash      string
	UpdatedAt time.Time
}
