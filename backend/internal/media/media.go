// Package media defines storage primitives whose HTTP-facing contract streams
// readers. The current PostgreSQL adapter remains compatible with the bytea schema;
// a future object-store adapter can implement the same interface without changing
// handlers.
package media

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrInvalidObject = errors.New("invalid media object")
	ErrSizeMismatch  = errors.New("media size mismatch")
	ErrTooLarge      = errors.New("media object too large")
	ErrQuotaExceeded = errors.New("unattached media quota exceeded")
	ErrUploadBusy    = errors.New("another media upload is already running")
)

type Metadata struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"ownerId"`
	Filename  string    `json:"filename"`
	AltText   string    `json:"altText"`
	MIMEType  string    `json:"mimeType"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CreatedAt time.Time `json:"createdAt"`
}

type PutObject struct {
	Metadata Metadata
	Body     io.Reader
}

type Object struct {
	Metadata Metadata
	Body     io.ReadSeekCloser
}

type CleanupResult struct {
	Deleted      int64
	DeletedBytes int64
}

type MediaStore interface {
	Put(context.Context, PutObject) (Metadata, error)
	Open(context.Context, string, string) (Object, error)
	DeleteOrphans(context.Context, time.Time, int) (CleanupResult, error)
}
