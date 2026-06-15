package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/gofiber/fiber/v2"
)

// setupApp spins up an isolated app backed by a temp-dir SQLite DB and returns
// a Fiber app with all routes registered. Auth middleware is omitted, so every
// request resolves to auth.LocalIdentity (owner "local").
func setupApp(t *testing.T) *fiber.App {
	t.Helper()
	dir, err := os.MkdirTemp("", "fastnas-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	cfg := &config.Config{DataDir: dir, BodyLimit: 100 * 1024 * 1024}
	if err := database.Init(cfg); err != nil {
		t.Fatalf("database.Init: %v", err)
	}
	// Close the DB, then remove the temp dir. RemoveAll's error is ignored
	// because fasthttp's SendFile may still hold a file handle open briefly on
	// Windows; on Linux (CI) this removes cleanly.
	t.Cleanup(func() {
		_ = database.Close()
		_ = os.RemoveAll(dir)
	})
	Configure(cfg)

	app := fiber.New(fiber.Config{DisableStartupMessage: true, BodyLimit: cfg.BodyLimit})
	app.Get("/files", ListFilesHandler)
	app.Post("/upload", UploadHandler)
	app.Get("/download/:uuid", DownloadHandler)
	app.Patch("/files/:uuid", UpdateFileHandler)
	app.Delete("/files/:uuid", DeleteFileHandler)
	app.Get("/trash", TrashListHandler)
	app.Post("/files/:uuid/restore", RestoreFileHandler)
	app.Delete("/trash/:uuid", PermanentDeleteHandler)
	app.Delete("/trash", EmptyTrashHandler)
	app.Get("/folders", ListAllFoldersHandler)
	app.Post("/folders", CreateFolderHandler)
	app.Patch("/folders/:uuid", UpdateFolderHandler)
	app.Delete("/folders/:uuid", DeleteFolderHandler)
	app.Post("/files/:uuid/shares", CreateShareHandler)
	app.Get("/files/:uuid/shares", ListSharesHandler)
	app.Delete("/shares/:token", DeleteShareHandler)
	app.Get("/shares/:token", AccessShareHandler)
	app.Post("/uploads", CreateUploadHandler)
	app.Get("/uploads/:id", UploadStatusHandler)
	app.Patch("/uploads/:id", UploadChunkHandler)
	app.Post("/uploads/:id/complete", CompleteUploadHandler)
	app.Delete("/uploads/:id", AbortUploadHandler)
	return app
}

func do(t *testing.T, app *fiber.App, req *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v interface{}) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// uploadFile performs a legacy multipart upload and returns the created File.
func uploadFile(t *testing.T, app *fiber.App, name, folder string, content []byte) models.File {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if folder != "" {
		_ = w.WriteField("folder", folder)
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	fw.Write(content)
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}
	var f models.File
	decode(t, resp, &f)
	return f
}

func TestUploadListDownload(t *testing.T) {
	app := setupApp(t)
	content := []byte("hello fastnas")
	f := uploadFile(t, app, "hello.txt", "", content)

	if f.UUID == "" || f.Checksum == "" {
		t.Fatalf("expected UUID and checksum, got %+v", f)
	}

	// Listing includes it.
	resp := do(t, app, httptest.NewRequest(http.MethodGet, "/files", nil))
	var listing listingResponse
	decode(t, resp, &listing)
	if listing.Total != 1 || len(listing.Files) != 1 {
		t.Fatalf("listing total=%d files=%d, want 1/1", listing.Total, len(listing.Files))
	}

	// Download returns the bytes.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/download/"+f.UUID, nil))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(body, content) {
		t.Fatalf("download body = %q, want %q", body, content)
	}
}

func TestSoftDeleteRestoreAndPurge(t *testing.T) {
	app := setupApp(t)
	f := uploadFile(t, app, "doc.txt", "", []byte("data"))

	// Delete -> moves to trash.
	resp := do(t, app, httptest.NewRequest(http.MethodDelete, "/files/"+f.UUID, nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	// Not in normal listing.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/files", nil))
	var listing listingResponse
	decode(t, resp, &listing)
	if listing.Total != 0 {
		t.Fatalf("after delete, listing total = %d, want 0", listing.Total)
	}

	// Present in trash.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/trash", nil))
	var trash []models.File
	decode(t, resp, &trash)
	if len(trash) != 1 {
		t.Fatalf("trash len = %d, want 1", len(trash))
	}

	// Restore brings it back.
	resp = do(t, app, httptest.NewRequest(http.MethodPost, "/files/"+f.UUID+"/restore", nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("restore status = %d", resp.StatusCode)
	}
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/files", nil))
	decode(t, resp, &listing)
	if listing.Total != 1 {
		t.Fatalf("after restore, listing total = %d, want 1", listing.Total)
	}

	// Delete again, then empty trash.
	do(t, app, httptest.NewRequest(http.MethodDelete, "/files/"+f.UUID, nil))
	resp = do(t, app, httptest.NewRequest(http.MethodDelete, "/trash", nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("empty trash status = %d", resp.StatusCode)
	}
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/trash", nil))
	decode(t, resp, &trash)
	if len(trash) != 0 {
		t.Fatalf("after empty, trash len = %d, want 0", len(trash))
	}
}

func TestFoldersRenameMove(t *testing.T) {
	app := setupApp(t)

	// Create a folder.
	body := bytes.NewBufferString(`{"name":"Docs"}`)
	req := httptest.NewRequest(http.MethodPost, "/folders", body)
	req.Header.Set("Content-Type", "application/json")
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("create folder status = %d", resp.StatusCode)
	}
	var folder models.Folder
	decode(t, resp, &folder)

	// Upload a file at root, then move it into the folder + rename it.
	f := uploadFile(t, app, "a.txt", "", []byte("x"))
	patch := bytes.NewBufferString(`{"filename":"b.txt","folder":"` + folder.UUID + `"}`)
	req = httptest.NewRequest(http.MethodPatch, "/files/"+f.UUID, patch)
	req.Header.Set("Content-Type", "application/json")
	resp = do(t, app, req)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("update file status = %d", resp.StatusCode)
	}
	var updated models.File
	decode(t, resp, &updated)
	if updated.Filename != "b.txt" || updated.FolderID == nil || *updated.FolderID != folder.UUID {
		t.Fatalf("rename/move failed: %+v", updated)
	}

	// Root listing should now be empty of files but show the folder.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/files", nil))
	var root listingResponse
	decode(t, resp, &root)
	if root.Total != 0 || len(root.Folders) != 1 {
		t.Fatalf("root listing total=%d folders=%d, want 0/1", root.Total, len(root.Folders))
	}

	// Listing inside the folder should show the file.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/files?folder="+folder.UUID, nil))
	var inside listingResponse
	decode(t, resp, &inside)
	if inside.Total != 1 {
		t.Fatalf("folder listing total = %d, want 1", inside.Total)
	}
}

func TestFolderMoveCycleRejected(t *testing.T) {
	app := setupApp(t)
	mk := func(name, parent string) models.Folder {
		b := bytes.NewBufferString(`{"name":"` + name + `","parent":"` + parent + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/folders", b)
		req.Header.Set("Content-Type", "application/json")
		resp := do(t, app, req)
		var f models.Folder
		decode(t, resp, &f)
		return f
	}
	parent := mk("parent", "")
	child := mk("child", parent.UUID)

	// Moving parent into its own child must be rejected.
	b := bytes.NewBufferString(`{"parent":"` + child.UUID + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/folders/"+parent.UUID, b)
	req.Header.Set("Content-Type", "application/json")
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("cyclic move status = %d, want 400", resp.StatusCode)
	}
}

func TestShareLink(t *testing.T) {
	app := setupApp(t)
	content := []byte("shared content")
	f := uploadFile(t, app, "share.txt", "", content)

	// Create a share.
	req := httptest.NewRequest(http.MethodPost, "/files/"+f.UUID+"/shares", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusCreated {
		t.Fatalf("create share status = %d, want 201", resp.StatusCode)
	}
	var share models.Share
	decode(t, resp, &share)
	if share.Token == "" {
		t.Fatal("expected a share token")
	}

	// Public access returns the bytes.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/shares/"+share.Token, nil))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("share access status=%d body=%q", resp.StatusCode, body)
	}

	// Revoke -> access now 404.
	resp = do(t, app, httptest.NewRequest(http.MethodDelete, "/shares/"+share.Token, nil))
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", resp.StatusCode)
	}
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/shares/"+share.Token, nil))
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("post-revoke access status = %d, want 404", resp.StatusCode)
	}
}

func TestResumableUpload(t *testing.T) {
	app := setupApp(t)
	content := []byte("0123456789abcdefghij") // 20 bytes
	const chunk = 7

	// Create session.
	create := `{"filename":"big.bin","size":` + strconv.Itoa(len(content)) + `,"mimeType":"application/octet-stream"}`
	req := httptest.NewRequest(http.MethodPost, "/uploads", bytes.NewBufferString(create))
	req.Header.Set("Content-Type", "application/json")
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusCreated {
		t.Fatalf("create upload status = %d, want 201", resp.StatusCode)
	}
	var session models.UploadSession
	decode(t, resp, &session)

	// Send chunks.
	for off := 0; off < len(content); off += chunk {
		end := off + chunk
		if end > len(content) {
			end = len(content)
		}
		req = httptest.NewRequest(http.MethodPatch, "/uploads/"+session.UUID, bytes.NewReader(content[off:end]))
		req.Header.Set("Upload-Offset", strconv.Itoa(off))
		resp = do(t, app, req)
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("chunk at %d status = %d, want 200", off, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// A stale-offset chunk should be rejected with 409.
	req = httptest.NewRequest(http.MethodPatch, "/uploads/"+session.UUID, bytes.NewReader([]byte("x")))
	req.Header.Set("Upload-Offset", "0")
	resp = do(t, app, req)
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("stale-offset status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Complete.
	resp = do(t, app, httptest.NewRequest(http.MethodPost, "/uploads/"+session.UUID+"/complete", nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("complete status = %d, want 200", resp.StatusCode)
	}
	var file models.File
	decode(t, resp, &file)
	if file.Size != int64(len(content)) {
		t.Fatalf("completed size = %d, want %d", file.Size, len(content))
	}

	// Downloaded content matches what we uploaded in chunks.
	resp = do(t, app, httptest.NewRequest(http.MethodGet, "/download/"+file.UUID, nil))
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("download = %q, want %q", got, content)
	}
}

func TestQuotaEnforced(t *testing.T) {
	app := setupApp(t)
	// Tighten the quota for this test.
	appConfig.UserQuota = 10
	defer func() { appConfig.UserQuota = 0 }()

	// 6 bytes is fine.
	uploadFile(t, app, "small.txt", "", []byte("123456"))

	// Another 6 bytes would exceed the 10-byte quota.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "over.txt")
	fw.Write([]byte("789012"))
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp := do(t, app, req)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("over-quota upload status = %d, want 403", resp.StatusCode)
	}
}
