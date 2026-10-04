package domain

import "time"

// Photo is the photo-specific part of an item. Its parent is its album.
type Photo struct {
	ItemID ID
	// TakenAt is when the picture was taken: exact if the camera recorded its time zone (UTCOffset,
	// in minutes), otherwise the camera's clock read in the server's time zone, otherwise the file
	// date.
	TakenAt   time.Time
	UTCOffset *int
	// Width and Height as displayed, orientation applied.
	Width, Height int
	Make, Model   string
	Lens          string
	// FNumber is the aperture (f/...), FocalLength is in mm; 0 if unknown.
	FNumber, FocalLength float64
	// ExposureTime in seconds, as "1/250" or "2".
	ExposureTime string
	ISO          int
	// Latitude and Longitude in degrees; nil if unknown.
	Latitude, Longitude *float64
}

// PhotoMonth counts the photos of a month, for the scrubber of a timeline.
type PhotoMonth struct {
	Year, Month, Count int
}
