package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"image/png"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// helper: set package-level arguments for tests
// ---------------------------------------------------------------------------

func setOutputPath(v string) { arguments.outputPath = &v }
func setProfileDir(v string) { arguments.profileDir = &v }
func setReUseProfile(v bool) { arguments.reUseProfile = &v }
func setURLs(u ...string)    { urls = stringSlice(u) }

// ---------------------------------------------------------------------------
// stringSlice
// ---------------------------------------------------------------------------

func TestStringSlice_SetAndString(t *testing.T) {
	var ss stringSlice
	ss.Set("a")
	ss.Set("b")
	if got := ss.String(); got != "a, b" {
		t.Errorf("String() = %q, want %q", got, "a, b")
	}
}

func TestClickFlags(t *testing.T) {
	oldFlags, oldClicks := flag.CommandLine, clickSelectors
	t.Cleanup(func() { flag.CommandLine, clickSelectors = oldFlags, oldClicks })
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	clickSelectors = nil
	defineFlagSlice("k", "click", "", &clickSelectors)
	if err := flag.CommandLine.Parse([]string{"--click=.menu, .fallback", "--click=", "-k=.item", "--click=.menu, .fallback"}); err != nil {
		t.Fatal(err)
	}
	want := []string{".menu, .fallback", ".item", ".menu, .fallback"}
	if got := captureParamsFromArgs().clickSelectors; !reflect.DeepEqual(got, want) {
		t.Fatalf("clicks = %v, want %v", got, want)
	}
}

func TestMCPClicks(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
		fail bool
	}{
		{name: "omitted", args: map[string]any{}},
		{name: "legacy", args: map[string]any{"click": ".menu"}, want: []string{".menu"}},
		{name: "ordered", args: map[string]any{"clicks": []any{".menu", ".item", ".menu"}}, want: []string{".menu", ".item", ".menu"}},
		{name: "empty", args: map[string]any{"clicks": []any{}}, want: []string{}},
		{name: "both", args: map[string]any{"click": "", "clicks": []any{}}, fail: true},
		{name: "not array", args: map[string]any{"clicks": ".menu"}, fail: true},
		{name: "not string", args: map[string]any{"clicks": []any{".menu", 1}}, fail: true},
		{name: "empty legacy", args: map[string]any{"click": ""}},
		{name: "empty selector", args: map[string]any{"clicks": []any{""}}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req mcp.CallToolRequest
			req.Params.Arguments = tc.args
			p, err := mcpCaptureParams(req)
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v, want failure %v", err, tc.fail)
			}
			if !tc.fail && !reflect.DeepEqual(p.clickSelectors, tc.want) {
				t.Fatalf("clicks = %v, want %v", p.clickSelectors, tc.want)
			}
		})
	}
}

func TestWaitForInputs(t *testing.T) {
	oldFlags, oldSelector := flag.CommandLine, arguments.waitSelector
	t.Cleanup(func() { flag.CommandLine, arguments.waitSelector = oldFlags, oldSelector })
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	arguments.waitSelector = defineFlagValue("a", "wait-for", "", "", flag.String, flag.StringVar)
	if err := flag.CommandLine.Parse([]string{"--wait-for=#ready"}); err != nil {
		t.Fatal(err)
	}
	if got := captureParamsFromArgs().waitSelector; got != "#ready" {
		t.Fatalf("wait-for = %q", got)
	}
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"wait_for": "#ready"}
	p, err := mcpCaptureParams(req)
	if err != nil || p.waitSelector != "#ready" {
		t.Fatalf("MCP wait_for = %q, %v", p.waitSelector, err)
	}
}

// ---------------------------------------------------------------------------
// outputPath
// ---------------------------------------------------------------------------

func TestOutputPath_SingleURL(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "out.png")
	setOutputPath(outFile)
	setURLs("https://example.com")
	if got := outputPath(0); got != outFile {
		t.Errorf("outputPath(0) = %q, want %q", got, outFile)
	}
}

func TestOutputPath_MultipleURLs(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.png")
	setOutputPath(outFile)
	setURLs("https://a.com", "https://b.com", "https://c.com")

	tests := []struct {
		index int
		want  string
	}{
		{0, filepath.Join(dir, "out_001.png")},
		{1, filepath.Join(dir, "out_002.png")},
		{2, filepath.Join(dir, "out_003.png")},
	}
	for _, tt := range tests {
		if got := outputPath(tt.index); got != tt.want {
			t.Errorf("outputPath(%d) = %q, want %q", tt.index, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// chromeProfileCacheRoot
// ---------------------------------------------------------------------------

func TestChromeProfileCacheRoot_EnvVar(t *testing.T) {
	t.Setenv("SESNAP_CACHE_DIR", "/custom/cache")
	if got, err := chromeProfileCacheRoot(); err != nil || got != "/custom/cache" {
		t.Errorf("chromeProfileCacheRoot() = %q, %v, want /custom/cache", got, err)
	}
}

func TestChromeProfileCacheRoot_Default(t *testing.T) {
	t.Setenv("SESNAP_CACHE_DIR", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".sesnap")
	if got, err := chromeProfileCacheRoot(); err != nil || got != want {
		t.Errorf("chromeProfileCacheRoot() = %q, %v, want %q", got, err, want)
	}
}

// ---------------------------------------------------------------------------
// setupProfileCache
// ---------------------------------------------------------------------------

func TestSetupProfileCache_EmptyProfileDir(t *testing.T) {
	setProfileDir("")
	if got, err := setupProfileCache(); err != nil || got != "" {
		t.Errorf("setupProfileCache() = %q, %v, want empty string", got, err)
	}
}

func TestSetupProfileCache_CopiesProfileToTempDir(t *testing.T) {
	// Create a fake profile source directory with a marker file
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "Cookies"), []byte("data"), 0644)

	setProfileDir(srcDir)
	setReUseProfile(false)

	got, err := setupProfileCache()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(got)

	profileName := filepath.Base(srcDir)
	// Should be a temp directory, not under chromeProfileCacheRoot
	if got == "" {
		t.Fatal("setupProfileCache() returned empty string")
	}
	// Verify marker file was copied
	if _, err := os.Stat(filepath.Join(got, profileName, "Cookies")); err != nil {
		t.Errorf("Cookies file should exist in cached profile: %v", err)
	}
}

func TestSetupProfileCache_ReUseExisting(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("SESNAP_CACHE_DIR", cacheRoot)

	srcDir := t.TempDir()
	setProfileDir(srcDir)
	setReUseProfile(true)

	got, err := setupProfileCache()
	if err != nil {
		t.Fatal(err)
	}
	profileName := filepath.Base(srcDir)
	cachedDir := filepath.Join(got, profileName)
	os.WriteFile(filepath.Join(cachedDir, "Marker"), []byte("cached"), 0644)
	if _, err := setupProfileCache(); err == nil {
		t.Fatal("concurrent cache use should fail")
	}
	cleanupProfileCache(got)

	reused, err := setupProfileCache()
	if err != nil || reused != got {
		t.Fatalf("reuse = %q, %v, want %q", reused, err, got)
	}
	defer cleanupProfileCache(reused)
	// Marker should still exist (not re-copied)
	if _, err := os.Stat(filepath.Join(cachedDir, "Marker")); err != nil {
		t.Error("Marker should still exist in reuse mode")
	}
}

func TestSetupProfileCache_Isolation(t *testing.T) {
	t.Setenv("SESNAP_CACHE_DIR", t.TempDir())
	setReUseProfile(true)
	var caches []string
	for _, marker := range []string{"first", "second"} {
		source := filepath.Join(t.TempDir(), "Default")
		os.Mkdir(source, 0700)
		os.WriteFile(filepath.Join(source, "Cookies"), []byte(marker), 0600)
		setProfileDir(source)
		cache, err := setupProfileCache()
		if err != nil {
			t.Fatal(err)
		}
		caches = append(caches, cache)
		data, err := os.ReadFile(filepath.Join(cache, "Default", "Cookies"))
		if err != nil || string(data) != marker {
			t.Fatalf("cached cookies = %q, %v", data, err)
		}
		cleanupProfileCache(cache)
	}
	if caches[0] == caches[1] {
		t.Fatal("different sources must not share a cache")
	}
}

func TestSetupProfileCache_CopyFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESNAP_CACHE_DIR", root)
	setReUseProfile(true)
	// Unix socket paths have a small OS limit; keep the fixture path short.
	source, err := os.MkdirTemp("", "ss-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(source) })
	if err := os.WriteFile(filepath.Join(source, "Cookies"), []byte("partial copy"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(source, "socket"))
	if err != nil {
		t.Skipf("cannot create socket: %v", err)
	}
	defer listener.Close()
	setProfileDir(source)
	if _, err := setupProfileCache(); err == nil {
		t.Fatal("copy of socket must fail")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed copy left cache/lock behind: %v, %v", entries, err)
	}
}

// ---------------------------------------------------------------------------
// cleanupProfileCache
// ---------------------------------------------------------------------------

func TestCleanupProfileCache_EmptyDir(t *testing.T) {
	// Should not panic with empty string
	setReUseProfile(false)
	cleanupProfileCache("")
}

func TestCleanupProfileCache_DeletesWhenNotReuse(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "test")
	os.WriteFile(marker, []byte("x"), 0644)

	setReUseProfile(false)
	cleanupProfileCache(dir)

	if _, err := os.Stat(dir); err == nil {
		t.Error("cache dir should have been deleted")
	}
}

func TestCleanupProfileCache_KeepsWhenReuse(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "test")
	os.WriteFile(marker, []byte("x"), 0644)

	setReUseProfile(true)
	os.Mkdir(dir+".lock", 0700)
	cleanupProfileCache(dir)

	if _, err := os.Stat(marker); err != nil {
		t.Error("cache dir should have been kept in reuse mode")
	}
	if _, err := os.Stat(dir + ".lock"); !os.IsNotExist(err) {
		t.Fatal("cache lock should have been released")
	}
}

// ---------------------------------------------------------------------------
// E2E tests (Chrome required)
//
// Run manually:
//   SESNAP_E2E=1 go test -v -run TestE2E
// ---------------------------------------------------------------------------

func skipUnlessE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("SESNAP_E2E") == "" {
		t.Skip("Skipping E2E test; set SESNAP_E2E=1 to run")
	}
}

func TestE2E_SequentialClicks(t *testing.T) {
	skipUnlessE2E(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<button id="open" onclick="setTimeout(() => document.getElementById('item').hidden = false, 700)">Open</button>
<button id="item" class="item" hidden onclick="document.body.dataset.result = 'selected'; document.body.dataset.clicks = Number(document.body.dataset.clicks || 0) + 1">Select</button>
<button class="item" onclick="document.body.dataset.result = 'wrong item'">Other</button>`))
	}))
	defer srv.Close()

	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(browserCtx, 20*time.Second)
	defer cancel()
	p := captureParams{windowWidth: 800, windowHeight: 600, scaleFactor: 1, clickSelectors: []string{"#open", ".item", ".item"}}
	buf, err := takeScreenshot(ctx, srv.URL, p)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.dataset.result + ':' + document.body.dataset.clicks`, &result)); err != nil {
		t.Fatal(err)
	}
	if result != "selected:2" || len(buf) == 0 {
		t.Fatalf("result = %q, screenshot bytes = %d", result, len(buf))
	}

	p.clickSelectors = []string{"#open", "[invalid"}
	buf, err = takeScreenshot(ctx, srv.URL, p)
	if err == nil || !strings.Contains(err.Error(), `click 2 ("[invalid")`) || len(buf) != 0 {
		t.Fatalf("expected second-click error and no screenshot: error = %v, bytes = %d", err, len(buf))
	}
}

func TestE2E_ClickNavigation(t *testing.T) {
	skipUnlessE2E(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.URL.Path == "/redirect":
			http.Redirect(w, r, "/destination", http.StatusFound)
		case r.URL.Path == "/destination" || r.Method == http.MethodPost:
			time.Sleep(1500 * time.Millisecond)
			w.Write([]byte(`<body style="margin:0;background:rgb(20,180,70)"><h1 id="ready">Destination</h1></body>`))
		case r.URL.Path == "/reload":
			w.Write([]byte(`<body style="background:red"><form method="post"><button id="go">Reload</button></form></body>`))
		case r.URL.Path == "/spa":
			w.Write([]byte(`<body style="background:red"><button id="go" onclick="document.body.innerHTML='';document.body.style.background='white';setTimeout(() => {document.body.style.background='rgb(20,180,70)';document.body.innerHTML='<h1 id=ready>Destination</h1>';},1500)">Go</button></body>`))
		case r.URL.Path == "/history":
			w.Write([]byte(`<body style="background:red"><button id="go" onclick="history.pushState({}, '', '/updated?view=2#ready');document.body.style.background='rgb(20,180,70)'">Go</button></body>`))
		default:
			w.Write([]byte(`<body style="background:red"><a id="go" href="/redirect">Go</a></body>`))
		}
	}))
	defer srv.Close()
	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, waitFor, finalPath string
		clicks                         []string
	}{
		{"redirect", "/", "", "/destination", []string{"#go"}},
		{"reload", "/reload", "", "/reload", []string{"#go"}},
		{"spa", "/spa", "#ready", "/spa", []string{"#go"}},
		{"history", "/history", "", "/updated?view=2#ready", []string{"#go"}},
		{"initial_redirect", "/redirect", "#ready", "/destination", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(browserCtx, 15*time.Second)
			defer cancel()
			p := captureParams{windowWidth: 640, windowHeight: 360, scaleFactor: 1,
				clickSelectors: tc.clicks, waitSelector: tc.waitFor, showAddressBar: true}
			buf, err := takeScreenshot(ctx, srv.URL+tc.path, p)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(buf))
			if err != nil {
				t.Fatal(err)
			}
			r, g, b, _ := img.At(320, 232).RGBA()
			if img.Bounds().Dx() != 640 || img.Bounds().Dy() != 412 || r>>8 != 20 || g>>8 != 180 || b>>8 != 70 {
				t.Fatalf("wrong destination screenshot: %v, RGB %d,%d,%d", img.Bounds(), r>>8, g>>8, b>>8)
			}
			var displayedURL string
			if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.url').textContent`, &displayedURL)); err != nil {
				t.Fatal(err)
			}
			if displayedURL != srv.URL+tc.finalPath {
				t.Fatalf("address bar = %q, want %q", displayedURL, srv.URL+tc.finalPath)
			}
		})
	}
	ctx, cancel := context.WithTimeout(browserCtx, 2*time.Second)
	defer cancel()
	p := captureParams{windowWidth: 640, windowHeight: 360, scaleFactor: 1, waitSelector: "#missing"}
	buf, err := takeScreenshot(ctx, srv.URL+"/history", p)
	if err == nil || !strings.Contains(err.Error(), `wait-for ("#missing")`) || len(buf) != 0 {
		t.Fatalf("missing wait-for must fail without a screenshot: %v, bytes=%d", err, len(buf))
	}
}

func TestE2E_ScreenshotModes(t *testing.T) {
	skipUnlessE2E(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<style>body{margin:0}button{display:block;height:20px;border:0;padding:0}#target{width:120px;height:80px;background:rgb(20,180,70);margin-top:900px}#tall{width:50px;height:20000px;background:rgb(40,60,200)}</style>
<button onclick="window.scrollTo(0,900)">Scroll</button><div id="target"></div><div id="tall"></div>`))
	}))
	defer srv.Close()
	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query   string
		full          bool
		width, height int
	}{
		{"viewport", "", false, 640, 480},
		{"full", "", true, 640, 16384},
		{"scrolled element", "#target", false, 240, 160},
		{"oversized element", "#tall", false, 100, 14384},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := captureParams{windowWidth: 320, windowHeight: 240, scaleFactor: 2, querySelector: tc.query, fullScreenshot: tc.full}
			if tc.query != "" {
				p.clickSelectors = []string{"button"}
			}
			buf, err := captureTab(context.Background(), browserCtx, make(chan struct{}, 1), srv.URL, p)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(buf))
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != tc.width || b.Dy() != tc.height {
				t.Fatalf("dimensions = %v, want %dx%d", b, tc.width, tc.height)
			}
			if tc.query == "#target" {
				r, g, b, _ := img.At(10, 10).RGBA()
				if r>>8 != 20 || g>>8 != 180 || b>>8 != 70 {
					t.Fatalf("wrong element pixels: %d,%d,%d", r>>8, g>>8, b>>8)
				}
			}
		})
	}
}

func TestMCPInvalidInputs(t *testing.T) {
	for _, args := range []map[string]any{
		{"urls": []string{}}, {"urls": []string{" "}},
		{"width": 0}, {"width": 320.5}, {"height": math.Inf(1)},
		{"scale": 0}, {"scale": math.NaN()}, {"width": 9000, "scale": 2},
		{"wait": -1}, {"wait": 1e100}, {"timeout": 0},
		{"quality": 101}, {"format": "webp"}, {"output": " "},
		{"width": "320"}, {"full": "true"},
		{"wait_for": 1}, {"wait_for": " "},
	} {
		if _, ok := args["urls"]; !ok {
			args["urls"] = []string{"http://example.com"}
		}
		if _, ok := args["output"]; !ok {
			args["output"] = "out.png"
		}
		var req mcp.CallToolRequest
		req.Params.Arguments = args
		result, err := mcpScreenshotHandler(context.Background(), nil, true)(context.Background(), req)
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("input %v: result = %v, error = %v", args, result, err)
		}
	}
}

func TestMCPCancelQueued(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"urls": []string{"http://example.com"}}
	result, err := mcpScreenshotHandler(context.Background(), sem, false)(ctx, req)
	if err != nil || !result.IsError || len(sem) != 1 {
		t.Fatalf("queued cancellation: %v, %v", result, err)
	}
}

func TestE2E_MCPIsolationAndCancel(t *testing.T) {
	skipUnlessE2E(t)
	var arrivals sync.WaitGroup
	arrivals.Add(2)
	visited := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cancel" {
			visited <- struct{}{}
		} else if r.URL.Path == "/red" || r.URL.Path == "/blue" {
			arrivals.Done()
			arrivals.Wait()
		}
		w.Header().Set("Content-Type", "text/html")
		color := "rgb(200,30,10)"
		if r.URL.Path == "/blue" {
			color = "rgb(10,40,210)"
		}
		w.Write([]byte(`<style>body{margin:0;background:` + color + `}</style>`))
	}))
	defer srv.Close()
	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	sem := make(chan struct{}, 2)
	handler := mcpScreenshotHandler(browserCtx, sem, false)
	var wg sync.WaitGroup
	for _, tc := range []struct {
		path      string
		width     int
		red, blue uint32
	}{{"/red", 340, 200, 10}, {"/blue", 460, 10, 210}} {
		wg.Go(func() {
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"urls": []string{srv.URL + tc.path}, "width": tc.width, "height": 240, "wait": 0}
			result, err := handler(context.Background(), req)
			if err != nil || result.IsError {
				t.Errorf("%s: %v, %v", tc.path, result, err)
				return
			}
			data, err := base64.StdEncoding.DecodeString(result.Content[1].(mcp.ImageContent).Data)
			if err != nil {
				t.Error(err)
				return
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Error(err)
				return
			}
			r, _, b, _ := img.At(10, 10).RGBA()
			if img.Bounds().Dx() != tc.width || r>>8 != tc.red || b>>8 != tc.blue {
				t.Errorf("%s: wrong dimensions or page pixels: %v, %d,%d", tc.path, img.Bounds(), r>>8, b>>8)
			}
		})
	}
	wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"urls": []string{srv.URL + "/cancel"}, "clicks": []string{"#missing"}, "wait": 0}
	done := make(chan *mcp.CallToolResult, 1)
	go func() { result, _ := handler(ctx, req); done <- result }()
	<-visited
	cancel()
	select {
	case result := <-done:
		if !result.IsError || len(sem) != 0 {
			t.Fatal("cancelled request must fail and release its capture slot")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not stop capture")
	}

	req.Params.Arguments = map[string]any{"urls": []string{srv.URL, "http://127.0.0.1:0"}, "wait": 0}
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError || len(result.Content) != 3 {
		t.Fatalf("partial failure must retain image and report error: %v, %v", result, err)
	}
	req.Params.Arguments = map[string]any{"urls": []string{srv.URL}, "clicks": []string{"#missing"}, "wait": 0, "timeout": 1}
	result, err = handler(context.Background(), req)
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcp.TextContent).Text, "deadline exceeded") {
		t.Fatalf("missing target must hit the capture deadline: %v, %v", result, err)
	}
}

func TestE2E_SelectorEscaping(t *testing.T) {
	skipUnlessE2E(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<select id="foo:bar" data-key="a` + "`" + `${x}'" onmouseenter="document.body.dataset.hovered='yes'"><option>One</option><option selected>Two</option></select>`))
	}))
	defer srv.Close()
	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	ctx, cancel := context.WithTimeout(browserCtx, 20*time.Second)
	defer cancel()
	p := captureParams{windowWidth: 320, windowHeight: 240, scaleFactor: 1,
		hoverSelector: `#foo\:bar`, expandSelect: `[data-key="a` + "`" + `${x}'"]`}
	if _, err := takeScreenshot(ctx, srv.URL, p); err != nil {
		t.Fatal(err)
	}
	var result []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`[document.body.dataset.hovered, document.querySelector('.__sesnap-select-overlay').children[1].textContent]`, &result)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, []string{"yes", "Two"}) {
		t.Fatalf("hover and select expansion = %v", result)
	}
	p.querySelector = `#foo\:bar`
	if _, err := takeScreenshot(ctx, srv.URL, p); err != nil {
		t.Fatal(err)
	}
}

func TestE2E_CLIFailureCleanup(t *testing.T) {
	skipUnlessE2E(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>Captured</body></html>"))
	}))
	defer srv.Close()
	oldArgs, oldURLs, oldFlags, oldClicks, oldScale := arguments, urls, chromeFlags, clickSelectors, deviceScaleFactor
	t.Cleanup(func() {
		arguments, urls, chromeFlags, clickSelectors, deviceScaleFactor = oldArgs, oldURLs, oldFlags, oldClicks, oldScale
	})
	output := filepath.Join(t.TempDir(), "result.png")
	profile := t.TempDir()
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	setOutputPath(output)
	setProfileDir(profile)
	setReUseProfile(false)
	setURLs("http://127.0.0.1:0", srv.URL)
	wait, parallel := 0, 2
	arguments.waitSeconds, arguments.parallel = &wait, &parallel
	clickSelectors = nil
	deviceScaleFactor = 1
	if err := run(context.Background()); err == nil {
		t.Fatal("one failed URL must make CLI fail")
	} else if !strings.Contains(err.Error(), urls[0]) {
		t.Fatalf("expected URL capture failure, got: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(output, ".png") + "_002.png"); err != nil {
		t.Fatalf("successful URL was not saved: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(output, ".png") + "_001.png"); !os.IsNotExist(err) {
		t.Fatal("navigation error must not produce a screenshot")
	}
	// Chrome may leave temporary directories unrelated to our profile copies.
	entries, err := filepath.Glob(filepath.Join(tempRoot, "sesnap-userdata-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary profiles remain after failure: %v, %v", entries, err)
	}
}
