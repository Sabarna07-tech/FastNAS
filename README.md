# FastNAS

FastNAS is a **single-binary, private cloud storage system** that runs over a private **Tailscale** network. It is designed to be zero-config, secure by default, and resource-efficient through streaming I/O.

![FastNAS Architecture](https://mermaid.ink/img/pako:eNpVkE1rwzAMhv9K0CkF-wN7G2w7DLbTDrfD2kNRbCe1sUXGSlso_e8z2Q6GXYT0eT8S6Q20wQkUeO-18ybslILzU2NPy3mnK_T0-bXQG_S8_dBo1sJ4d4-vj2L6_f2aL3fYX_0jHNFh_8q-8R1qLHDmUAsbe7jAwx4qjDDAw6dCj5O1sI_WwQ0eOqywxxsc4KFBhT00eOiwwgEaVHDQ4AENKjzAw6dCj3-shX38O7jB_x1W2OMKj2hQ4RENHjqscECDCg4aPKBBhQd4-FTo8Y-1sI9_Bzd46LDCCh9whEc0qPCIgwYrdMiggg0aPKBBhQd4-HTo8Y-1sI9_Bzd4RLDCCg9whEc0qPCIgwYrdMiggg0aPKBBhQd4-FTo8Y-1sI8WwQ0eEaywwgMc4RENDjrooMEKHTKoYIMGD2hQ4QEePhV6_GMt7KNFcINHBcussMIHHOERDSo84qDBCj3+AU21q8A?type=png)

> **Goal**: Create a Dropstyle-like file sharing service that lives entirely on your private VPN, accessible from anywhere without exposing public ports.

## 🚀 Key Features

*   **Zero Configuration**: Runs with a single environment variable (`TS_AUTH_KEY`). No port forwarding or firewall rules needed.
*   **Private Networking**: Embedded **Tailscale** node (`tsnet`) ensures the service is only accessible to devices in your tailnet.
*   **Streaming I/O**: Files are streamed directly from Request Body $\to$ Disk. No RAM buffering, allowing upload/download of massive files on low-memory devices (e.g., Raspberry Pi).
*   **Single Binary**: The Frontend (HTML/JS) and Database logic are compiled into a single executable.
*   **Metadata Search**: SQLite database stores file metadata for quick listing and retrieval.

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

---

## 🔌 API Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/upload` | Multipart form upload. Streams to disk. Returns JSON metadata. |
| `GET` | `/files` | Returns JSON list of all files, sorted by newest. |
| `GET` | `/download/:uuid` | Streams file content with correct Content-Disposition. |

---

## 🔮 Future Improvements (Talking Points)
*   **Chunked Uploads**: For unstable connections, break files into chunks and reassemble.
*   **S3 Backend**: Replace local disk storage with S3 interface for infinite scalability.
*   **Authentication**: Add valid OIDC login for user-specific file isolation.
*   **Image Previews**: Generate thumbnails for uploaded images using a background worker.
