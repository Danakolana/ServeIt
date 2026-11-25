# lanserve

The folder on this machine is already a link. Open it from a phone, a laptop on the same network, or a guest who should see only the files you picked — one binary, no runtime, no install tree.

lanserve is a LAN file server. Point it at any directory, hand someone a link or a QR code, and keep the rest of the disk private.

## Why it is different

Most one-command file servers publish the whole tree and stop there. You install a language runtime, pull a package, and everyone who can open the port can walk every subdirectory.

lanserve is the opposite shape:

- **One file.** The UI, search, auth, and QR codes are compiled in. The current binary is about **13 MB**. There is no Node, Python, or package directory beside it.
- **Any path, not only the shell’s current directory.** `--dir` is the served root. Everything else is a flag.
- **A real guest boundary.** The super-admin panel lets you check individual files and folders, including a slice of a subdirectory. Unchecked siblings never appear on the guest link.
- **Links that expire.** Idle timeout plus a hard maximum of 24 hours. Page views and downloads are counted. You can revoke a link immediately.

## Run it

```bash
go build -o lanserve .
./lanserve --dir /data/drop --port 9000
```

Or, if you already have a binary:

```bash
./lanserve --dir ~/Videos --port 8080 --password guest --admin-password owner
```

On startup the terminal prints each LAN address, the admin URL, and a QR code for both. The super-admin password is printed when you do not set one.

| Flag | Default | What it does |
|---|---|---|
| `--dir` | `.` | Directory to publish. Absolute or relative. This is the only root guests and the file browser can see. |
| `--port` | `8080` | TCP port. The process listens on all interfaces. |
| `--password` | empty | Basic-auth password for the main browser and search. Username is always `admin`. Empty means the file browser is open on the LAN. |
| `--admin-password` | generated | Password for `/admin`. Username is `admin`. If omitted, lanserve reuses `--password`. If both are empty, it prints a random admin password. |

```bash
./lanserve
./lanserve --dir /home/user/files --port 9000
./lanserve --dir /data --port 8888 --password hunter2 --admin-password separate-secret
```

## What you get

**File browser.** Breadcrumbs, directory listing, and a click-to-download for any file under `--dir`.

**Search.** Live search across files and subfolders, capped at 200 hits.

**Super admin** at `/admin`. Browse the same tree, check the rows you want, and create a temporary link. A checked folder includes its descendants. A checked file is only that file. Selection stays in the browser while you move between folders, so a share can mix paths from different places.

**Guest page** at `/s/<token>`. The guest can list and download only the selection. Each view or download restarts the idle timer. The link still dies at the maximum lifetime, which cannot exceed 24 hours.

**QR codes.** One in the admin panel for each share link, and startup QR codes in the terminal for the server addresses.

**State.** Shares live in `.lanserve-shares.json` under the served root. That file is hidden from browsing, search, and guest links.

## Share a slice of a folder

1. Open `http://<lan-address>:<port>/admin` and sign in as `admin`.
2. Walk into the subdirectory. Check only the files or child folders that should leave the machine.
3. Set idle timeout and maximum lifetime, then create the link.
4. Copy the URL or show the QR code. Revoke it from the same page when you are done.

The main browser and a guest link are different doors. `--password` locks the full tree. A share link does not unlock anything outside its selection, and it does not require the file-browser password.

## Build

```bash
go build -o lanserve .
```

Go 1.26 or newer. The HTML pages are embedded with `//go:embed`, so the binary does not need `tmpl.html`, `admin.html`, or `share.html` next to it at runtime.
