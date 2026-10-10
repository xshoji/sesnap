# sesnap

A web page screenshot tool with parallel multi-URL capture and lock-free Chrome profile support, powered by [chromedp](https://github.com/chromedp/chromedp) (headless Chrome). Only Chrome is required — no Puppeteer, no Playwright, no Node.js, no Python.

### Why sesnap?

- **Parallel capture** – Multiple URLs are captured simultaneously in separate tabs within a single Chrome process. No sequential waiting — all pages load and render at the same time.
- **Lock-free profile usage** – When using a Chrome profile (`-p`), the tool copies it to an isolated cache directory. This means you can take screenshots with your logged-in session **even while your main browser is running** — no profile lock conflicts.

### Other Features

- **Viewport / Element / Full-page screenshot** – capture the visible area, a specific CSS selector (`-q`), or the entire scrollable page (`-f`)
- **Browser-style address bar** – add a realistic address bar with favicon and URL to the top of screenshots (`-b`), perfect for documentation and presentations
    - <img width="50%" height="50%" alt="sample" src="https://github.com/user-attachments/assets/b05a3711-92b4-4e00-bfca-e96cca693d67"/>

- **Click / Hover before capture** – click (`-c`) or hover (`-e`) a CSS selector before taking the screenshot, useful for capturing dropdown menus, tooltips, and other interactive states
- **Custom Chrome flags** – pass arbitrary Chrome flags with `-C`
- **Idempotent execution** – without `-r`, the cached profile is always freshly copied, ensuring consistent results regardless of previous runs

## Requirements

- Google Chrome / Chromium installed

**For development:**
- Go 1.26+

## Installation

### Homebrew

```bash
brew install xshoji/tap/sesnap --cask
```


### Build from source

```bash
git clone https://github.com/xshoji/sesnap.git
cd sesnap
go build -ldflags="-s -w" -trimpath -o sesnap main.go
```

## Usage

```bash
sesnap -u <URL> -o /tmp/screenshot.png [options]
```

### Options

| Short | Long | Default | Description |
|-------|------|---------|-------------|
| `-u` | `--url` | *(required)* | URL to capture (can be specified multiple times) |
| `-o` | `--output` | *(required)* | Output file path (auto-numbered with multiple URLs: `_001.png`, `_002.png`, …) |
| `-q` | `--query` | `""` | CSS selector – screenshot the first matching element |
| `-p` | `--profile` | `""` | Chrome profile directory to copy and cache |
| `-w` | `--wait` | `3` | Wait seconds after navigation before capturing |
| `-W` | `--width` | `1280` | Viewport width |
| `-H` | `--height` | `860` | Viewport height |
| `-e` | `--hover` | `""` | Hover over the first element matching the CSS selector before capture |
| `-c` | `--click` | — | Click the first element matching the CSS selector before capture; repeat to click in order |
| `-a` | `--wait-for` | `""` | Wait for the first element matching this CSS selector to become visible after clicks, before hover and capture |
| `-s` | `--expand-select` | `""` | Expand `<select>` elements as HTML dropdown overlay before capture. Use CSS selector or `"*"` for all |
| `-f` | `--full` | `false` | Enable full-page screenshot |
| `-b` | `--address-bar` | `false` | Add browser-style address bar (favicon + actual browser URL at capture time) to the top of screenshot |
| `-d` | `--debug` | `false` | Enable debug mode |
| `-n` | `--no-headless` | `false` | Disable headless mode (show browser window) |
| `-r` | `--reuse` | `false` | Reuse cached profile (do not delete after execution) |
| `-j` | `--parallel` | `NumCPU` | Max number of parallel tabs for screenshot capture |
| `-t` | `--timeout` | `60` | Timeout seconds per URL, including navigation and interactions |
| `-m` | `--mcp` | `false` | Run as MCP (Model Context Protocol) server over stdio |
| `-C` | `--chrome-flag` | `""` | Extra Chrome flag as `key=value` (can be specified multiple times) |

### Examples

```bash
# Viewport screenshot
sesnap -u="https://www.example.com/" -W=1280 -H=800 -o=/tmp/example.png

# Element screenshot with CSS selector
sesnap -u="https://news.yahoo.co.jp/" -q="#liveStream" -o="/tmp/livestream.png"

# Full-page screenshot
sesnap -u="https://www.example.com/" -f -o=/tmp/fullpage.png

# Multiple URLs (parallel capture)
sesnap -u="https://www.yahoo.co.jp/" -u="https://www.google.com/" -o=/tmp/sites.png

# With Chrome profile (for logged-in sessions)
sesnap -u="https://example.com/dashboard" \
  -p="/Users/you/Library/Application Support/Google/Chrome/Default" \
  -r -o=/tmp/dashboard.png

# With browser-style address bar
sesnap -u="https://www.example.com/" -b -o=/tmp/with_bar.png

# Custom Chrome flags
sesnap -u="https://example.com/" -C="lang=ja" -C="disable-extensions" -o=/tmp/example.png

# Hover over an element before capture (e.g. tooltip)
sesnap -u="https://example.com/" -e=".tooltip-trigger" -o=/tmp/tooltip.png

# Click an element before capture (e.g. open dropdown menu)
sesnap -u="https://example.com/" -c=".menu-button" -o=/tmp/menu.png

# Click a menu button, then a menu item
sesnap -u="https://example.com/" --click=".menu-button" --click=".menu-item" -o=/tmp/result.png

# Expand a specific <select> dropdown as HTML overlay
sesnap -u="https://example.com/" -s="select#country" -o=/tmp/select.png

# Expand all <select> elements
sesnap -u="https://example.com/" -s="*" -o=/tmp/all_selects.png

# Click to open menu, then hover a sub-item
sesnap -u="https://example.com/" -c=".menu-button" -e=".submenu-item" -o=/tmp/submenu.png

# Click, wait for the destination content, and show the final URL
sesnap -u="https://example.com/" -c=".next" --wait-for="#ready" -b -o=/tmp/destination.png
```

Repeated `-c` / `--click` flags run in the order supplied, before hover and select expansion. Each click waits for the target to become visible, then waits at least 500ms. If the main frame is still loading, capture waits for loading to stop (up to another 10 seconds), including same-URL reloads and redirects. Actions stay in the original tab; links that open a new tab do not switch the capture target. A failed click stops capture for that URL; the error includes the click number and selector. Commas remain part of the CSS selector, not separators between operations. Empty flags (`--click=""`) retain the legacy no-click behavior; whitespace-only selectors are rejected.

For SPA updates or asynchronous rendering, use `-a` / `--wait-for` with a selector that becomes visible when the desired content is ready. This wait runs after all clicks, before hover and select expansion, and does not change the capture area. It also works without clicks. A missing element fails at the per-URL capture deadline. An already-visible element satisfies the wait immediately; choose a selector specific to the destination state. The 500ms delay alone cannot detect navigation that starts later or guarantee completion of asynchronous requests or animations. `-w` / `--wait` applies only after the initial navigation, not after clicks.

Each URL has a 60-second capture deadline (`-t` / `--timeout` to change it), starting when a capture slot is acquired. Initial navigation commit and document parsing have a separate 10-second limit and do not wait for every resource to load. Failed URLs do not prevent other URLs from completing. Any failure returns a nonzero CLI exit status after cleanup.

### MCP Server Mode

sesnap can run as an [MCP (Model Context Protocol)](https://modelcontextprotocol.io) server, allowing AI agents to take screenshots directly without spawning a new process for each capture. Chrome stays running across requests, dramatically reducing latency.

```bash
# Start as MCP server
sesnap --mcp

# With Chrome profile for logged-in sessions
sesnap --mcp -p="/Users/you/Library/Application Support/Google/Chrome/Default" -r
```

The server exposes three tools via stdio:

| Tool | Description |
|------|-------------|
| `screenshot` | Capture URLs and return images directly as base64 (supports `format: "jpeg"` for token savings) |
| `screenshot_to_file` | Capture URLs and save to files, returning file paths (no image tokens consumed) |
| `list_profiles` | List available Chrome profile directories |

Both screenshot tools accept `clicks` for sequential clicks:

```json
{"urls": ["https://example.com/"], "clicks": [".menu-button", ".menu-item"]}
```

The existing `click` string remains supported for a single click (`click: ""` still means no click). Supplying both `click` and `clicks` is an error. An empty `clicks` array performs no clicks; empty elements are rejected.

Use `wait_for` to wait for destination content without cropping the screenshot:

```json
{"urls": ["https://example.com/"], "clicks": [".next"], "wait_for": "#ready", "address_bar": true}
```

`address_bar` displays the actual browser URL at capture time, including redirects and SPA URL changes.

Each URL uses a separate tab, including simultaneous single-URL requests. The `timeout` parameter sets the capture deadline in seconds (default: 60), and request cancellation stops active captures and queued work. A failed URL or file write sets `isError: true`, including partial failures; successful images or paths remain in the response.

`urls` must be nonempty, and `output` must be nonempty for `screenshot_to_file`. Width and height must be positive integers fitting within 16384 CSS and physical pixels. `scale` must be finite and positive, `wait` a nonnegative integer, `timeout` a positive integer, `quality` an integer from 1 to 100, and `format` either `png` or `jpeg`.

**AI client configuration example** (e.g. `claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "sesnap": {
      "command": "sesnap",
      "args": ["--mcp", "-j", "4"]
    }
  }
}
```

### Details of the -p flag and the Google Chrome profile directory

> Chromium Docs - User Data Directory  
> https://chromium.googlesource.com/chromium/src/+/HEAD/docs/user_data_dir.md  

- The `-p` flag specifies a Chrome profile directory to copy and use for the screenshot session. This allows you to capture pages with your logged-in session without locking your main browser.
- The original profile is never modified — it is always copied to an isolated directory.
- **Without `-r`**: the profile is copied to a system temporary directory (e.g., `/tmp/sesnap-userdata-*`) and automatically deleted after each run. Your home directory is never touched.
- **With `-r`**: the profile is copied to a persistent cache directory (`~/.sesnap/`, overridable via `SESNAP_CACHE_DIR`) and kept for reuse across runs.

Reusable caches are keyed by the resolved absolute source path, not just the profile name. Copies are published only after completion. Concurrent use of the same cache is rejected; use without `-r` for independent parallel processes. Chrome's `Singleton*` locks are never deleted by sesnap.

After a forced termination, a cache's `.lock` directory may remain. The error reports its path. Remove only that directory after confirming no sesnap process is using the cache. Older basename-keyed caches are not reused or deleted automatically.


### Limitations

- **Full-page screenshot size limit** — Full-page (`-f`) and element (`-q`) screenshots are limited by Chrome's GPU texture size of 16384 physical pixels per axis. With the default scale factor (2.0), this means pages taller or wider than **8192 CSS pixels** will be clipped. At scale factor 1.0, the limit is 16384 CSS pixels.
- **Element position limit** — Element capture resets scrolling and clips to the expanded viewport. Elements outside its maximum bounds produce an error rather than an image of another region.

### Tips

- **Icon fonts showing as ✕ marks** — Web fonts (e.g., Font Awesome, Material Icons) may not finish loading within the default wait time. Try increasing `-w` (e.g., `-w 10`).

### Environment Variables

| Variable | Description |
|----------|-------------|
| `SESNAP_CACHE_DIR` | Override the default persistent profile cache directory used with `-r` (default: `~/.sesnap`) |

## Development

### Build

```bash
go build -ldflags="-s -w" -trimpath -o sesnap main.go

# Cross-compiling with GoReleaser
goreleaser build --snapshot --clean
```

### Test

```bash
# Unit tests only (no Chrome required)
go test -v

# All tests including E2E (Chrome required)
SESNAP_E2E=1 go test -v
```

## Release

The release flow for this repository is automated with GitHub Actions.
Pushing Git tags triggers the release job.

```
# Release
git tag v0.0.1 && git push --tags


# Delete tag
v="v0.0.1"; git tag -d "${v}" && git push origin :"${v}"

# Delete tag and recreate new tag and push
v="v0.0.1"; git tag -d "${v}" && git push origin :"${v}"; git tag "${v}"; git push --tags
```

## License

See [LICENSE](LICENSE) for details.
