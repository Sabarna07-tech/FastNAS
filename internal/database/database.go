package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Init(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "fastnas.db")

	// "Record not found" is expected control flow here (it drives 404s), so we
	// don't want it logged as an error.
	gormLogger := logger.New(log.New(os.Stderr, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
	})

	var err error
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	if err := DB.AutoMigrate(&models.File{}, &models.Folder{}, &models.UploadSession{}, &models.Share{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}

// Ping verifies the database connection is alive.
func Ping() error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// Close releases the underlying database connection.
func Close() error {
	if DB == nil {
		return nil
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
