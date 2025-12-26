package database

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

func Init(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "fastnas.db")

	var err error
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	if err := DB.AutoMigrate(&models.File{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}
