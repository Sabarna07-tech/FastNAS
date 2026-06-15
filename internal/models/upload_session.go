package models

import (
	"time"
)

// UploadSession tracks an in-progress resumable, chunked upload. The bytes are
// streamed to TempPath and Uploaded records how many have been received, so an
// interrupted upload can be resumed (even across server restarts) by querying
// the current offset and continuing from there.
type UploadSession struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	UUID      string    `gorm:"uniqueIndex;not null" json:"id"`
	OwnerID   string    `gorm:"index;not null" json:"-"`
	Filename  string    `gorm:"not null" json:"filename"`
	Size      int64     `gorm:"not null" json:"size"`     // total expected bytes
	MimeType  string    `json:"mimeType"`                 //
	FolderID  *string   `json:"-"`                        // destination folder (nil = root)
	Uploaded  int64     `gorm:"not null" json:"uploaded"` // bytes received so far
	TempPath  string    `gorm:"not null" json:"-"`        // in-progress file on disk
	CreatedAt time.Time `gorm:"autoCreateTime" json:"-"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"-"`
}
