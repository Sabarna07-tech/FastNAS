package models

import (
	"time"
)

type File struct {
	ID        uint      `gorm:"primaryKey"`
	UUID      string    `gorm:"uniqueIndex;not null"`
	Filename  string    `gorm:"not null"`
	Size      int64     `gorm:"not null"`
	MimeType  string    `gorm:"not null"`
	DiskPath  string    `gorm:"not null"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}
