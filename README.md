<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.svg"><img src="docs/logo.svg" alt="ServeIt" height="44"></picture></p>

**A single-binary LAN file server with searchable browsing and expiring guest links for exactly the files and folders you select.**

[![Latest release](https://img.shields.io/github/v/release/Danakolana/ServeIt?display_name=tag)](https://github.com/Danakolana/ServeIt/releases/latest)
[![Release build](https://github.com/Danakolana/ServeIt/actions/workflows/release.yml/badge.svg)](https://github.com/Danakolana/ServeIt/actions/workflows/release.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go)](https://go.dev/)

![ServeIt startup panel and file browser](docs/startup-and-browser.png)

![ServeIt startup console QR and partial sharing workflow](docs/demo.gif)

The demo deliberately selects only **3 files and 1 folder** from a larger list. Unchecked siblings stay outside the temporary guest link.

ServeIt also prints a scannable LAN QR code in the console at startup, so another device can open the browser immediately.

ServeIt turns a folder into a clean web interface for devices on the same network. Browse and search the full served folder, or create a temporary guest link containing only selected files and directories. There is no runtime, package manager, account system, or installation directory.

The original Classic interface remains the default. Use the **Modern UI** link in the browser, admin page, or guest page to switch to the optional modern theme; ServeIt remembers the choice in that browser.

## Quick start

### Linux, 64-bit PC

```bash
curl -fL -o ServeIt https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-linux-amd64
chmod +x ServeIt
./ServeIt
```

### macOS, Apple silicon

```bash
curl -fL -o ServeIt https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-macos-arm64
chmod +x ServeIt
./ServeIt
```

Intel Mac users should replace `macos-arm64` with `macos-amd64`.

### Windows PowerShell, 64-bit

```powershell
Invoke-WebRequest -Uri "https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-windows-amd64.exe" -OutFile "ServeIt.exe"
.\ServeIt.exe
```

That is enough: running `./ServeIt` or `.\ServeIt.exe` with no arguments immediately serves the current folder. Use `--dir /another/path` only when you want to share a different folder.

ServeIt prints the address, QR code, authentication state, and admin credentials when it starts. Open the address from another device on the same Wi-Fi or Ethernet network.

> [!CAUTION]
> The main browser has no password by default. Hidden files are excluded unless `--show-hidden` is set. Point `--dir` at a dedicated sharing folder instead of your home directory or a source repository. See [Security model](#security-model).

## Latest binaries

Each [GitHub release](https://github.com/Danakolana/ServeIt/releases/latest) contains these static binaries and a `SHA256SUMS` file:

| Download | Platform |
|---|---|
| [ServeIt-linux-amd64](https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-linux-amd64) | Linux, Intel/AMD 64-bit |
| [ServeIt-linux-arm64](https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-linux-arm64) | Linux, ARM64 |
| [ServeIt-macos-amd64](https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-macos-amd64) | macOS, Intel |
| [ServeIt-macos-arm64](https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-macos-arm64) | macOS, Apple silicon |
| [ServeIt-windows-amd64.exe](https://github.com/Danakolana/ServeIt/releases/latest/download/ServeIt-windows-amd64.exe) | Windows, Intel/AMD 64-bit |
| [SHA256SUMS](https://github.com/Danakolana/ServeIt/releases/latest/download/SHA256SUMS) | SHA-256 checksums for all binaries |

To verify the Linux 64-bit download above:

```bash
curl -fLO https://github.com/Danakolana/ServeIt/releases/latest/download/SHA256SUMS
expected="$(awk '$2 == "ServeIt-linux-amd64" {print $1}' SHA256SUMS)"
printf '%s  ServeIt\n' "$expected" | sha256sum -c -
```

On macOS, use `shasum -a 256 -c -` in place of `sha256sum -c -` and select the checksum for your macOS asset.

## Highlights

- **Selective guest sharing.** Check individual files and folders in the admin page. A guest link cannot browse unchecked siblings or the rest of the served tree.
- **Expiring links.** Every link has an idle timeout and a hard maximum lifetime of 24 hours. View and download counts are visible to the administrator, and links can be revoked.
- **Streamed multi-file downloads.** Select several files or folders and receive one stored ZIP. File bytes are read as they are sent instead of buffering the entire archive in memory.
- **Recursive live search.** Search the served tree from the browser, with results capped at 200 matches.
- **One small binary.** Current release binaries are roughly 8–9 MB and contain the UI, search, authentication, QR generation, and server.
- **QR codes where they help.** Startup QR codes open the file browser; every guest link has its own QR code in the admin page.

![ServeIt admin panel](docs/admin.png)

![A selective guest link](docs/share.png)

## Run and configure

```bash
./ServeIt
./ServeIt --dir /home/user/files --port 9000
./ServeIt --dir /data --port 8888 --password guest --admin-password separate-secret
./ServeIt --listen 127.0.0.1 --show-hidden
```

On Windows, use `ServeIt.exe`; the flags are the same.

| Flag | Default | Purpose |
|---|---|---|
| `--dir` | `.` | Root directory exposed by the main browser. Guest links remain limited to their selected items. |
| `--port` | `8080` | TCP port. |
| `--listen` | `0.0.0.0` | IP address to bind. Use `127.0.0.1` for local-only access or `::` for an IPv6 listener. |
| `--password` | empty | Basic-auth password for the main browser and search. The username is `admin`. Empty means open access on the LAN. |
| `--password-file` | empty | Read the main-browser password from a file whose permissions do not allow group or other access. |
| `--admin-password` | generated | Separate password for `/admin`, with username `admin`. If omitted, ServeIt generates and prints a random password. |
| `--admin-password-file` | empty | Read the admin password from a restrictive file. |
| `--public-url` | auto-detected LAN URL | Canonical `http://` or `https://` origin used in guest links and QR codes. Set this when ServeIt is behind a reverse proxy. |
| `--allow-host` | local names and addresses | Additional Host name or IP accepted by the server. Repeat the flag for multiple names. |
| `--state-file` | per-user ServeIt state path | Store share metadata outside the served directory. Relative paths are resolved from the current working directory. `SERVEIT_STATE_FILE` provides the same setting. |
| `--show-hidden` | false | Include path segments beginning with `.` in browsing, search, shares, and downloads. |

The equivalent environment variables are `SERVEIT_PASSWORD` and `SERVEIT_ADMIN_PASSWORD`. A flag value takes precedence over its password file, which takes precedence over the environment variable. Password files must be readable only by the current user.

Startup QR codes prefer `192.168.x.x` addresses and may include private IPv6 addresses such as `fd00::/8`. Docker, VM, link-local, and other interface ranges are skipped to reduce the chance of scanning an unusable address. If no usable LAN address exists, ServeIt displays a loopback address for local access. Guest-link QR codes use `--public-url` when it is configured.

When `--public-url` is set, it is used instead of request headers, so a forged `Host` or `X-Forwarded-Proto` header cannot change generated links.

ServeIt rejects unexpected Host headers to reduce DNS-rebinding exposure. If you access it through a local DNS name, add that name with `--allow-host`. Binding to `127.0.0.1` keeps the server local to the machine.

Search walks are cancellable and limited to four concurrent filesystem walks, with results capped at 200 matches. ZIP downloads are capped at 100,000 entries or 10 GiB per request.

Press Ctrl+C once to stop accepting new requests while active transfers drain. Press it a second time to force-close active connections. Share state is flushed during shutdown; revoked and long-expired records are retained for up to 30 days before pruning.

## Share only part of a folder

1. Open `http://<lan-address>:<port>/admin` and sign in as `admin`.
2. Browse to the relevant directory and check the files or child folders to share.
3. Choose the idle timeout and maximum lifetime, then create the link.
4. Copy the URL or show its QR code. Revoke the link when it is no longer needed.

The main browser and guest links are separate access paths. `--password` protects the complete tree under `--dir`. A guest URL is a capability link that exposes only its recorded selection and does not require the main-browser password.

Good fits include:

- Sharing selected photos with family on the same Wi-Fi without publishing the rest of the photo library.
- Giving a colleague temporary read-only access to one project folder on a trusted office LAN.
- Distributing meeting, classroom, or workshop files without creating accounts or installing a cloud client.

## Why ServeIt?

ServeIt focuses on temporary, selective, read-only sharing. Other tools may be a better fit when you need uploads, WebDAV, permanent user accounts, or public Internet hosting.

| Tool | Setup and access model | Best fit |
|---|---|---|
| **ServeIt** | One binary; full local browser plus expiring links for selected files and folders | Temporary selective sharing on a trusted LAN |
| `python -m http.server` | Requires Python; exposes the selected root without guest-link isolation | A quick, unrestricted directory listing |
| `npx serve` | Requires Node/npm; serves a directory as a static site | Local web development and static-site previews |
| miniserve / dufs | Single-binary general file servers with broader transfer features, depending on the tool | Uploads, WebDAV, or general-purpose file serving |
| File Browser | Persistent service with users, permissions, uploads, and configuration | Ongoing browser-based file management |

## Security model

ServeIt is intended for a trusted local network. It is not hardened for direct exposure to the public Internet.

- **HTTP is unencrypted.** Basic authentication controls access but does not encrypt passwords, share URLs, or files in transit. Use an HTTPS reverse proxy or tunnel if the network is not trusted.
- **The main browser exposes the whole configured root.** With no `--password`, anyone who can reach the server can browse and download it. Hidden path segments are excluded by default; use `--show-hidden` only when that is intentional.
- **Guest links are the isolation boundary.** A guest can access only the files and folder descendants selected when the link was created. Anyone who obtains the URL can use it until it expires or is revoked.
- **Admin credentials matter.** ServeIt generates a separate admin password unless `--admin-password`, `--admin-password-file`, or `SERVEIT_ADMIN_PASSWORD` supplies one. The main-browser password never implicitly grants admin access.
- **Browser scripts use a restrictive CSP.** Embedded classic scripts receive a per-response nonce; external theme scripts are limited to same-origin assets.
- **Revocation affects new requests.** A transfer that has already started may finish sending bytes; revoking the link blocks subsequent requests.
- **Admin mutations use same-origin request checks.** Share creation and revocation require the admin request header and JSON content type, and reject a mismatched `Origin` when one is supplied.
- **Share state is reserved.** Temporary-link metadata is stored in a per-user ServeIt state file outside the served root by default. Use `--state-file` or `SERVEIT_STATE_FILE` to choose a location. Existing `.serveit-shares.json` and `.lanserve-shares.json` files under the served root are migrated on first use, while aliases to the active state file remain hidden from browsing, search, guest links, and ZIP downloads. Corrupt state is quarantined with a timestamped backup instead of being overwritten.

Security-default improvements are tracked in the [roadmap](BACKLOG.md). If you discover a vulnerability, avoid including secrets or private share URLs in a public issue.

## Troubleshooting

### Another device cannot connect

- Confirm both devices are on the same LAN and not separated by guest-Wi-Fi isolation, a VPN, or a VLAN.
- Allow ServeIt through the host firewall on private networks. Windows may show this prompt on first launch.
- Use the printed `192.168.x.x` URL. A `127.0.0.1` address works only on the computer running ServeIt.

### The expected QR code is missing

ServeIt prefers `192.168.0.0/16` and private IPv6 addresses for startup QR codes. Addresses belonging to Docker, virtual machines, link-local interfaces, and other ranges are skipped. If no usable LAN address is found, type the reachable address manually.

### macOS blocks the binary

After verifying the checksum, open **System Settings → Privacy & Security** and allow the application. If the downloaded file still carries a quarantine attribute, remove it explicitly:

```bash
xattr -d com.apple.quarantine ./ServeIt
```

### Windows SmartScreen appears

Verify the downloaded binary against `SHA256SUMS`. If it matches the GitHub release, choose **More info → Run anyway**. The project does not currently distribute a code-signed Windows executable.

## Build from source

Go 1.24 or newer is required.

```bash
git clone https://github.com/Danakolana/ServeIt.git
cd ServeIt
go test ./...
go build -o ServeIt .
```

The HTML pages are embedded with `//go:embed`, so the built executable does not need template files beside it.

## Contributing and roadmap

The detailed development plan is in [BACKLOG.md](BACKLOG.md). Bug reports and focused feature proposals are welcome in [GitHub Issues](https://github.com/Danakolana/ServeIt/issues).

Release maintainers should follow [RELEASING.md](RELEASING.md).
