package main

import (
	"log"
	"os"

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

	// 3. Initialize tsnet Server
	if err := os.MkdirAll("./ts-state", 0700); err != nil {
		log.Fatalf("Failed to create ts-state directory: %v", err)
	}

	s := &tsnet.Server{
		Hostname: "fastnas",
		AuthKey:  cfg.TSAuthKey,
		Dir:      "./ts-state",
		Logf:     log.Printf,
	}

	if err := s.Start(); err != nil {
		log.Fatalf("Failed to start tsnet server: %v", err)
	}
	defer s.Close()

	// 4. Create Listener
	ln, err := s.Listen("tcp", ":80")
	if err != nil {
		log.Fatalf("Failed to create listener: %v", err)
	}

	// 5. Setup Fiber
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		BodyLimit:             50 * 1024 * 1024 * 1024, // 50GB
	})
	app.Use(logger.New())

	// Routes
	app.Post("/upload", handlers.UploadHandler)
	app.Get("/files", handlers.ListFilesHandler)
	app.Get("/download/:uuid", handlers.DownloadHandler)
	app.Delete("/files/:uuid", handlers.DeleteFileHandler)
	app.Get("/thumbnail/:uuid", handlers.ThumbnailHandler)

	// Serve Frontend
	app.Use("/", filesystem.New(filesystem.Config{
		Root: web.GetFileSystem(),
	}))

	// Start local listener for debugging/local access
	go func() {
		log.Println("Also available locally at http://localhost:8080")
		if err := app.Listen(":8080"); err != nil {
			log.Printf("Failed to start local listener: %v", err)
		}
	}()

	log.Println("FastNAS is running on Tailscale (fastnas:80)...")
	if err := app.Listener(ln); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
