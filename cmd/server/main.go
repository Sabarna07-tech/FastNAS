package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fastnas/fastnas/internal/auth"
	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/handlers"
	"github.com/fastnas/fastnas/web"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"tailscale.com/tsnet"
)

func main() {
	// 1. Load Config
	cfg := config.Load()

	// 2. Initialize Database
	if err := database.Init(cfg); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Inject config into handlers and pre-create working directories so they
	// aren't created lazily (and racily) on the first request.
	handlers.Configure(cfg)
	for _, dir := range []string{
		filepath.Join(cfg.DataDir, "thumbs"),
		filepath.Join(cfg.DataDir, "uploads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create %s: %v", dir, err)
		}
	}

	// 3. Initialize tsnet Server
	if err := os.MkdirAll("./ts-state", 0700); err != nil {
		log.Fatalf("Failed to create ts-state directory: %v", err)
	}

	s := &tsnet.Server{
		Hostname: cfg.Hostname,
		AuthKey:  cfg.TSAuthKey,
		Dir:      "./ts-state",
		Logf:     log.Printf,
	}

	if err := s.Start(); err != nil {
		log.Fatalf("Failed to start tsnet server: %v", err)
	}

	// 4. Create Listener
	ln, err := s.Listen("tcp", ":80")
	if err != nil {
		log.Fatalf("Failed to create listener: %v", err)
	}

	// LocalClient is used to resolve the Tailscale identity behind each request.
	lc, err := s.LocalClient()
	if err != nil {
		log.Fatalf("Failed to get Tailscale local client: %v", err)
	}

	// 5. Setup Fiber
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		BodyLimit:             cfg.BodyLimit,
	})
	app.Use(logger.New())

	// Health check (no identity resolution needed).
	app.Get("/healthz", handlers.HealthHandler)

	// Resolve the caller's Tailscale identity for every request.
	app.Use(auth.New(lc))

	// Routes
	app.Get("/me", handlers.MeHandler)
	app.Get("/stats", handlers.StatsHandler)
	app.Post("/upload", handlers.UploadHandler)
	app.Get("/files", handlers.ListFilesHandler)
	app.Get("/download/:uuid", handlers.DownloadHandler)
	app.Patch("/files/:uuid", handlers.UpdateFileHandler)
	app.Delete("/files/:uuid", handlers.DeleteFileHandler)
	app.Get("/thumbnail/:uuid", handlers.ThumbnailHandler)

	// Trash (soft delete)
	app.Get("/trash", handlers.TrashListHandler)
	app.Post("/files/:uuid/restore", handlers.RestoreFileHandler)
	app.Delete("/trash/:uuid", handlers.PermanentDeleteHandler)
	app.Delete("/trash", handlers.EmptyTrashHandler)

	app.Get("/folders", handlers.ListAllFoldersHandler)
	app.Post("/folders", handlers.CreateFolderHandler)
	app.Patch("/folders/:uuid", handlers.UpdateFolderHandler)
	app.Delete("/folders/:uuid", handlers.DeleteFolderHandler)

	// Share links
	app.Post("/files/:uuid/shares", handlers.CreateShareHandler)
	app.Get("/files/:uuid/shares", handlers.ListSharesHandler)
	app.Delete("/shares/:token", handlers.DeleteShareHandler)
	app.Get("/shares/:token", handlers.AccessShareHandler)

	// Resumable, chunked uploads
	app.Post("/uploads", handlers.CreateUploadHandler)
	app.Get("/uploads/:id", handlers.UploadStatusHandler)
	app.Patch("/uploads/:id", handlers.UploadChunkHandler)
	app.Post("/uploads/:id/complete", handlers.CompleteUploadHandler)
	app.Delete("/uploads/:id", handlers.AbortUploadHandler)

	// Serve Frontend
	app.Use("/", filesystem.New(filesystem.Config{
		Root: web.GetFileSystem(),
	}))

	// Janitor: drop interrupted upload sessions (and their temp files) that
	// have been idle for over a day.
	go func() {
		const maxAge = 24 * time.Hour
		handlers.CleanupStaleUploads(maxAge)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			handlers.CleanupStaleUploads(maxAge)
		}
	}()

	// Serve on the Tailscale listener.
	go func() {
		log.Printf("FastNAS is running on Tailscale (%s:80)...", cfg.Hostname)
		if err := app.Listener(ln); err != nil {
			log.Printf("Tailscale listener stopped: %v", err)
		}
	}()

	// Optionally serve a local listener for debugging/local access.
	if cfg.LocalAddr != "" {
		go func() {
			log.Printf("Also available locally at http://localhost%s", cfg.LocalAddr)
			if err := app.Listen(cfg.LocalAddr); err != nil {
				log.Printf("Failed to start local listener: %v", err)
			}
		}()
	}

	// Block until we receive a shutdown signal, then clean up gracefully.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		log.Printf("Error during HTTP shutdown: %v", err)
	}
	if err := s.Close(); err != nil {
		log.Printf("Error closing Tailscale server: %v", err)
	}
	if err := database.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}
	log.Println("Shutdown complete.")
}
