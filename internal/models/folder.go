package models

import (
	"time"
)

// Folder represents a directory in a user's file tree. Parent/child links are
// expressed by UUID so the rest of the API can reference folders the same way
// it references files. A nil ParentID means the folder sits at the root.
type Folder struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	UUID      string    `gorm:"uniqueIndex;not null"`
	Name      string    `gorm:"not null"`
	OwnerID   string    `gorm:"index;not null" json:"-"`
	ParentID  *string   `gorm:"index"` // UUID of the parent folder; nil = root
	CreatedAt time.Time `gorm:"autoCreateTime"`
}
