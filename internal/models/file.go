package models

import (
	"time"

	"gorm.io/gorm"
)

type File struct {
	ID         uint           `gorm:"primaryKey"`
	UUID       string         `gorm:"uniqueIndex;not null"`
	Filename   string         `gorm:"not null"`
	Size       int64          `gorm:"not null"`
	MimeType   string         `gorm:"not null"`
	DiskPath   string         `gorm:"not null" json:"-"` // internal path; never exposed to clients
	OwnerID    string         `gorm:"index;not null"`    // stable Tailscale user id (or "local")
	OwnerEmail string         `gorm:"not null"`          // Tailscale login name, for display
	FolderID   *string        `gorm:"index"`             // UUID of containing folder; nil = root
	Checksum   string         `gorm:"index"`             // SHA-256 of the contents (hex)
	CreatedAt  time.Time      `gorm:"autoCreateTime"`
	DeletedAt  gorm.DeletedAt `gorm:"index"` // set when the file is in the trash (soft delete)
}
