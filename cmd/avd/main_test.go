package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestExtractTasksFromDocumentDedupesVideoLinks(t *testing.T) {
	html := `
<html>
  <body>
    <a href="/videos/abc-123/">cover</a>
    <a href="https://jable.tv/videos/abc-123/">title</a>
    <a href="/videos/xyz-789/">other</a>
    <a href="/categories/chinese-subtitle/">ignore</a>
  </body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	tasks := extractTasksFromDocument(doc, "https://jable.tv/categories/chinese-subtitle/", "https://jable.tv")
	if got, want := len(tasks), 2; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if tasks[0].VideoID != "abc-123" {
		t.Fatalf("first video id = %q, want %q", tasks[0].VideoID, "abc-123")
	}
	if tasks[1].VideoID != "xyz-789" {
		t.Fatalf("second video id = %q, want %q", tasks[1].VideoID, "xyz-789")
	}
}

func TestMergeTasksIntoFileAppendsOnlyNewIDs(t *testing.T) {
	dir := t.TempDir()
	taskFile := filepath.Join(dir, "auto-tasks.txt")
	if err := os.WriteFile(taskFile, []byte("# auto discovered video ids\nabc-123\n"), 0o644); err != nil {
		t.Fatalf("seed task file: %v", err)
	}

	added, err := mergeTasksIntoFile(taskFile, []Task{
		{VideoID: "abc-123"},
		{VideoID: "xyz-789"},
	})
	if err != nil {
		t.Fatalf("merge tasks: %v", err)
	}
	if got, want := added, 1; got != want {
		t.Fatalf("added = %d, want %d", got, want)
	}

	data, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("read merged task file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "abc-123\n") {
		t.Fatalf("merged file missing existing task: %q", content)
	}
	if !strings.Contains(content, "xyz-789\n") {
		t.Fatalf("merged file missing new task: %q", content)
	}
}

func TestLoadConfigUsesAVDProxyOverride(t *testing.T) {
	t.Setenv("AVD_PROXY", "http://127.0.0.1:7890")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"proxy":"http://example.com:8080"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got, want := cfg.Proxy, "http://127.0.0.1:7890"; got != want {
		t.Fatalf("proxy = %q, want %q", got, want)
	}
}

func TestProxyEnvKeepsExistingProxyVariablesWhenNoExplicitProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7890")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")

	env := proxyEnv("")
	content := "\n" + strings.Join(env, "\n") + "\n"
	if !strings.Contains(content, "\nHTTP_PROXY=http://127.0.0.1:7890\n") {
		t.Fatalf("HTTP_PROXY missing from env: %q", content)
	}
	if !strings.Contains(content, "\nHTTPS_PROXY=http://127.0.0.1:7890\n") {
		t.Fatalf("HTTPS_PROXY missing from env: %q", content)
	}
}

func TestWriteNFOUsesPosterAndFanartTags(t *testing.T) {
	dir := t.TempDir()
	app := &App{}

	err := app.writeNFO(VideoMetadata{
		VideoID:         "abc-123",
		Title:           "Example Title",
		ReleaseDate:     "2026-04-03",
		Genres:          []string{"中文字幕"},
		CoverLocal:      posterFileName,
		BackgroundLocal: fanartFileName,
	}, dir)
	if err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "abc-123.nfo"))
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "<poster>poster.jpg</poster>") {
		t.Fatalf("nfo missing poster tag: %q", content)
	}
	if !strings.Contains(content, "<fanart>fanart.jpg</fanart>") {
		t.Fatalf("nfo missing fanart tag: %q", content)
	}
	if strings.Contains(content, "<thumb") {
		t.Fatalf("nfo should not contain thumb tags: %q", content)
	}
}
