package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLoadConfigKeepsConfigProxyWhenAVDProxyIsEmpty(t *testing.T) {
	t.Setenv("AVD_PROXY", "")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"proxy":"http://example.com:8080"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got, want := cfg.Proxy, "http://example.com:8080"; got != want {
		t.Fatalf("proxy = %q, want %q", got, want)
	}
}

func TestLoadConfigUsesMaxRetainedVideosOverride(t *testing.T) {
	t.Setenv("maxRetainedVideos", "100")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"maxRetainedVideos":5}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got, want := cfg.MaxRetainedVideos, 100; got != want {
		t.Fatalf("maxRetainedVideos = %d, want %d", got, want)
	}
}

func TestLoadConfigRejectsInvalidMaxRetainedVideosOverride(t *testing.T) {
	t.Setenv("MAX_RETAINED_VIDEOS", "abc")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"maxRetainedVideos":5}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := loadConfig(configPath)
	if err == nil {
		t.Fatalf("load config should reject invalid max retained videos override")
	}
	if !strings.Contains(err.Error(), "MAX_RETAINED_VIDEOS") {
		t.Fatalf("error should mention env name: %v", err)
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

func TestTempVideoPathUsesSeparateTempDir(t *testing.T) {
	got := tempVideoPath(filepath.Join("data", "videos"), "abc-123")
	want := filepath.Join("data", "tmp", "abc-123", "abc-123.mp4")
	if got != want {
		t.Fatalf("tempVideoPath() = %q, want %q", got, want)
	}
	if !strings.Contains(got, filepath.Join("data", "tmp", "abc-123")) {
		t.Fatalf("temp path should be stored outside videos directory: %q", got)
	}
}

func TestTryClaimTaskSkipsPrunedTasks(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeState(statePath, State{
		Tasks: map[string]*TaskState{
			"abc-123": {Status: "pruned"},
		},
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	app := &App{
		config:      Config{StateFile: statePath},
		logger:      log.New(io.Discard, "", 0),
		activeTasks: map[string]bool{},
	}

	claimed, err := app.tryClaimTask(Task{VideoID: "abc-123"})
	if err != nil {
		t.Fatalf("tryClaimTask: %v", err)
	}
	if claimed {
		t.Fatalf("pruned task should not be claimed")
	}
}

func TestPruneRetainedVideosRemovesOldestAndUpdatesTaskSources(t *testing.T) {
	dir := t.TempDir()
	videosRoot := filepath.Join(dir, "videos")
	autoTaskFile := filepath.Join(dir, "auto-tasks.txt")
	stateFile := filepath.Join(dir, "state.json")

	if err := os.MkdirAll(videosRoot, 0o755); err != nil {
		t.Fatalf("mkdir videos root: %v", err)
	}
	if err := os.WriteFile(autoTaskFile, []byte("# auto discovered video ids\nold-001\nnew-001\n"), 0o644); err != nil {
		t.Fatalf("write auto task file: %v", err)
	}

	oldDir := filepath.Join(videosRoot, "old-001")
	newDir := filepath.Join(videosRoot, "new-001")
	for _, dirPath := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatalf("mkdir video dir: %v", err)
		}
	}

	oldVideoPath := filepath.Join(oldDir, "old-001.mp4")
	newVideoPath := filepath.Join(newDir, "new-001.mp4")
	for _, path := range []string{
		oldVideoPath,
		filepath.Join(oldDir, "old-001.nfo"),
		newVideoPath,
		filepath.Join(newDir, "new-001.nfo"),
	} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write file %s: %v", path, err)
		}
	}

	now := time.Now()
	if err := os.Chtimes(oldVideoPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("chtimes old video: %v", err)
	}
	if err := os.Chtimes(newVideoPath, now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("chtimes new video: %v", err)
	}

	if err := writeState(stateFile, State{
		Tasks: map[string]*TaskState{
			"old-001": {Status: "completed", OutputPath: oldVideoPath},
			"new-001": {Status: "completed", OutputPath: newVideoPath},
		},
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	app := &App{
		config: Config{
			AutoTaskFile:      autoTaskFile,
			StateFile:         stateFile,
			VideosRoot:        videosRoot,
			MaxRetainedVideos: 1,
		},
		logger:      log.New(io.Discard, "", 0),
		activeTasks: map[string]bool{},
	}

	if err := app.pruneRetainedVideos(); err != nil {
		t.Fatalf("pruneRetainedVideos: %v", err)
	}

	if fileExists(oldDir) {
		t.Fatalf("oldest video dir should be removed")
	}
	if !fileExists(newVideoPath) {
		t.Fatalf("newer video should be kept")
	}

	taskData, err := os.ReadFile(autoTaskFile)
	if err != nil {
		t.Fatalf("read auto task file: %v", err)
	}
	taskContent := string(taskData)
	if strings.Contains(taskContent, "old-001\n") {
		t.Fatalf("old pruned task should be removed from task file: %q", taskContent)
	}
	if !strings.Contains(taskContent, "new-001\n") {
		t.Fatalf("new retained task should stay in task file: %q", taskContent)
	}

	state, err := readState(stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got := state.Tasks["old-001"].Status; got != "pruned" {
		t.Fatalf("old task status = %q, want %q", got, "pruned")
	}
	if got := state.Tasks["old-001"].OutputPath; got != "" {
		t.Fatalf("old task output path = %q, want empty", got)
	}
	if got := state.Tasks["new-001"].Status; got != "completed" {
		t.Fatalf("new task status = %q, want %q", got, "completed")
	}
}
