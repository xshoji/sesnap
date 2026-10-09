package main

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if err := flag.CommandLine.Parse([]string{"--click=.menu, .fallback", "-k=.item", "--click=.menu, .fallback"}); err != nil {
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
	if got := chromeProfileCacheRoot(); got != "/custom/cache" {
		t.Errorf("chromeProfileCacheRoot() = %q, want /custom/cache", got)
	}
}

func TestChromeProfileCacheRoot_Default(t *testing.T) {
	t.Setenv("SESNAP_CACHE_DIR", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".sesnap")
	if got := chromeProfileCacheRoot(); got != want {
		t.Errorf("chromeProfileCacheRoot() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// removeStaleChromeLocks
// ---------------------------------------------------------------------------

func TestRemoveStaleChromeLocks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		os.WriteFile(filepath.Join(dir, name), []byte{}, 0644)
	}
	removeStaleChromeLocks(dir)
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("lock file %s should have been removed", name)
		}
	}
}

// ---------------------------------------------------------------------------
// setupProfileCache
// ---------------------------------------------------------------------------

func TestSetupProfileCache_EmptyProfileDir(t *testing.T) {
	setProfileDir("")
	if got := setupProfileCache(); got != "" {
		t.Errorf("setupProfileCache() = %q, want empty string", got)
	}
}

func TestSetupProfileCache_CopiesProfileToTempDir(t *testing.T) {
	// Create a fake profile source directory with a marker file
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "Cookies"), []byte("data"), 0644)

	setProfileDir(srcDir)
	setReUseProfile(false)

	got := setupProfileCache()
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

	// Pre-create cached profile
	profileName := filepath.Base(srcDir)
	cachedDir := filepath.Join(cacheRoot, "userdata-"+profileName, profileName)
	os.MkdirAll(cachedDir, 0700)
	os.WriteFile(filepath.Join(cachedDir, "Marker"), []byte("cached"), 0644)

	got := setupProfileCache()
	wantDir := filepath.Join(cacheRoot, "userdata-"+profileName)
	if got != wantDir {
		t.Fatalf("setupProfileCache() = %q, want %q", got, wantDir)
	}
	// Marker should still exist (not re-copied)
	if _, err := os.Stat(filepath.Join(cachedDir, "Marker")); err != nil {
		t.Error("Marker should still exist in reuse mode")
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
	cleanupProfileCache(dir)

	if _, err := os.Stat(marker); err != nil {
		t.Error("cache dir should have been kept in reuse mode")
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
<button id="item" hidden onclick="document.body.dataset.result = 'selected'">Select</button>`))
	}))
	defer srv.Close()

	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	ctx, cancel := context.WithTimeout(browserCtx, 20*time.Second)
	defer cancel()
	p := captureParams{windowWidth: 800, windowHeight: 600, scaleFactor: 1, clickSelectors: []string{"#open", "#item"}}
	buf, err := takeScreenshot(ctx, srv.URL, p)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.dataset.result`, &result)); err != nil {
		t.Fatal(err)
	}
	if result != "selected" || len(buf) == 0 {
		t.Fatalf("result = %q, screenshot bytes = %d", result, len(buf))
	}

	p.clickSelectors = []string{"#open", "[invalid"}
	buf, err = takeScreenshot(ctx, srv.URL, p)
	if err == nil || !strings.Contains(err.Error(), `click 2 ("[invalid")`) || len(buf) != 0 {
		t.Fatalf("expected second-click error and no screenshot: error = %v, bytes = %d", err, len(buf))
	}
}

func TestE2E_ViewportScreenshot(t *testing.T) {
	skipUnlessE2E(t)

	outFile := filepath.Join(t.TempDir(), "viewport.png")
	setOutputPath(outFile)
	setURLs("https://www.example.com")
	setProfileDir("")

	w := int64(1280)
	h := int64(860)
	ws := 1
	f := false
	d := false
	n := false
	arguments.windowWidth = &w
	arguments.windowHeight = &h
	deviceScaleFactor = 2.0
	arguments.waitSeconds = &ws
	arguments.fullScreenshot = &f
	arguments.debug = &d
	arguments.noHeadless = &n
	arguments.querySelector = strPtr("")
	chromeFlags = nil

	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatalf("failed to start browser: %v", err)
	}

	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	buf, err := takeScreenshot(tabCtx, "https://www.example.com", captureParamsFromArgs())
	if err != nil {
		t.Fatalf("takeScreenshot failed: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("screenshot buffer is empty")
	}
	if err := os.WriteFile(outFile, buf, 0644); err != nil {
		t.Fatalf("failed to write screenshot: %v", err)
	}
	info, _ := os.Stat(outFile)
	t.Logf("screenshot saved: %s (%d bytes)", outFile, info.Size())
}

func TestE2E_FullPageScreenshot(t *testing.T) {
	skipUnlessE2E(t)

	outFile := filepath.Join(t.TempDir(), "fullpage.png")
	setOutputPath(outFile)
	setURLs("https://www.example.com")
	setProfileDir("")

	w := int64(1280)
	h := int64(860)
	ws := 1
	f := true
	d := false
	n := false
	arguments.windowWidth = &w
	arguments.windowHeight = &h
	deviceScaleFactor = 2.0
	arguments.waitSeconds = &ws
	arguments.fullScreenshot = &f
	arguments.debug = &d
	arguments.noHeadless = &n
	arguments.querySelector = strPtr("")
	chromeFlags = nil

	browserCtx, shutdown := newBrowserContext("")
	defer shutdown()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatalf("failed to start browser: %v", err)
	}

	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	buf, err := takeScreenshot(tabCtx, "https://www.example.com", captureParamsFromArgs())
	if err != nil {
		t.Fatalf("takeScreenshot failed: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("screenshot buffer is empty")
	}
	t.Logf("full-page screenshot: %d bytes", len(buf))
}

func strPtr(s string) *string { return &s }
