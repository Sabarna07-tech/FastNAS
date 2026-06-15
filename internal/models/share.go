package models

import (
	"time"
)

// Share is a tokenized, optionally time-limited public link to a single file.
// Anyone who can reach the server and holds the (unguessable) token can fetch
// the file via /shares/:token without being the owner.
type Share struct {
	ID        uint       `gorm:"primaryKey" json:"-"`
	Token     string     `gorm:"uniqueIndex;not null" json:"token"`
	FileUUID  string     `gorm:"index;not null" json:"fileUuid"`
	OwnerID   string     `gorm:"index;not null" json:"-"`
	ExpiresAt *time.Time `json:"expiresAt"` // nil = never expires
	CreatedAt time.Time  `gorm:"autoCreateTime" json:"createdAt"`
}
