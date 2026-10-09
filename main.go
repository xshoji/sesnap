package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// stringSlice implements flag.Value to accept multiple -u flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// version is set at build time via ldflags.
var version = "dev"

const (
	Req                  = "\x1b[33m(required)\x1b[0m "
	UsageDummy           = "########"
	defaultWidth   int64 = 1280
	defaultHeight  int64 = 860
	defaultWait          = 3
	defaultQuality       = 80
	// maxPhysicalDim is Chrome's GPU texture limit in physical pixels.
	// The actual CSS pixel limit depends on deviceScaleFactor.
	maxPhysicalDim    int64 = 16384
	cleanupTimeout          = 10 * time.Second
	navigationTimeout       = 10 * time.Second
	captureTimeout          = 60 * time.Second
	maxWaitSeconds          = int64(math.MaxInt64 / int64(time.Second))
)

var (
	commandDescription = "A fast, multi-page screenshot tool that requires only Chrome. Supports profile specification without locking your main browser.\n  Set SESNAP_CACHE_DIR to override the default profile cache directory (~/.sesnap).\n  Device scale factor can be changed via -c \"device-scale-factor=1.0\" (default: 2.0 Retina).\n  Custom DNS resolution via -c \"host-resolver-rules=MAP example.com 127.0.0.1\"."
	urls               stringSlice
	chromeFlags        stringSlice
	clickSelectors     stringSlice
	deviceScaleFactor  = 2.0
	arguments          = struct {
		outputPath     *string
		querySelector  *string
		profileDir     *string
		waitSeconds    *int
		windowWidth    *int64
		windowHeight   *int64
		hoverSelector  *string
		expandSelect   *string
		fullScreenshot *bool
		showAddressBar *bool
		debug          *bool
		noHeadless     *bool
		reUseProfile   *bool
		parallel       *int
		mcpMode        *bool
		timeoutSeconds *int
	}{
		defineFlagValue("o", "output" /*       */, "" /*               */, Req+"Output path of screenshot (with multiple URLs, auto-numbered: <base>_001.png, _002.png, ...)", flag.String, flag.StringVar),
		defineFlagValue("q", "query" /*        */, "" /*               */, "Query selector. Screenshot the first matching element. ( e.g. -q=\".className#id\" )", flag.String, flag.StringVar),
		defineFlagValue("p", "profile" /*      */, "" /*               */, "Chrome profile directory to copy. (e.g. -p=\"~/Library/Application Support/Google/Chrome/Default\").", flag.String, flag.StringVar),
		defineFlagValue("w", "wait" /*         */, defaultWait /*      */, "Wait seconds after page navigation before taking screenshot", flag.Int, flag.IntVar),
		defineFlagValue("W", "width" /*        */, defaultWidth /*     */, "Viewport width (affects page layout, e.g. responsive design). Without -q, this is the output image width", flag.Int64, flag.Int64Var),
		defineFlagValue("H", "height" /*       */, defaultHeight /*    */, "Viewport height (affects page layout, e.g. responsive design). Without -q, this is the output image height", flag.Int64, flag.Int64Var),
		defineFlagValue("e", "hover" /*        */, "" /*               */, "Hover over the first element matching the CSS selector before capture (e.g. -e=\".tooltip-trigger\")", flag.String, flag.StringVar),
		defineFlagValue("s", "expand-select" /**/, "" /*               */, "Expand <select> elements as HTML dropdown overlay before capture. Use CSS selector or \"*\" for all (e.g. -s=\"select#country\", -s=\"*\")", flag.String, flag.StringVar),
		defineFlagValue("f", "full" /*         */, false /*            */, "Enable full screenshot mode", flag.Bool, flag.BoolVar),
		defineFlagValue("b", "address-bar" /*  */, false /*            */, "Add browser-style address bar to the top of screenshot", flag.Bool, flag.BoolVar),
		defineFlagValue("d", "debug" /*        */, false /*            */, "Enable debug mode", flag.Bool, flag.BoolVar),
		defineFlagValue("n", "no-headless" /*  */, false /*            */, "Disable headless mode", flag.Bool, flag.BoolVar),
		defineFlagValue("r", "reuse" /*        */, false /*            */, "Reuse cached profile (do not delete after execution)", flag.Bool, flag.BoolVar),
		defineFlagValue("t", "parallel" /*     */, runtime.NumCPU() /* */, "Max number of parallel tabs for screenshot capture", flag.Int, flag.IntVar),
		defineFlagValue("m", "mcp" /*          */, false /*            */, "Run as MCP (Model Context Protocol) server over stdio", flag.Bool, flag.BoolVar),
		defineFlagValue("T", "timeout", int(captureTimeout/time.Second), "Timeout seconds per URL, including navigation and interactions", flag.Int, flag.IntVar),
	}
)

// captureParams holds per-request screenshot parameters, decoupled from global flags.
type captureParams struct {
	windowWidth    int64
	windowHeight   int64
	waitSeconds    int
	querySelector  string
	clickSelectors []string
	hoverSelector  string
	expandSelect   string
	fullScreenshot bool
	showAddressBar bool
	scaleFactor    float64
	timeout        time.Duration
}

func captureParamsFromArgs() captureParams {
	// Empty flags retain the legacy no-click behavior.
	var selectors []string
	for _, sel := range clickSelectors {
		if sel != "" {
			selectors = append(selectors, sel)
		}
	}
	return captureParams{
		windowWidth:    *arguments.windowWidth,
		windowHeight:   *arguments.windowHeight,
		waitSeconds:    *arguments.waitSeconds,
		querySelector:  *arguments.querySelector,
		clickSelectors: selectors,
		hoverSelector:  *arguments.hoverSelector,
		expandSelect:   *arguments.expandSelect,
		fullScreenshot: *arguments.fullScreenshot,
		showAddressBar: *arguments.showAddressBar,
		scaleFactor:    deviceScaleFactor,
		timeout:        time.Duration(*arguments.timeoutSeconds) * time.Second,
	}
}

func (p captureParams) validate() error {
	if math.IsNaN(p.scaleFactor) || math.IsInf(p.scaleFactor, 0) || p.scaleFactor <= 0 {
		return fmt.Errorf("scale must be finite and greater than zero")
	}
	if p.windowWidth < 1 || p.windowHeight < 1 ||
		p.windowWidth > maxPhysicalDim || p.windowHeight > maxPhysicalDim ||
		float64(p.windowWidth)*p.scaleFactor > float64(maxPhysicalDim) ||
		float64(p.windowHeight)*p.scaleFactor > float64(maxPhysicalDim) {
		return fmt.Errorf("viewport dimensions must be positive and fit within %d CSS and physical pixels", maxPhysicalDim)
	}
	if p.waitSeconds < 0 || int64(p.waitSeconds) > maxWaitSeconds {
		return fmt.Errorf("wait is out of range")
	}
	for i, sel := range p.clickSelectors {
		if strings.TrimSpace(sel) == "" {
			return fmt.Errorf("click %d: selector must not be empty", i+1)
		}
	}
	return nil
}

func init() {
	defineFlagSlice("u", "url", Req+"URL (can be specified multiple times, e.g. -u \"https://xxxx/\" -u \"https://yyyy/\")", &urls)
	defineFlagSlice("c", "chrome-flag", "Extra Chrome flag as key=value (can be specified multiple times, e.g. -c \"lang=ja\" -c \"disable-extensions\").", &chromeFlags)
	defineFlagSlice("k", "click", "Click the first matching element before capture; repeat to click selectors in order (e.g. -k .menu-button -k .menu-item)", &clickSelectors)
	flag.Usage = customUsage(commandDescription)
}

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Restore default signal handling after the first signal; a second forces exit.
	stopOnSignal := context.AfterFunc(ctx, stop)
	err := run(ctx)
	stopOnSignal()
	stop()
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run owns the browser and profile for both CLI and MCP execution.
func run(ctx context.Context) error {
	if *arguments.parallel < 1 {
		return fmt.Errorf("parallel must be at least 1")
	}
	if *arguments.timeoutSeconds < 1 || int64(*arguments.timeoutSeconds) > maxWaitSeconds {
		return fmt.Errorf("timeout is out of range")
	}
	for _, cf := range chromeFlags {
		k, v, _ := strings.Cut(cf, "=")
		if k == "device-scale-factor" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("invalid device-scale-factor: %q", v)
			}
			deviceScaleFactor = f
		}
	}
	params := captureParamsFromArgs()
	if err := params.validate(); err != nil {
		return err
	}
	if !*arguments.mcpMode {
		if len(urls) == 0 || strings.TrimSpace(*arguments.outputPath) == "" {
			flag.Usage()
			return fmt.Errorf("-u and -o are required")
		}
		for _, u := range urls {
			if strings.TrimSpace(u) == "" {
				return fmt.Errorf("URLs must not be empty")
			}
		}
	}

	profileCacheDir, err := setupProfileCache()
	if err != nil {
		return err
	}
	defer cleanupProfileCache(profileCacheDir)
	if err := ctx.Err(); err != nil {
		return err
	}
	browserCtx, shutdown := newBrowserContext(profileCacheDir)
	defer shutdown()
	// Allocate on the root context; cancelling an allocation context kills Chrome.
	if err := chromedp.Run(browserCtx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sem := make(chan struct{}, *arguments.parallel)
	if *arguments.mcpMode {
		return runMCPServer(ctx, browserCtx, sem)
	}
	logSettings(profileCacheDir)
	results := make(chan error, len(urls))
	for i, u := range urls {
		go func() {
			log.Printf("[%d/%d] capturing: %s", i+1, len(urls), u)
			buf, err := captureTab(ctx, browserCtx, sem, u, params)
			if err == nil {
				err = saveImage(outputPath(i), buf)
			}
			if err != nil {
				err = fmt.Errorf("capture %s: %w", u, err)
			}
			results <- err
		}()
	}
	var failures []error
	for range urls {
		if err := <-results; err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func saveImage(path string, buf []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	log.Printf("saved screenshot: %s", path)
	return nil
}

// captureTab gives every URL its own tab and propagates request cancellation.
func captureTab(ctx, browserCtx context.Context, sem chan struct{}, url string, p captureParams) ([]byte, error) {
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()
	timeout := p.timeout
	if timeout == 0 {
		timeout = captureTimeout
	}
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return takeScreenshot(tabCtx, url, p)
}

// outputPath returns the output file path for the i-th URL.
// Single URL: uses -o as-is. Multiple URLs: <base>_001.png, _002.png, ...
func outputPath(index int) string {
	if len(urls) == 1 {
		return *arguments.outputPath
	}
	ext := filepath.Ext(*arguments.outputPath)
	base := strings.TrimSuffix(*arguments.outputPath, ext)
	return fmt.Sprintf("%s_%03d%s", base, index+1, ext)
}

// setupProfileCache publishes only complete copies and locks reusable caches.
func setupProfileCache() (cacheDir string, err error) {
	if *arguments.profileDir == "" {
		return "", nil
	}
	source, err := filepath.Abs(*arguments.profileDir)
	if err != nil {
		return "", err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	*arguments.profileDir = source
	profileName := filepath.Base(source)
	root := ""
	if *arguments.reUseProfile {
		root, err = chromeProfileCacheRoot()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
		cacheDir = filepath.Join(root, fmt.Sprintf("userdata-%x", sha256.Sum256([]byte(source))))
		lockPath := cacheDir + ".lock"
		// ponytail: crash locks require manual removal; never steal a live cache.
		if err := os.Mkdir(lockPath, 0700); err != nil {
			return "", fmt.Errorf("cannot lock profile cache %s (in use or stale lock): %w", lockPath, err)
		}
		defer func() {
			if err != nil {
				os.Remove(lockPath)
			}
		}()
		info, statErr := os.Stat(filepath.Join(cacheDir, profileName))
		if statErr == nil && info.IsDir() {
			return cacheDir, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("invalid cached profile: %v", statErr)
		}
	}
	staging, err := os.MkdirTemp(root, "sesnap-userdata-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil || *arguments.reUseProfile {
			os.RemoveAll(staging)
		}
	}()
	if err := os.CopyFS(filepath.Join(staging, profileName), os.DirFS(source)); err != nil {
		return "", fmt.Errorf("copy profile: %w", err)
	}
	if *arguments.reUseProfile {
		if err := os.Rename(staging, cacheDir); err != nil {
			return "", err
		}
		return cacheDir, nil
	}
	return staging, nil
}

// newBrowserContext creates a chromedp browser context with the configured options.
// userDataDir is the profile cache directory returned by setupProfileCache (empty if no profile).
// Returns the context and a shutdown function that can be called multiple times safely.
func newBrowserContext(userDataDir string) (context.Context, func()) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", !*arguments.noHeadless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)

	// Apply extra Chrome flags from -c options
	for _, cf := range chromeFlags {
		k, v, _ := strings.Cut(cf, "=")
		if v == "" {
			opts = append(opts, chromedp.Flag(k, true))
		} else {
			opts = append(opts, chromedp.Flag(k, v))
		}
	}

	if userDataDir != "" {
		profileName := filepath.Base(*arguments.profileDir)
		opts = append(opts,
			chromedp.Flag("user-data-dir", userDataDir),
			chromedp.Flag("profile-directory", profileName),
			chromedp.Flag("use-mock-keychain", false),
			chromedp.Flag("password-store", "keychain"),
		)
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	var ctxOpts []chromedp.ContextOption
	if *arguments.debug {
		ctxOpts = append(ctxOpts, chromedp.WithDebugf(log.Printf))
	} else {
		ctxOpts = append(ctxOpts, chromedp.WithLogf(log.Printf))
	}

	browserCtx, browserCancel := chromedp.NewContext(allocCtx, ctxOpts...)

	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(browserCtx, cleanupTimeout)
			defer closeCancel()
			if c := chromedp.FromContext(browserCtx); c.Browser != nil {
				closeAllTargets(closeCtx)
				if err := chromedp.Cancel(closeCtx); err != nil {
					log.Printf("graceful browser close failed: %v", err)
				}
			}
			browserCancel()
			allocCancel()
		})
	}

	return browserCtx, shutdown
}

// takeScreenshot navigates to the URL and captures a screenshot.
// All chromedp actions run in a single Run call to avoid race conditions
// when multiple tabs operate concurrently.
func takeScreenshot(ctx context.Context, url string, p captureParams) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	// Build and execute all tasks in one Run call
	tasks := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(
			p.windowWidth,
			p.windowHeight,
			p.scaleFactor,
			false,
		),
		chromedp.ActionFunc(func(ctx context.Context) error { return navigate(ctx, url) }),
		chromedp.Sleep(time.Duration(p.waitSeconds) * time.Second),
	}

	// Wait for each target so earlier clicks can reveal later targets.
	for i, sel := range p.clickSelectors {
		tasks = append(tasks,
			chromedp.ActionFunc(func(ctx context.Context) error {
				err := chromedp.Tasks{
					chromedp.WaitVisible(sel, chromedp.ByQuery),
					chromedp.Click(sel, chromedp.ByQuery),
					chromedp.Sleep(500 * time.Millisecond),
				}.Do(ctx)
				if err != nil {
					return fmt.Errorf("click %d (%q): %w", i+1, sel, err)
				}
				return nil
			}),
		)
	}
	// Hover action before capture (e.g. trigger tooltip or :hover style)
	if p.hoverSelector != "" {
		sel := p.hoverSelector
		tasks = append(tasks,
			chromedp.WaitVisible(sel, chromedp.ByQuery),
			chromedp.ActionFunc(func(ctx context.Context) error {
				var coords []float64
				literal, _ := json.Marshal(sel)
				if err := chromedp.Evaluate(
					`(function(){var r=document.querySelector(`+string(literal)+`).getBoundingClientRect();return[r.left+r.width/2,r.top+r.height/2]})()`,
					&coords,
				).Do(ctx); err != nil {
					return err
				}
				return input.DispatchMouseEvent(input.MouseMoved, coords[0], coords[1]).Do(ctx)
			}),
			chromedp.Sleep(300*time.Millisecond),
		)
	}
	// Expand <select> elements as HTML dropdown overlays
	if p.expandSelect != "" {
		sel := p.expandSelect
		tasks = append(tasks,
			chromedp.ActionFunc(func(ctx context.Context) error {
				return expandSelectElements(ctx, sel)
			}),
			chromedp.Sleep(100*time.Millisecond),
		)
	}

	var buf []byte
	switch {
	case p.fullScreenshot:
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			data, err := captureFullPage(ctx, p)
			if err != nil {
				return err
			}
			buf = data
			return nil
		}))
	case p.querySelector != "":
		qs := p.querySelector
		tasks = append(tasks,
			chromedp.WaitVisible(qs, chromedp.ByQuery),
			chromedp.ActionFunc(func(ctx context.Context) error {
				data, err := captureElement(ctx, qs, p)
				if err != nil {
					return err
				}
				buf = data
				return nil
			}),
		)
	default:
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			data, err := captureViewport(ctx, nil)
			if err != nil {
				return err
			}
			buf = data
			return nil
		}))
	}

	if err := chromedp.Run(ctx, tasks); err != nil {
		return nil, err
	}
	if p.showAddressBar {
		combined, err := addAddressBar(ctx, url, buf, p.scaleFactor)
		if err != nil {
			return nil, err
		}
		buf = combined
	}
	return buf, nil
}

// navigate waits for this loader to commit and parse, not for all resources to load.
func navigate(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, navigationTimeout)
	defer cancel()
	frameID, loaderID, errorText, _, err := page.Navigate(url).Do(ctx)
	if err != nil {
		return err
	}
	if errorText != "" {
		return fmt.Errorf("navigate %s: %s", url, errorText)
	}
	for {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		if tree.Frame.ID == frameID && (loaderID == "" || tree.Frame.LoaderID == loaderID) {
			var state string
			if err := chromedp.Evaluate(`document.readyState`, &state).Do(ctx); err != nil {
				return err
			}
			if state == "interactive" || state == "complete" {
				return nil
			}
		}
		if err := chromedp.Sleep(100 * time.Millisecond).Do(ctx); err != nil {
			return fmt.Errorf("wait for navigation %s: %w", url, err)
		}
	}
}

// captureFullPage resizes the viewport to the full page dimensions and takes
// a normal viewport screenshot. This avoids captureBeyondViewport which is
// unreliable when multiple tabs capture concurrently.
func captureFullPage(ctx context.Context, p captureParams) ([]byte, error) {
	var dims []int64
	if err := chromedp.Evaluate(
		`[document.documentElement.scrollWidth, Math.max(document.documentElement.scrollHeight, document.body.scrollHeight)]`,
		&dims,
	).Do(ctx); err != nil {
		return nil, err
	}
	w := max(p.windowWidth, dims[0])
	h := dims[1]
	if err := emulation.SetDeviceMetricsOverride(
		clampDim(w, p.scaleFactor), clampDim(h, p.scaleFactor), p.scaleFactor, false,
	).Do(ctx); err != nil {
		return nil, err
	}
	return captureViewport(ctx, nil)
}

// captureElement expands the viewport to contain the target element, then
// captures it with a clip rect. This avoids captureBeyondViewport which is
// unreliable when multiple tabs capture concurrently.
// Caller must ensure the selector is already present/visible (e.g. via WaitVisible).
func captureElement(ctx context.Context, selector string, p captureParams) ([]byte, error) {
	x, y, w, h, err := getElementRect(ctx, selector)
	if err != nil {
		return nil, err
	}
	// Reset scroll so document-coordinate clips stay within the resized viewport.
	if err := chromedp.Evaluate(`window.scrollTo({left:0,top:0,behavior:'instant'})`, nil).Do(ctx); err != nil {
		return nil, err
	}
	// Expand viewport so the element is fully visible
	needW := max(p.windowWidth, int64(math.Ceil(x+w)))
	needH := max(p.windowHeight, int64(math.Ceil(y+h)))
	if err := emulation.SetDeviceMetricsOverride(
		clampDim(needW, p.scaleFactor), clampDim(needH, p.scaleFactor), p.scaleFactor, false,
	).Do(ctx); err != nil {
		return nil, err
	}
	// Re-read rect after viewport resize (layout may shift)
	x, y, w, h, err = getElementRect(ctx, selector)
	if err != nil {
		return nil, err
	}
	rx, ry := math.Round(x), math.Round(y)
	width := min(math.Round(w+x-rx), float64(clampDim(needW, p.scaleFactor))-rx)
	height := min(math.Round(h+y-ry), float64(clampDim(needH, p.scaleFactor))-ry)
	if rx < 0 || ry < 0 || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("element is outside the capture viewport")
	}
	return captureViewport(ctx, &page.Viewport{
		X: rx, Y: ry,
		Width:  width,
		Height: height,
		Scale:  1,
	})
}

// captureViewport takes a PNG screenshot of the current viewport.
// If clip is non-nil, only the specified region is captured.
func captureViewport(ctx context.Context, clip *page.Viewport) ([]byte, error) {
	action := page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithCaptureBeyondViewport(false)
	if clip != nil {
		action = action.WithClip(clip)
	}
	return action.Do(ctx)
}

// expandSelectElements injects JavaScript that replaces <select> elements
// matching the given CSS selector with expanded HTML dropdown overlays.
// Use "*" to expand all <select> elements on the page.
func expandSelectElements(ctx context.Context, selector string) error {
	jsSelector := selector
	if selector == "*" {
		jsSelector = "select"
	}
	js := `(function(sel) {
  var selects = document.querySelectorAll(sel);
  for (var i = 0; i < selects.length; i++) {
    var s = selects[i];
    if (s.tagName !== 'SELECT') continue;
    var rect = s.getBoundingClientRect();
    var cs = window.getComputedStyle(s);
    var selected = s.selectedIndex;

    // Create overlay container
    var overlay = document.createElement('div');
    overlay.className = '__sesnap-select-overlay';
    overlay.style.cssText = 'position:absolute;z-index:2147483647;' +
      'left:' + (rect.left + window.scrollX) + 'px;' +
      'top:' + (rect.bottom + window.scrollY) + 'px;' +
      'min-width:' + rect.width + 'px;' +
      'background:#fff;' +
      'border:1px solid #c0c0c0;' +
      'border-radius:4px;' +
      'box-shadow:0 2px 8px rgba(0,0,0,0.15);' +
      'padding:1px 0;' +
      'font-family:' + cs.fontFamily + ';' +
      'font-size:' + cs.fontSize + ';' +
      'box-sizing:border-box;' +
      'max-height:400px;overflow-y:auto;';

    for (var j = 0; j < s.options.length; j++) {
      var item = s.options[j];
      var div = document.createElement('div');
      div.textContent = item.text;
      var bgColor = j === selected ? '#0b57d0' : '#fff';
      var fgColor = j === selected ? '#fff' : '#202124';
      if (item.disabled) { fgColor = '#9e9e9e'; }
      div.style.cssText = 'padding:4px 8px;' +
        'white-space:nowrap;' +
        'cursor:default;' +
        'background:' + bgColor + ';' +
        'color:' + fgColor + ';';
      overlay.appendChild(div);
    }

    // Highlight the select element itself
    s.style.outline = '2px solid #0b57d0';
    s.style.outlineOffset = '-1px';

    document.body.appendChild(overlay);
  }
})`
	literal, _ := json.Marshal(jsSelector)
	js += "(" + string(literal) + ")"

	return chromedp.Evaluate(js, nil).Do(ctx)
}

// getElementRect returns the document rect of the first element
// matching the given CSS selector.
func getElementRect(ctx context.Context, selector string) (x, y, w, h float64, err error) {
	var rect []float64
	literal, _ := json.Marshal(selector)
	if err = chromedp.Evaluate(
		`(function(){var r=document.querySelector(`+string(literal)+`).getBoundingClientRect();return[r.x+window.scrollX,r.y+window.scrollY,r.width,r.height]})()`,
		&rect,
	).Do(ctx); err != nil {
		return
	}
	return rect[0], rect[1], rect[2], rect[3], nil
}

// addAddressBar renders a browser-style address bar with favicon and URL using
// chromedp, then stitches it on top of the page screenshot.
func addAddressBar(ctx context.Context, pageURL string, pageBuf []byte, scaleFactor float64) ([]byte, error) {
	// Get favicon as a data URI from current page (still on the target page).
	// Collects all favicon candidates from the page, tries data: URIs first
	// (no fetch needed), then attempts each URL-based candidate with fetch,
	// falling back to /favicon.ico as a last resort.
	//
	// Covered patterns:
	//   <link rel="icon" href="...">              — standard
	//   <link rel="shortcut icon" href="...">     — legacy
	//   <link rel="apple-touch-icon" href="...">  — iOS
	//   <link rel="icon" href="data:image/...">   — embedded data URI
	//   /favicon.ico                              — convention fallback
	var faviconDataURL string
	if err := chromedp.Run(ctx, chromedp.EvaluateAsDevTools(`
(async function() {
  // Collect all favicon candidates from <link> tags, then /favicon.ico
  var cs = [];
  document.querySelectorAll('link[rel*="icon"]').forEach(function(el) {
    if (el.href) cs.push(el.href);
  });
  cs.push(location.origin + '/favicon.ico');

  // Deduplicate while preserving order
  var seen = {};
  cs = cs.filter(function(u) {
    if (seen[u]) return false;
    seen[u] = true;
    return true;
  });

  // stripDarkMode removes @media (prefers-color-scheme: dark) blocks from SVG
  // to prevent favicons from rendering as white-on-white in the address bar.
  function stripDarkMode(svgText) {
    return svgText.replace(/@media\s*\(\s*prefers-color-scheme:\s*dark\s*\)\s*\{[^}]*\{[^}]*\}\s*\}/g, '');
  }

  // Try data: URIs first (instant, no fetch needed)
  for (var i = 0; i < cs.length; i++) {
    if (!cs[i].startsWith('data:')) continue;
    if (cs[i].indexOf('image/svg') !== -1) {
      try {
        var svgContent;
        if (cs[i].indexOf(';base64,') !== -1) {
          svgContent = atob(cs[i].split(';base64,')[1]);
        } else {
          svgContent = decodeURIComponent(cs[i].split(',').slice(1).join(','));
        }
        return 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(stripDarkMode(svgContent))));
      } catch(e) { return cs[i]; }
    }
    return cs[i];
  }

  // Try each URL-based candidate
  // If fetch succeeds: convert to data URL (SVG: strip dark mode first)
  // If fetch fails (CORS etc.): use the URL directly in <img> tag
  for (var i = 0; i < cs.length; i++) {
    if (cs[i].startsWith('data:')) continue;
    try {
      var r = await fetch(cs[i]);
      if (!r.ok) continue;
      var b = await r.blob();
      if (b.size === 0) continue;
      if (b.type === 'image/svg+xml' || cs[i].endsWith('.svg')) {
        var svgText = await b.text();
        return 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(stripDarkMode(svgText))));
      }
      var result = await new Promise(function(ok) {
        var rd = new FileReader();
        rd.onload = function() { ok(rd.result); };
        rd.onerror = rd.onabort = function() { ok(''); };
        rd.readAsDataURL(b);
      });
      if (result) return result;
    } catch (e) {
      // CORS or network error: return the URL directly for <img> tag
      return cs[i];
    }
  }
  return '';
})()`,
		&faviconDataURL,
		func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
			return p.WithAwaitPromise(true)
		},
	)); err != nil {
		faviconDataURL = ""
	}

	// Decode page screenshot to get pixel dimensions
	pageImg, err := png.Decode(bytes.NewReader(pageBuf))
	if err != nil {
		return nil, fmt.Errorf("decode page screenshot: %w", err)
	}
	pageW := pageImg.Bounds().Dx()

	// Calculate CSS width to match the page screenshot pixel width
	cssW := int64(math.Round(float64(pageW) / scaleFactor))
	const barCSSH int64 = 52

	// Build address bar HTML and capture it in the same tab
	barHTML := buildAddressBarHTML(pageURL, faviconDataURL)
	dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(barHTML))

	var barBuf []byte
	if err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(cssW, barCSSH, scaleFactor, false),
		chromedp.Navigate(dataURL),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.ActionFunc(func(ctx context.Context) error {
			data, err := captureViewport(ctx, nil)
			if err != nil {
				return err
			}
			barBuf = data
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("capture address bar: %w", err)
	}

	// Decode bar screenshot
	barImg, err := png.Decode(bytes.NewReader(barBuf))
	if err != nil {
		return nil, fmt.Errorf("decode bar screenshot: %w", err)
	}

	// Stitch: bar on top, page below
	barB := barImg.Bounds()
	pageB := pageImg.Bounds()
	result := image.NewRGBA(image.Rect(0, 0, pageB.Dx(), barB.Dy()+pageB.Dy()))
	draw.Draw(result, image.Rect(0, 0, barB.Dx(), barB.Dy()), barImg, barB.Min, draw.Src)
	draw.Draw(result, image.Rect(0, barB.Dy(), pageB.Dx(), barB.Dy()+pageB.Dy()), pageImg, pageB.Min, draw.Src)

	var out bytes.Buffer
	if err := png.Encode(&out, result); err != nil {
		return nil, fmt.Errorf("encode combined screenshot: %w", err)
	}
	return out.Bytes(), nil
}

// buildAddressBarHTML returns an HTML page that renders a browser-style address bar.
func buildAddressBarHTML(pageURL, faviconURL string) string {
	escapedURL := html.EscapeString(pageURL)
	imgTag := ""
	if faviconURL != "" {
		imgTag = fmt.Sprintf(`<img src="%s" width="16" height="16" style="margin-right:8px;flex-shrink:0" onerror="this.style.display='none'">`, html.EscapeString(faviconURL))
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><head><style>*{margin:0;padding:0;box-sizing:border-box}`+
		`body{background:#dee1e6;display:flex;align-items:center;height:52px;padding:0 8px}`+
		`.bar{display:flex;align-items:center;flex:1;height:36px;padding:0 12px;background:#fff;border-radius:24px;`+
		`font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;overflow:hidden}`+
		`.url{color:#202124;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}`+
		`</style></head><body><div class="bar">%s<span class="url">%s</span></div></body></html>`, imgTag, escapedURL)
}

// clampDim clamps a CSS dimension to Chrome's max texture size to avoid tiling artifacts.
// The GPU limit is in physical pixels, so we divide by deviceScaleFactor.
func clampDim(v int64, scaleFactor float64) int64 {
	return int64(min(float64(v), math.Floor(float64(maxPhysicalDim)/scaleFactor)))
}

// closeAllTargets closes all page targets via CDP so no tabs remain in the
// session history. This prevents leftover tabs when reusing a profile with -r.
func closeAllTargets(ctx context.Context) {
	targets, err := chromedp.Targets(ctx)
	if err != nil {
		return
	}
	c := chromedp.FromContext(ctx)
	if c == nil || c.Browser == nil {
		return
	}
	for _, t := range targets {
		if t.Type == "page" {
			target.CloseTarget(t.TargetID).Do(cdp.WithExecutor(ctx, c.Browser))
		}
	}
}

// cleanupProfileCache releases reusable caches or deletes temporary copies.
func cleanupProfileCache(cacheDir string) {
	if cacheDir == "" {
		return
	}
	if *arguments.reUseProfile {
		if err := os.Remove(cacheDir + ".lock"); err != nil {
			log.Printf("release profile cache: %v", err)
		}
		return
	}
	log.Printf("delete cached profile: %s", cacheDir)
	if err := os.RemoveAll(cacheDir); err != nil {
		log.Printf("failed to delete cached profile: %v", err)
	}
}

// chromeProfileCacheRoot returns the root directory for cached Chrome profiles.
// Can be overridden by the SESNAP_CACHE_DIR environment variable.
func chromeProfileCacheRoot() (string, error) {
	if dir := os.Getenv("SESNAP_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".sesnap"), nil
}

func logSettings(profileCacheDir string) {
	for i, u := range urls {
		log.Printf("         url[%d]: %s", i, u)
	}
	log.Printf("          query: %s", *arguments.querySelector)
	log.Printf("         output: %s", *arguments.outputPath)
	log.Printf("    profile dir: %s", *arguments.profileDir)
	log.Printf("       viewport: %dx%d", *arguments.windowWidth, *arguments.windowHeight)
	log.Printf("          hover: %s", *arguments.hoverSelector)
	log.Printf("         clicks: %v", clickSelectors)
	log.Printf("  expand-select: %s", *arguments.expandSelect)
	log.Printf("   scale factor: %.1f", deviceScaleFactor)
	log.Printf("full screenshot: %v", *arguments.fullScreenshot)
	log.Printf("       headless: %v", !*arguments.noHeadless)
	log.Printf("       parallel: %d", *arguments.parallel)
	log.Printf("   wait seconds: %d", *arguments.waitSeconds)
	for i, cf := range chromeFlags {
		log.Printf("  chrome flag[%d]: %s", i, cf)
	}
	if profileCacheDir != "" {
		log.Printf("  profile cache: %s", profileCacheDir)
		log.Printf("   save profile: %v", *arguments.reUseProfile)
	}
}

// =======================================
// flag Utils
// =======================================

// defineFlagSlice registers a stringSlice flag with both short and long names.
func defineFlagSlice(short, long, description string, s *stringSlice) {
	flagUsage := short + UsageDummy + description
	flag.Var(s, long, flagUsage)
	flag.Var(s, short, UsageDummy)
}

// Helper function for flag
func defineFlagValue[T comparable](short, long string, defaultValue T, description string, flagFunc func(name string, value T, usage string) *T, flagVarFunc func(p *T, name string, value T, usage string)) *T {
	flagUsage := short + UsageDummy + description
	var zero T
	if defaultValue != zero {
		flagUsage = flagUsage + fmt.Sprintf(" (default %v)", defaultValue)
	}
	f := flagFunc(long, defaultValue, flagUsage)
	flagVarFunc(f, short, defaultValue, UsageDummy)
	return f
}

// Custom usage message
func customUsage(description string) func() {
	return func() {
		optionsUsage, requiredOptionExample := getOptionsUsage()
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s %s[OPTIONS]\n\n", func() string { e, _ := os.Executable(); return filepath.Base(e) }(), requiredOptionExample)
		fmt.Fprintf(flag.CommandLine.Output(), "Description:\n  %s\n\n", description)
		fmt.Fprintf(flag.CommandLine.Output(), "Options:\n%s", optionsUsage)
	}
}

// Get options usage message
func getOptionsUsage() (string, string) {
	requiredOptionExample := ""
	optionNameWidth := 0
	usages := make([]string, 0)
	flag.VisitAll(func(f *flag.Flag) {
		name, _ := flag.UnquoteUsage(f)
		optionNameWidth = max(optionNameWidth, len(f.Name)+len(name)+5)
	})
	flag.VisitAll(func(f *flag.Flag) {
		if f.Usage == UsageDummy {
			return
		}
		value, _ := flag.UnquoteUsage(f)
		short, mainUsage, _ := strings.Cut(f.Usage, UsageDummy)
		if strings.Contains(mainUsage, Req) {
			requiredOptionExample += fmt.Sprintf("--%s %s ", f.Name, value)
		}
		usages = append(usages, fmt.Sprintf("  -%-1s, --%-"+strconv.Itoa(optionNameWidth)+"s %s\n", short, f.Name+" "+value, mainUsage))
	})
	sort.SliceStable(usages, func(i, j int) bool {
		return strings.Count(usages[i], Req) > strings.Count(usages[j], Req)
	})
	return strings.Join(usages, ""), requiredOptionExample
}

// =======================================
// MCP Server
// =======================================

func runMCPServer(ctx, browserCtx context.Context, sem chan struct{}) error {
	s := server.NewMCPServer(
		"sesnap",
		version,
		server.WithToolCapabilities(false),
	)

	// Shared inputs keep both screenshot tools consistent.
	options := []mcp.ToolOption{
		mcp.WithArray("urls",
			mcp.Description("URLs to capture"),
			mcp.WithStringItems(),
			mcp.Required(),
			mcp.MinItems(1),
		),
		mcp.WithNumber("width", mcp.Description("Viewport width in CSS pixels (default: 1280)")),
		mcp.WithNumber("height", mcp.Description("Viewport height in CSS pixels (default: 860)")),
		mcp.WithBoolean("full", mcp.Description("Enable full-page screenshot (default: false)")),
		mcp.WithString("query", mcp.Description("CSS selector to screenshot a specific element")),
		mcp.WithNumber("wait", mcp.Description("Wait seconds after navigation before capture (default: 3)")),
		mcp.WithString("click", mcp.Description("Click the first element matching this CSS selector before capture")),
		mcp.WithArray("clicks", mcp.Description("CSS selectors to click in order before capture; cannot be combined with click"), mcp.WithStringItems()),
		mcp.WithString("hover", mcp.Description("Hover over the first element matching this CSS selector before capture")),
		mcp.WithString("expand_select", mcp.Description("Expand <select> elements as HTML overlay. Use CSS selector or \"*\" for all")),
		mcp.WithBoolean("address_bar", mcp.Description("Add browser-style address bar to screenshot (default: false)")),
		mcp.WithString("format", mcp.Description("Image format: \"png\" or \"jpeg\" (default: \"png\")")),
		mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default: 80, only used with format=jpeg)")),
		mcp.WithNumber("scale", mcp.Description("Device scale factor (default: 1.0)")),
		mcp.WithNumber("timeout", mcp.Description("Timeout seconds per URL (default: 60)")),
	}
	s.AddTool(mcp.NewTool("screenshot", append(options, mcp.WithDescription("Take screenshots of web pages and return images directly."))...), mcpScreenshotHandler(browserCtx, sem, false))

	// screenshot_to_file: capture URLs and save to files (no image tokens)
	s.AddTool(
		mcp.NewTool("screenshot_to_file", append(options,
			mcp.WithDescription("Take screenshots of web pages and save to files. Returns file paths instead of images."),
			mcp.WithString("output", mcp.Description("Output file path (auto-numbered for multiple URLs: <base>_001.png, _002.png, ...)"), mcp.Required()),
		)...),
		mcpScreenshotHandler(browserCtx, sem, true),
	)

	// list_profiles: list available Chrome profile directories
	s.AddTool(
		mcp.NewTool("list_profiles",
			mcp.WithDescription("List available Chrome profile directories for use with the -p flag"),
		),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			profiles := listChromeProfiles()
			if len(profiles) == 0 {
				return mcp.NewToolResultText("No Chrome profiles found."), nil
			}
			var sb strings.Builder
			for _, p := range profiles {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", p.name, p.path))
			}
			return mcp.NewToolResultText(sb.String()), nil
		},
	)

	// --- 6. Start stdio server ---
	return server.NewStdioServer(s).Listen(ctx, os.Stdin, os.Stdout)
}

// mcpCaptureParams builds captureParams from MCP tool request arguments.
func mcpCaptureParams(request mcp.CallToolRequest) (captureParams, error) {
	args := request.GetArguments()
	values := map[string]float64{}
	for _, spec := range []struct {
		key                string
		fallback, min, max float64
	}{
		{"width", float64(defaultWidth), 1, float64(maxPhysicalDim)},
		{"height", float64(defaultHeight), 1, float64(maxPhysicalDim)},
		{"wait", defaultWait, 0, float64(maxWaitSeconds)},
		{"timeout", captureTimeout.Seconds(), 1, float64(maxWaitSeconds)},
		{"quality", defaultQuality, 1, 100},
		{"scale", 1, 0, float64(maxPhysicalDim)},
	} {
		value := spec.fallback
		if raw, ok := args[spec.key]; ok {
			switch v := raw.(type) {
			case float64:
				value = v
			case int:
				value = float64(v)
			default:
				return captureParams{}, fmt.Errorf("%s must be a number", spec.key)
			}
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < spec.min || value > spec.max ||
			(spec.key != "scale" && math.Trunc(value) != value) {
			return captureParams{}, fmt.Errorf("%s is out of range or not an integer", spec.key)
		}
		values[spec.key] = value
	}
	for _, key := range []string{"query", "click", "hover", "expand_select", "format", "output"} {
		if raw, ok := args[key]; ok {
			if _, ok := raw.(string); !ok {
				return captureParams{}, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	for _, key := range []string{"full", "address_bar"} {
		if raw, ok := args[key]; ok {
			if _, ok := raw.(bool); !ok {
				return captureParams{}, fmt.Errorf("%s must be a boolean", key)
			}
		}
	}
	_, hasClick := args["click"]
	_, hasClicks := args["clicks"]
	if hasClick && hasClicks {
		return captureParams{}, fmt.Errorf("click and clicks cannot be specified together")
	}

	var selectors []string
	if hasClicks {
		var err error
		selectors, err = request.RequireStringSlice("clicks")
		if err != nil {
			return captureParams{}, err
		}
	} else if hasClick {
		sel, err := request.RequireString("click")
		if err != nil {
			return captureParams{}, err
		}
		if sel != "" {
			selectors = []string{sel}
		}
	}

	p := captureParams{
		windowWidth:    int64(values["width"]),
		windowHeight:   int64(values["height"]),
		waitSeconds:    int(values["wait"]),
		querySelector:  request.GetString("query", ""),
		clickSelectors: selectors,
		hoverSelector:  request.GetString("hover", ""),
		expandSelect:   request.GetString("expand_select", ""),
		fullScreenshot: request.GetBool("full", false),
		showAddressBar: request.GetBool("address_bar", false),
		scaleFactor:    values["scale"],
		timeout:        time.Duration(values["timeout"]) * time.Second,
	}
	return p, p.validate()
}

// mcpScreenshotHandler returns a tool handler that captures screenshots.
// If toFile is true, screenshots are saved to disk; otherwise returned as base64 images.
func mcpScreenshotHandler(browserCtx context.Context, sem chan struct{}, toFile bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reqURLs, err := request.RequireStringSlice("urls")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(reqURLs) == 0 {
			return mcp.NewToolResultError("urls must not be empty"), nil
		}
		for _, url := range reqURLs {
			if strings.TrimSpace(url) == "" {
				return mcp.NewToolResultError("URLs must not be empty"), nil
			}
		}

		p, err := mcpCaptureParams(request)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		format := request.GetString("format", "png")
		quality := int(request.GetFloat("quality", defaultQuality))
		if format != "png" && format != "jpeg" {
			return mcp.NewToolResultError("format must be png or jpeg"), nil
		}

		var outputBase string
		if toFile {
			outputBase, err = request.RequireString("output")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if strings.TrimSpace(outputBase) == "" {
				return mcp.NewToolResultError("output must not be empty"), nil
			}
		}

		type captureResult struct {
			index int
			buf   []byte
			err   error
		}
		results := make(chan captureResult, len(reqURLs))

		for i, u := range reqURLs {
			go func(i int, u string) {
				buf, captureErr := captureTab(ctx, browserCtx, sem, u, p)
				results <- captureResult{i, buf, captureErr}
			}(i, u)
		}

		// Collect results in order
		ordered := make([]captureResult, len(reqURLs))
		for range reqURLs {
			r := <-results
			ordered[r.index] = r
		}

		// Build response
		var content []mcp.Content
		failed := false
		for i, r := range ordered {
			if r.err != nil {
				failed = true
				content = append(content, mcp.TextContent{
					Type: "text",
					Text: fmt.Sprintf("[%d] %s: error: %v", i+1, reqURLs[i], r.err),
				})
				continue
			}

			imgBuf := r.buf
			mimeType := "image/png"
			if format == "jpeg" {
				mimeType = "image/jpeg"
				imgBuf, err = convertToJPEG(r.buf, quality)
				if err != nil {
					failed = true
					content = append(content, mcp.TextContent{
						Type: "text",
						Text: fmt.Sprintf("[%d] %s: JPEG conversion error: %v", i+1, reqURLs[i], err),
					})
					continue
				}
			}

			if toFile {
				outPath := mcpOutputPath(outputBase, i, len(reqURLs), format)
				if err := saveImage(outPath, imgBuf); err != nil {
					failed = true
					content = append(content, mcp.TextContent{
						Type: "text",
						Text: fmt.Sprintf("[%d] %s: write error: %v", i+1, reqURLs[i], err),
					})
					continue
				}
				content = append(content, mcp.TextContent{
					Type: "text",
					Text: fmt.Sprintf("[%d] %s -> %s", i+1, reqURLs[i], outPath),
				})
			} else {
				encoded := base64.StdEncoding.EncodeToString(imgBuf)
				content = append(content, mcp.TextContent{
					Type: "text",
					Text: fmt.Sprintf("[%d] %s", i+1, reqURLs[i]),
				})
				content = append(content, mcp.ImageContent{
					Type:     "image",
					Data:     encoded,
					MIMEType: mimeType,
				})
			}
		}

		return &mcp.CallToolResult{Content: content, IsError: failed}, nil
	}
}

// mcpOutputPath returns the output file path for the i-th URL in MCP mode.
func mcpOutputPath(base string, index, total int, format string) string {
	ext := "." + format
	dir := filepath.Dir(base)
	name := filepath.Base(base)
	nameNoExt := strings.TrimSuffix(name, filepath.Ext(name))

	if total == 1 {
		// Ensure correct extension
		return filepath.Join(dir, nameNoExt+ext)
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%03d%s", nameNoExt, index+1, ext))
}

// convertToJPEG converts PNG image bytes to JPEG with the given quality.
func convertToJPEG(pngBuf []byte, quality int) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(pngBuf))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type chromeProfile struct {
	name string
	path string
}

// listChromeProfiles lists available Chrome profile directories.
func listChromeProfiles() []chromeProfile {
	dataDir := chromeUserDataDir()
	if dataDir == "" {
		return nil
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var profiles []chromeProfile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		prefPath := filepath.Join(dataDir, entry.Name(), "Preferences")
		if _, err := os.Stat(prefPath); err != nil {
			continue
		}
		profiles = append(profiles, chromeProfile{
			name: entry.Name(),
			path: filepath.Join(dataDir, entry.Name()),
		})
	}
	return profiles
}

// chromeUserDataDir returns the default Chrome user data directory for the current OS.
func chromeUserDataDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Google", "Chrome")
	case "linux":
		return filepath.Join(homeDir, ".config", "google-chrome")
	case "windows":
		return filepath.Join(homeDir, "AppData", "Local", "Google", "Chrome", "User Data")
	default:
		return ""
	}
}
