# sesnap

A CLI and MCP screenshot tool powered by [chromedp](https://github.com/chromedp/chromedp). Only Chrome is required at runtime.

- Capture multiple URLs in parallel within one Chrome process.
- Use logged-in Chrome profiles without locking your running browser.
- Capture viewports, full pages, or elements, with optional interactions and a browser-style address bar.

<img width="50%" height="50%" alt="Browser-style address bar sample" src="https://github.com/user-attachments/assets/b05a3711-92b4-4e00-bfca-e96cca693d67"/>

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

# Custom Chrome flags
sesnap -u="https://example.com/" -C="lang=ja" -C="disable-extensions" -o=/tmp/example.png

# Click in order, wait for a submenu, then hover and capture with an address bar
sesnap -u="https://example.com/" -c=".menu-button" -c=".category" \
  -a="#submenu" -e=".submenu-item" -b -o=/tmp/submenu.png

# Expand a <select> dropdown (use -s="*" to expand all)
sesnap -u="https://example.com/" -s="select#country" -o=/tmp/select.png
```

After navigation, actions run in this order: clicks → wait-for → hover → select expansion → capture.

- Repeat `-c` to click in order. Each click waits for visibility, then at least 500ms and any detected navigation. Capture stays in the original tab; new tabs are not followed.
- For asynchronous updates, use `-a` / `--wait-for` with a selector specific to the desired state. It waits for visibility without cropping. `-w` applies only after initial navigation.
- Each URL has a 60-second deadline (`-t` / `--timeout`), starting when a capture slot is acquired; initial navigation has a separate 10-second limit. A failed URL does not stop other URLs, but any failure returns a nonzero exit status.

### MCP Server Mode

Run `sesnap --mcp` as an [MCP](https://modelcontextprotocol.io) server over stdio. Chrome stays running across requests. Client configuration (e.g. `claude_desktop_config.json`):

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

| Tool | Description |
|------|-------------|
| `screenshot` | Return base64 images (PNG by default; supports `format: "jpeg"`) |
| `screenshot_to_file` | Save images and return file paths |
| `list_profiles` | List available Chrome profile directories |

Screenshot request example:

```json
{"urls": ["https://example.com/"], "clicks": [".menu-button", ".menu-item"], "wait_for": "#ready", "address_bar": true}
```

`urls` is required and must be nonempty; `screenshot_to_file` also requires `output`. Use either `clicks` or the legacy single `click`, never both. `wait_for` waits for visibility without cropping; `address_bar` shows the URL at capture time.

Each URL uses a separate tab. `timeout` defaults to 60 seconds; cancellation stops active and queued captures. Any capture or file-write failure sets `isError: true`, while successful results remain in the response.

### Chrome Profiles

`-p` copies a [Chrome profile](https://chromium.googlesource.com/chromium/src/+/HEAD/docs/user_data_dir.md) without modifying the original.

- **Without `-r`**: use a temporary copy and delete it after execution.
- **With `-r`**: reuse a persistent copy under `~/.sesnap/`. Set `SESNAP_CACHE_DIR` to override this directory.

Concurrent use of the same persistent cache is rejected; omit `-r` for independent parallel processes. Chrome's `Singleton*` locks are never deleted by sesnap.

After forced termination, a cache's `.lock` directory may remain. The error reports its path. Remove only that directory after confirming no sesnap process is using the cache.

### Limitations

- **Full-page screenshot size limit** — Full-page (`-f`) and element (`-q`) screenshots are limited by Chrome's GPU texture size of 16384 physical pixels per axis. With the default scale factor (2.0), this means pages taller or wider than **8192 CSS pixels** will be clipped. At scale factor 1.0, the limit is 16384 CSS pixels.
- **Element position limit** — Element capture resets scrolling and clips to the expanded viewport. Elements outside its maximum bounds produce an error rather than an image of another region.

### Tips

- **Icon fonts showing as ✕ marks** — Web fonts (e.g., Font Awesome, Material Icons) may not finish loading within the default wait time. Try increasing `-w` (e.g., `-w 10`).

## Development

### Build

For a local build, see [Build from source](#build-from-source). To cross-compile with GoReleaser:

```bash
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
