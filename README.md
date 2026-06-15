# FastNAS

FastNAS is a **single-binary, private cloud storage system** that runs over a private **Tailscale** network. It is designed to be zero-config, secure by default, and resource-efficient through streaming I/O.

![FastNAS Architecture](https://mermaid.ink/img/pako:eNpVkE1rwzAMhv9K0CkF-wN7G2w7DLbTDrfD2kNRbCe1sUXGSlso_e8z2Q6GXYT0eT8S6Q20wQkUeO-18ybslILzU2NPy3mnK_T0-bXQG_S8_dBo1sJ4d4-vj2L6_f2aL3fYX_0jHNFh_8q-8R1qLHDmUAsbe7jAwx4qjDDAw6dCj5O1sI_WwQ0eOqywxxsc4KFBhT00eOiwwgEaVHDQ4AENKjzAw6dCj3-shX38O7jB_x1W2OMKj2hQ4RENHjqscECDCg4aPKBBhQd4-FTo8Y-1sI9_Bzd46LDCCh9whEc0qPCIgwYrdMiggg0aPKBBhQd4-HTo8Y-1sI9_Bzd4RLDCCg9whEc0qPCIgwYrdMiggg0aPKBBhQd4-FTo8Y-1sI8WwQ0eEaywwgMc4RENDjrooMEKHTKoYIMGD2hQ4QEePhV6_GMt7KNFcINHBcussMIHHOERDSo84qDBCj3+AU21q8A?type=png)

> **Goal**: Create a Dropstyle-like file sharing service that lives entirely on your private VPN, accessible from anywhere without exposing public ports.

## 🚀 Key Features

*   **Zero Configuration**: Runs with a single environment variable (`TS_AUTH_KEY`). No port forwarding or firewall rules needed.
*   **Private Networking**: Embedded **Tailscale** node (`tsnet`) ensures the service is only accessible to devices in your tailnet.
*   **Per-User Isolation**: Files are owned by the caller's Tailscale identity (resolved via `WhoIs`); users only see and manage their own files.
*   **Resumable, Chunked Uploads**: Multi-file drag-&-drop with per-file progress bars; interrupted uploads resume from the last received byte — even across server restarts. No RAM buffering, so massive files work on low-memory devices (e.g., Raspberry Pi).
*   **Folders, Search & Pagination**: Organize files into folders; server-side filename search and paginated listings scale past thousands of files.
*   **Sharing**: Generate tokenized, optionally time-limited public links to individual files.
*   **Trash**: Deletes are recoverable; files sit in the trash until restored or permanently purged.
*   **Integrity**: A SHA-256 checksum is computed and stored for every file; per-user quota and free-disk-space guards protect storage.
*   **Single Binary**: The Frontend (HTML/JS) and Database logic are compiled into a single executable, with previews for images, video, audio, PDF, and text/code/Markdown.

---

## 🛠 Tech Stack

*   **Language**: Go (Latest Stable)
*   **Web Framework**: [Fiber](https://gofiber.io/) (Express-inspired, zero allocation)
*   **Networking**: [tsnet](https://tailscale.com/kb/1244/tsnet/) (Userspace Tailscale networking)
*   **Database**: SQLite (via `modernc.org/sqlite` pure-Go driver) + [GORM](https://gorm.io/)
*   **Frontend**: Vanilla HTML/JS + TailwindCSS (Served via `embed.FS`)

---

## 🏗 System Design & Architecture (Interview Prep)

If you are using this project for a technical interview, here are the key architectural decisions and trade-offs made:

### 1. Embedded Private Networking (`tsnet`)
Instead of running the app behind a standard reverse proxy (Nginx) and exposing port 80/443, we embed the VPN node **directly** into the application using `tailscale.com/tsnet`.
*   **Why?**
    *   **Security**: The app listens on a userspace networking interface. The host machine's ports remain closed to the public internet.
    *   **Portability**: The binary carries its own network identity. You can move it to a different machine, and it keeps the same DNS name (`http://fastnas`).
    *   **NAT Traversal**: Leveraging Tailscale's DERP servers and STUN to punch through NATs seamlessly.

### 2. Streaming vs. Buffering (Memory Management)
A naive implementation reads the entire uploaded file into memory (`[]byte`) before writing to disk. This causes OOM (Out of Memory) crashes on large files (e.g., uploading a 4GB movie on a 512MB RAM VM).
*   **Our Approach**:
    *   **Upload**: We use `io.Copy` to stream the `MultipartReader` directly to an `os.File`.
    *   **Download**: We use `ctx.SendFile` (or `io.Copy` to ResponseWriter), effectively using `sendfile(2)` syscalls where possible for zero-copy networking.

### 3. Pure Go SQLite (CGO vs. Pure Go)
Initially, we used the standard `go-sqlite3` driver which requires CGO (compiling C code). This breaks cross-compilation (building for Linux on Windows) and requires `gcc` on the build machine.
*   **Solution**: Switched to `github.com/glebarez/sqlite` (Pure Go).
*   **Trade-off**: Slightly lower performance than the C-bound driver, but massively improved build portability.

### 4. Single Binary Deployment
We use Go's `//go:embed` to compile `index.html` and static assets into the binary itself.
*   **Benefit**: Deployment is just `scp fastnas` and run. No "assets folder missing" errors.

---

## 🚦 Getting Started

### Prerequisites
*   A [Tailscale](https://tailscale.com/) account.
*   Go 1.25+ installed.

### Build
```powershell
go build -o fastnas.exe ./cmd/server
```

### Run
1.  **Generate Auth Key**: Go to [Tailscale Admin Console](https://login.tailscale.com/admin/settings/keys) -> Create Auth Key (Reusable, Ephemeral recommended for testing).
2.  **Set Environment**:
    ```powershell
    $env:TS_AUTH_KEY = "tskey-auth-..."
    ```
3.  **Start Server**:
    ```powershell
    ./fastnas.exe
    ```
4.  **Access**:
    *   **Via VPN**: `http://fastnas/` (from any device on your Tailscale network).
    *   **Locally**: `http://localhost:8080/` (for debugging on the host machine).

### Configuration

All configuration is via environment variables:

| Variable | Default | Description |
| :--- | :--- | :--- |
| `TS_AUTH_KEY` | *(empty)* | Tailscale auth key. Without it the node won't authenticate and all requests fall back to the local identity. |
| `DATA_DIR` | `./data` | Directory for the SQLite DB, uploaded files, thumbnails, and upload temp files. |
| `TS_HOSTNAME` | `fastnas` | Tailscale node hostname (and the DNS name it's reachable at). |
| `MAX_UPLOAD_SIZE` | `53687091200` (50 GB) | Max request body in bytes for the legacy single-shot `/upload` (resumable uploads are chunked and unaffected). |
| `LOCAL_ADDR` | `:8080` | Local debug listener address. Set empty to disable the local listener entirely. |
| `USER_QUOTA` | `0` (unlimited) | Max total bytes a single user may store (counts trashed files). Uploads that would exceed it are rejected with `403`. |
| `MIN_FREE_BYTES` | `0` | Refuse an upload unless this many bytes would remain free afterwards. Uploads that don't fit are rejected with `507`. |

---

## 🚢 Deployment

### Docker
```bash
docker build -t fastnas .
docker run -d --name fastnas \
  -e TS_AUTH_KEY=tskey-auth-... \
  -v fastnas-data:/var/lib/fastnas \
  -p 8080:8080 \
  fastnas
```
The Tailscale interface needs no published host ports; `-p 8080:8080` only exposes the optional local listener.

### systemd
A hardened unit is provided at [`deploy/fastnas.service`](deploy/fastnas.service):
```bash
sudo install -m 0755 fastnas /usr/local/bin/fastnas
echo 'TS_AUTH_KEY=tskey-auth-...' | sudo install -m 0600 /dev/stdin /etc/fastnas.env
sudo cp deploy/fastnas.service /etc/systemd/system/
sudo systemctl enable --now fastnas
```

A `GET /healthz` endpoint (returns `{"status":"ok"}`, or `503` if the DB is unreachable) is available for liveness/readiness probes. The server shuts down gracefully on `SIGINT`/`SIGTERM`.

---

## 🔌 API Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `GET` | `/healthz` | Liveness probe (checks DB connectivity). |
| `GET` | `/me` | Current resolved Tailscale identity. |
| `GET` | `/stats` | Aggregate file count and bytes used for the caller. |
| `GET` | `/files` | Paginated listing of the caller's folder/search results. Params: `folder`, `q`, `page`, `limit`. |
| `POST` | `/upload` | Legacy single-shot multipart upload (streams to disk). |
| `POST` | `/uploads` | Start a resumable, chunked upload session. |
| `PATCH` | `/uploads/:id` | Append a chunk at the `Upload-Offset` header position. |
| `POST` | `/uploads/:id/complete` | Finalize a completed upload session. |
| `GET` | `/download/:uuid` | Stream a file (`?preview=true` for inline). |
| `PATCH` | `/files/:uuid` | Rename and/or move a file. |
| `DELETE` | `/files/:uuid` | Move a file to the trash (soft delete). |
| `GET` | `/trash` | List the caller's trashed files. |
| `POST` | `/files/:uuid/restore` | Restore a file from the trash. |
| `DELETE` | `/trash/:uuid` | Permanently delete one trashed file. |
| `DELETE` | `/trash` | Empty the trash (permanent). |
| `GET`/`POST` | `/folders` | List all folders / create a folder. |
| `PATCH`/`DELETE` | `/folders/:uuid` | Rename+move / recursively delete a folder. |
| `POST`/`GET` | `/files/:uuid/shares` | Create / list share links for a file. |
| `GET` | `/shares/:token` | Public, token-gated access to a shared file. |
| `DELETE` | `/shares/:token` | Revoke a share link. |

All file/folder endpoints are scoped to the caller's Tailscale identity.

---

## 🔮 Future Improvements (Talking Points)
*   **S3 Backend**: Replace local disk storage with S3 interface for infinite scalability.
*   **Background thumbnailing**: Pre-generate previews via a worker instead of on first request.
*   **Content de-duplication**: SHA-256 checksums are already stored per file — collapse identical uploads onto shared storage.
*   **OIDC / SSO**: Layer an identity provider on top of the Tailscale identity for non-tailnet access.
