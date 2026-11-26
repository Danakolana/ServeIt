# Backlog

Do not start these until the owner explicitly says to begin implementation.
Each item below is the full brief. Ship them as written, and keep the UI simple, minimal, and sharp.

## 1. Terminal startup should look intentional

**Status: done.**

When the process starts, the terminal is the product’s front door. QR codes, URLs, credentials, and any other useful startup facts should read as one designed panel, not a debug dump.

- Group related lines: what is being served (`--dir`), port, whether file-browser auth is on, admin URL, and the admin username.
- Show the admin password only in that panel, labeled, and only when it was generated or supplied at startup. Do not scatter it in a second log line.
- Render QR codes so they sit with the URL they encode. Label each code in plain language (`Files`, `Admin`) and keep the blocks aligned.
- Add only information that helps the next action: LAN URL to open, admin URL, how to stop the process, and auth state. Skip noise.
- Plain ASCII is fine. The result should still look orderly in a normal terminal, including narrow widths.
- Startup logging from the standard library should not break the panel apart.

## 2. QR codes for the useful LAN address only

**Status: done.**

Today every up, non-loopback IPv4 interface gets its own file QR and admin QR. On a typical machine that includes a `192.168.x.x` LAN address and a `172.x.x.x` address from Docker, a VM, or another virtual NIC. The `172` code is the one people scan by mistake.

- Prefer `192.168.0.0/16`. Print QR codes and the primary URL for those addresses only.
- If no `192.168` address exists, fall back to loopback (`127.0.0.1`) so local use still has a code.
- Do not print QR codes for `172.16.0.0/12`, link-local, or other virtual ranges.
- The text panel may mention that other interfaces were skipped, in one short line, without extra QR codes.
- The owner observed two codes, one in the `192.168` range and one in `172`. After this change, a normal home LAN should show the `192.168` code only. (The spoken “198” range means `192.168`.)

## 3. Sortable columns on listings that deserve it

**Status: done.**

The file tables are the working surface: name, size, and modified time. If a listing is long enough to scan, those columns should sort.

- Main file browser, super-admin picker, and guest share page.
- Clicking a column header sorts by that column. A second click reverses it. The active column and direction are visible.
- Name sort is case-insensitive. Size sort is numeric. Modified sort is by timestamp.
- Directories can stay grouped above files unless the user has chosen size or date; pick one rule and use it on every table.
- Sorting is local to the current page. It must not drop the admin selection, and it must not refetch in a way that loses checked rows.
- Keep the control as the existing header text. No extra toolbar.

## 4. Choose files, then download them together

**Status: done.**

The user-facing file browser (and the guest share page, which is the same job for a visitor) only downloads one click at a time. A person should be able to tick several files in the current list, press one download action, and leave the rest to the server.

- Selection is for the current directory listing. State the count and the total size before the download starts.
- One primary action starts the transfer. After that, the panel owns the work: the user should not have to click each file or keep the tab focused on every response.
- Large files are the normal case. Do not buffer a whole archive in memory. Stream a zip, stored (no recompression), or an equally simple single-response archive, writing file bytes as they are read.
- Show clear progress or a clear “preparing / downloading” state, and a failure if a chosen path disappears mid-request.
- Empty selection does nothing. Selecting a folder means that folder’s contents are part of the archive; say so in the UI in one line.
- The main browser respects normal auth. A guest archive contains only paths already allowed by that share.
- Stay inside the current minimal layout: a checkbox column, a small action bar, and the existing type. No second visual system.

## 5. A small pass on the existing UI

**Status: done.**

The pages should stay simple, minimal, and polished. This is a refinement of the current browser, admin, and guest screens, not a redesign.

- Tighten spacing, type, and the header so the three pages feel like one product.
- Keep the light background, short labels, and the current density. Do not add illustrations, gradients, or extra chrome.
- Check the admin share cards and the file tables at a narrow width. Actions must remain reachable.
- Do not block items 1–4 on this pass, and do not restyle ahead of the selection and sort controls those items add.

## 6. Benchmark and improve multi-file LAN download throughput

**Status: implemented. Two-device LAN measurement remains pending.**

The archive already stores file bytes without compression and streams them, which is a good baseline for large files. For folder downloads, the browser's `?plan=1` request walks the full tree and the download request walks it again. The ZIP writer also flushes the HTTP response after every file. Both costs can matter when a folder contains thousands of small files.

- Benchmark a representative large file and a folder with many small files over loopback and a real LAN. Record time to first byte, total throughput, memory use, and request count.
- Remove the per-file flush if the benchmark shows it improves throughput while the transfer still starts promptly. Let the HTTP server buffer writes normally, or flush at a measured interval.
- Avoid the second full tree walk for the plan when it materially delays download start. Keep the selection count and size accurate enough for the UI, and preserve the current streaming, stored ZIP format and path checks.
- Keep single-file downloads on the direct file-serving path so the HTTP server can use its optimized file transfer behavior.
- Re-run the same benchmarks and retain only changes with a clear improvement and no meaningful memory regression.

Implementation: the ZIP writer no longer flushes after each file. The browser reuses its folder plan for the size display and makes a lightweight `?check=1` request before submitting the archive form, avoiding another full tree walk. The check still rejects missing selected paths and expired or unauthorized shares. Direct single-file links are unchanged.

Loopback probe (`PERF_PROBE=1 go test -run TestPerformanceProbe -v`): 2,000 files of 4 KiB each, three runs, median values. Click-to-first-byte fell from 35.2 ms to 14.8 ms; total click-to-complete fell from 108.9 ms to 70.4 ms; effective throughput rose from 74.1 to 114.5 MiB/s. Both flows used two requests; the preflight changed from a full plan to a lightweight check. Total Go allocation fell from about 76.9 to 72.2 MB per run. A 32 MiB single-file ZIP probe was noisy and did not show a clear gain. These numbers are local loopback results, not a physical LAN measurement.

## 7. Make every valid filename downloadable

**Status: implemented.**

The main browser currently mishandles several ordinary filesystem names. `http.ServeFile` redirects a request ending in `/index.html` to its directory, so an `index.html` file cannot be downloaded directly. Main-browser links also leave `#`, `?`, and percent sequences with URL semantics instead of encoding them as filename bytes. A symlink whose resolved target is a directory is rendered as a downloadable file.

- Serve direct files in both the main browser and guest shares with `os.Open` plus `http.ServeContent`, retaining HEAD, Range, modification time, MIME detection, and the current attachment filename.
- Add one path-to-URL helper that percent-encodes each path segment while preserving `/` separators. Use it for main listing links and breadcrumbs. Keep query-parameter paths encoded as query data.
- Classify an allowed symlink using its resolved target so a symlink to a directory receives directory navigation, sorting, and selection behavior.
- Handler tests cover nested `index.html`, spaces, Unicode, `#`, `?`, `%41`, literal `%`, and symlinked directories. They assert the rendered URL and the bytes returned by following it.

## 8. Protect open-mode servers from accidental exposure and DNS rebinding

**Status: core protections implemented; IPv6 and state-file alias follow-up implemented. Priority: critical.**

No password is the default. ServeIt now validates Host headers, supports an explicit listen address and hidden-file policy, and prints an open-mode warning. The remaining work is to expand platform-specific coverage and test the edge cases below.

- Validate the request Host before routing. Accept configured IP literals, loopback names, the machine hostname, and explicit names supplied by a repeatable `--allow-host` option. Reject other hosts consistently, including requests for files, search, shares, and admin APIs. **Implemented.**
- Replace the fixed IPv4-only bind with an explicit `--listen` address and support IPv6 listeners and bracketed IPv6 URLs. Preserve a predictable default and show every reachable address accurately at startup. **Implemented for private IPv6 addresses, bracketed URLs, and explicit IPv6 listen values; platform-specific display coverage remains.**
- Hide any path segment beginning with `.` by default. Enforce this in direct file access, listings, search, share creation and browsing, ZIP planning, checks, and archive generation. Add `--show-hidden` as an explicit opt-in. **Implemented.**
- Continue reserving the share-state file even when hidden files are enabled. On case-insensitive filesystems, identify the actual state file with filesystem identity (`os.SameFile` or an equivalent resolved comparison) rather than a case-sensitive spelling check. **Implemented for the active state path and aliases.**
- Print a concise startup warning when the full browser has no password. State whether hidden files are excluded and show the opt-in flag. **Implemented.**
- Add DNS-rebinding-style Host tests and table-driven hidden-path tests, including nested dot directories, alternate state-file casing, symlinks, search, and ZIP requests.

## 9. Separate durable share state from the served directory

**Status: partially implemented; follow-up durability work remains. Priority: high.**

The state file is written inside the served root. Existing shares loaded from a read-only USB drive or mount fail on every view because activity persistence cannot create and rename the temporary file; the resulting storage error is currently reported as “Share link not found.” Invalid JSON also aborts startup, and every view rewrites the complete file while expired and revoked records accumulate forever.

- Add an explicit `--state-file` option and environment equivalent. Choose a writable per-user default keyed by the canonical served root, with a documented compatibility or one-time migration path for existing state files. **Implemented with `SERVEIT_STATE_FILE`, per-user root-keyed defaults, and legacy-file migration.**
- Keep share creation and revocation durable before reporting success. Coalesce page-view, download-count, and idle-expiry writes over a short bounded interval, flush them during graceful shutdown, and document the maximum counter/activity loss after a crash.
- Never mutate the in-memory record permanently when persistence fails. Return a distinct internal/storage error and show an accurate 5xx response instead of “not found.” **Implemented for activity updates and revocation.**
- If the state JSON is corrupt, atomically quarantine it with a timestamped backup name, start with an empty store, and print a warning containing the recovery path. Do not overwrite the only corrupt copy. **Implemented.**
- Prune revoked and long-expired shares using a documented retention window so the file and admin response stay bounded.
- Test read-only roots, unwritable state destinations, interrupted writes, corrupt JSON recovery, restart persistence, migration, and pruning.

## 10. Harden authentication and HTTP trust boundaries

**Status: pending. Priority: high.**

The browser password becomes the admin password when `--admin-password` is omitted, secrets can only be supplied on the command line, comparisons are ordinary string comparisons, and failed authentication has no rate limit. The API also trusts `X-Forwarded-Proto` from any client and accepts an admin JSON body regardless of Content-Type.

- Always generate a separate admin password unless the owner explicitly supplies one. Do not reuse the browser password implicitly.
- Support `SERVEIT_PASSWORD`, `SERVEIT_ADMIN_PASSWORD`, and restrictive password-file options. Define and test precedence without printing supplied secrets outside the existing startup credential panel.
- Compare credentials in constant time and add bounded per-client authentication rate limiting for the browser and admin doors. Avoid global sleeps that let one client stall everyone else.
- Require `application/json` for admin JSON mutations. Add Origin checks for authenticated state-changing browser requests where applicable.
- Ignore forwarded scheme and host headers by default. Honor them only behind an explicitly configured trusted proxy, then validate their syntax before generating share URLs.
- Add a Content Security Policy. Use per-response nonces for the existing embedded scripts/styles or move them to dedicated embedded asset routes; do not weaken the policy with unrestricted inline script execution.
- Add authentication, rate-limit, Content-Type, proxy-spoofing, and CSP tests.

## 11. Correct share activity, countdowns, and admin polling cost

**Status: pending. Priority: medium.**

HEAD requests and every Range request for a shared file currently increment the download counter and rewrite state. The guest and admin countdowns subtract server timestamps from `Date.now()`, so a device with a bad clock can show a valid link as expired. Every 15-second admin poll also regenerates a PNG QR code for every share.

- Define a download as an initial successful GET. Do not count HEAD requests, and prevent video seeking or resume ranges from inflating the counter. Document the chosen resume behavior and test it with multiple Range requests.
- Send server-derived remaining seconds to both pages. Decrement from that baseline with a monotonic browser timer; refresh the baseline after server activity instead of depending on the wall clock of the phone or computer.
- Remove QR image generation from the share-list response. Generate or cache a QR only when the user opens its modal, using a small authenticated endpoint.
- Document that revocation blocks new requests but does not recall bytes already sent on an established transfer. Do not add per-chunk store locking merely to terminate active transfers.
- Keep the 15-second list refresh lightweight and add tests for counters, Range/HEAD behavior, remaining-time serialization, and lazy QR generation.

## 12. Make releases reproducible, least-privileged, and installable with Go

**Status: pending. Priority: high before the next release.**

The module path now matches the GitHub repository. The release workflow still needs hardening: it publishes without first running tests or vet, grants `contents: write` to build jobs, interpolates the manual tag input directly into shell source, and references actions by mutable major-version tags.

- Verify `go install github.com/Danakolana/ServeIt@<new-tag>` in a clean environment.
- Add a verification job that runs `go test ./...`, `go vet ./...`, and the supported cross-build matrix before publishing. The release job must depend on it.
- Give the workflow read-only permissions by default and grant `contents: write` only to the publishing job.
- Pass manual inputs through environment variables, validate release tags against the intended version format, and ensure a manual release builds the commit identified by that tag.
- Pin third-party actions to reviewed full commit SHAs with version comments. Include checkout, Go setup, artifact upload/download, and release publishing actions.
- Test the workflow with a non-publishing pull-request or local workflow check, then publish the next patch release and verify all binaries plus `SHA256SUMS`.

## 13. Reduce filesystem work on large shares and searches

**Status: pending. Priority: medium; benchmark each optimization.**

Path safety currently calls `EvalSymlinks` for ordinary entries repeatedly: listing resolves every entry, share authorization resolves both sides on each check, and ZIP walking can resolve and reauthorize the same descendant several times. `Use` performs filesystem authorization and a state-file rewrite while holding the global store mutex. Abandoned searches continue walking, and ZIP streaming uses the default 32 KiB copy buffer.

- Resolve the served root and selected share roots once. Use `Lstat`/`DirEntry.Type` to take the fast path for ordinary descendants, resolving actual symlinks before allowing them. Preserve the invariant that a symlink cannot escape the served root or its selected share root.
- Remove filesystem calls and persistence I/O from the share-store critical section. Copy the immutable authorization data under lock, validate outside it, then reacquire and recheck revocation/expiry before recording activity.
- Check `r.Context()` during search walking and stop promptly when the client disconnects or starts a replacement search.
- Benchmark a reusable 256 KiB to 1 MiB ZIP copy buffer against the existing 32 KiB behavior for one large file and thousands of small files. Retain it only if throughput improves without material memory growth.
- Extend the performance probe to report path-resolution calls, concurrent share latency, cancellation delay, allocations, first-byte time, and throughput. Include a real two-device LAN run.

## 14. Introduce a server object and focused security tests

**Status: pending. Priority: medium after tasks 7–13.**

Process-wide roots, passwords, templates, and the share store make tests mutate global state and prevent safe parallel execution. Main/share JavaScript and CSS also duplicate download and listing behavior, increasing the chance that a security or correctness fix reaches only one page.

- Move configuration, canonical root, templates, authentication, and share storage into a `Server` type with methods for handlers. Keep `main` responsible only for flags, construction, startup output, signals, and serving.
- Parse all templates through one constructor and expose shared embedded CSS/JavaScript from dedicated routes or template components with one implementation of sorting and multi-download behavior.
- Add table-driven tests for `cleanShareRel`, URL construction, authorization, case variants, backslashes, traversal, symlink boundaries, hidden paths, and state-file aliases. Run independent server instances in parallel where useful.
- Replace mixed `fmt.Printf`/`log.Printf` handler diagnostics with one injected logger. Preserve concise user-facing startup output.
- Move user-visible limits such as search results, selected items, ZIP entries, and maximum lifetime into clearly named constants. Keep related validation messages and boundary tests tied to those constants.
- Treat variable shadowing, `utf8.RuneCountInString`, and terminal-width library choices as cleanup within touched code, not reasons for a standalone refactor.

## 15. Shut down gracefully without cutting off active downloads

**Status: pending. Priority: high.**

The process currently relies on the operating system’s default SIGINT/SIGTERM behavior. Pressing Ctrl+C terminates it immediately, aborting in-progress file and ZIP transfers and providing no opportunity to flush coalesced share activity from task 9.

- Use `signal.NotifyContext` or equivalent handling for SIGINT and SIGTERM. On the first signal, stop accepting new connections and begin `http.Server.Shutdown` while existing requests continue.
- Allow active downloads to finish without an arbitrary short deadline. A second signal should call `Server.Close` and force an immediate exit so the operator can always stop a stuck transfer.
- Flush pending share-store activity before the graceful exit completes. Report flush or shutdown errors through the common logger and return a nonzero exit status when appropriate.
- Print one concise shutdown message stating that active transfers are draining and that a second Ctrl+C forces exit. Do not interleave routine request noise with the startup or shutdown panels.
- Add an integration test with a deliberately slow streaming response: confirm the first signal rejects new work but lets the active response finish, and confirm a second signal aborts it. Cover SIGTERM and persistence flushing without sending real signals to the main test process.
