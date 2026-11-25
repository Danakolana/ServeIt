# Backlog

Do not start these until the owner explicitly says to begin implementation.
Each item below is the full brief. Ship them as written, and keep the UI simple, minimal, and sharp.

## 1. Terminal startup should look intentional

When the process starts, the terminal is the product’s front door. QR codes, URLs, credentials, and any other useful startup facts should read as one designed panel, not a debug dump.

- Group related lines: what is being served (`--dir`), port, whether file-browser auth is on, admin URL, and the admin username.
- Show the admin password only in that panel, labeled, and only when it was generated or supplied at startup. Do not scatter it in a second log line.
- Render QR codes so they sit with the URL they encode. Label each code in plain language (`Files`, `Admin`) and keep the blocks aligned.
- Add only information that helps the next action: LAN URL to open, admin URL, how to stop the process, and auth state. Skip noise.
- Plain ASCII is fine. The result should still look orderly in a normal terminal, including narrow widths.
- Startup logging from the standard library should not break the panel apart.

## 2. QR codes for the useful LAN address only

Today every up, non-loopback IPv4 interface gets its own file QR and admin QR. On a typical machine that includes a `192.168.x.x` LAN address and a `172.x.x.x` address from Docker, a VM, or another virtual NIC. The `172` code is the one people scan by mistake.

- Prefer `192.168.0.0/16`. Print QR codes and the primary URL for those addresses only.
- If no `192.168` address exists, fall back to loopback (`127.0.0.1`) so local use still has a code.
- Do not print QR codes for `172.16.0.0/12`, link-local, or other virtual ranges.
- The text panel may mention that other interfaces were skipped, in one short line, without extra QR codes.
- The owner observed two codes, one in the `192.168` range and one in `172`. After this change, a normal home LAN should show the `192.168` code only. (The spoken “198” range means `192.168`.)

## 3. Sortable columns on listings that deserve it

The file tables are the working surface: name, size, and modified time. If a listing is long enough to scan, those columns should sort.

- Main file browser, super-admin picker, and guest share page.
- Clicking a column header sorts by that column. A second click reverses it. The active column and direction are visible.
- Name sort is case-insensitive. Size sort is numeric. Modified sort is by timestamp.
- Directories can stay grouped above files unless the user has chosen size or date; pick one rule and use it on every table.
- Sorting is local to the current page. It must not drop the admin selection, and it must not refetch in a way that loses checked rows.
- Keep the control as the existing header text. No extra toolbar.

## 4. Choose files, then download them together

The user-facing file browser (and the guest share page, which is the same job for a visitor) only downloads one click at a time. A person should be able to tick several files in the current list, press one download action, and leave the rest to the server.

- Selection is for the current directory listing. State the count and the total size before the download starts.
- One primary action starts the transfer. After that, the panel owns the work: the user should not have to click each file or keep the tab focused on every response.
- Large files are the normal case. Do not buffer a whole archive in memory. Stream a zip, stored (no recompression), or an equally simple single-response archive, writing file bytes as they are read.
- Show clear progress or a clear “preparing / downloading” state, and a failure if a chosen path disappears mid-request.
- Empty selection does nothing. Selecting a folder means that folder’s contents are part of the archive; say so in the UI in one line.
- The main browser respects normal auth. A guest archive contains only paths already allowed by that share.
- Stay inside the current minimal layout: a checkbox column, a small action bar, and the existing type. No second visual system.

## 5. A small pass on the existing UI

The pages should stay simple, minimal, and polished. This is a refinement of the current browser, admin, and guest screens, not a redesign.

- Tighten spacing, type, and the header so the three pages feel like one product.
- Keep the light background, short labels, and the current density. Do not add illustrations, gradients, or extra chrome.
- Check the admin share cards and the file tables at a narrow width. Actions must remain reachable.
- Do not block items 1–4 on this pass, and do not restyle ahead of the selection and sort controls those items add.
