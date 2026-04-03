package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	videoURLRe    = regexp.MustCompile(`/videos/([^/]+)/?`)
	hlsURLRe      = regexp.MustCompile(`var\s+hlsUrl\s*=\s*'([^']+)'`)
	extinfRe      = regexp.MustCompile(`(?m)^#EXTINF:([0-9]+(?:\.[0-9]+)?),`)
	releaseDateRe = regexp.MustCompile(`上市於\s*([0-9]{4}-[0-9]{2}-[0-9]{2})`)
	version       = "dev"
	commit        = "unknown"
	buildTime     = "unknown"
)

type Config struct {
	BaseURL                     string `json:"baseUrl"`
	PollIntervalSeconds         int    `json:"pollIntervalSeconds"`
	DownloadConcurrency         int    `json:"downloadConcurrency"`
	HTTPTimeoutSeconds          int    `json:"httpTimeoutSeconds"`
	MaxRetries                  int    `json:"maxRetries"`
	AutoTaskFile                string `json:"autoTaskFile"`
	StateFile                   string `json:"stateFile"`
	VideosRoot                  string `json:"videosRoot"`
	UserAgent                   string `json:"userAgent"`
	AcceptLanguage              string `json:"acceptLanguage"`
	FFmpegPath                  string `json:"ffmpegPath"`
	Proxy                       string `json:"proxy"`
	OverwriteVideo              bool   `json:"overwriteVideo"`
	SaveActorImages             bool   `json:"saveActorImages"`
	CategoryPageURL             string `json:"categoryPageURL"`
	CategoryScanIntervalSeconds int    `json:"categoryScanIntervalSeconds"`
}

type Task struct {
	RawValue string
	VideoID  string
	PageURL  string
}

type App struct {
	config      Config
	client      *http.Client
	logger      *log.Logger
	mu          sync.Mutex
	activeTasks map[string]bool
}

type State struct {
	Tasks map[string]*TaskState `json:"tasks"`
}

type TaskState struct {
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	UpdatedAt  string `json:"updatedAt"`
	LastError  string `json:"lastError,omitempty"`
	OutputPath string `json:"outputPath,omitempty"`
	Title      string `json:"title,omitempty"`
}

type VideoMetadata struct {
	VideoID         string
	PageURL         string
	Title           string
	HLSURL          string
	Duration        time.Duration
	BackgroundURL   string
	CoverLocal      string
	BackgroundLocal string
	ReleaseDate     string
	Genres          []string
	Models          []ModelInfo
}

type ModelInfo struct {
	ID         string
	Name       string
	PageURL    string
	ThumbURL   string
	LocalThumb string
}

type MovieNFO struct {
	XMLName       xml.Name   `xml:"movie"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle"`
	Plot          string     `xml:"plot,omitempty"`
	Outline       string     `xml:"outline,omitempty"`
	Studio        string     `xml:"studio"`
	ID            string     `xml:"id"`
	UniqueID      UniqueID   `xml:"uniqueid"`
	Premiered     string     `xml:"premiered,omitempty"`
	Genres        []string   `xml:"genre,omitempty"`
	Thumbs        []ThumbNFO `xml:"thumb,omitempty"`
	Actors        []ActorNFO `xml:"actor,omitempty"`
}

type UniqueID struct {
	Type    string `xml:"type,attr,omitempty"`
	Default string `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type ThumbNFO struct {
	Aspect string `xml:"aspect,attr,omitempty"`
	Value  string `xml:",chardata"`
}

type ActorNFO struct {
	Name  string `xml:"name"`
	Thumb string `xml:"thumb,omitempty"`
}

type ffmpegProgress struct {
	Speed     string
	TotalSize int64
	OutTime   time.Duration
}

func main() {
	configPath := flag.String("config", "config/config.json", "path to config json")
	once := flag.Bool("once", false, "process current tasks once and exit")
	taskValue := flag.String("task", "", "process a single video id or full page URL and exit")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	logger.Printf("starting %s", versionString())
	if err := ensureRuntimePaths(cfg); err != nil {
		logger.Fatalf("prepare runtime paths: %v", err)
	}

	app, err := newApp(cfg, logger)
	if err != nil {
		logger.Fatalf("create app: %v", err)
	}

	if *taskValue != "" {
		task, err := normalizeTask(*taskValue, cfg.BaseURL)
		if err != nil {
			logger.Fatalf("invalid task: %v", err)
		}
		if err := app.processTasks(ctx, []Task{task}); err != nil {
			logger.Fatalf("process task: %v", err)
		}
		return
	}

	if *once {
		if strings.TrimSpace(cfg.CategoryPageURL) != "" && cfg.CategoryScanIntervalSeconds > 0 {
			if err := app.scanCategoryPage(ctx); err != nil {
				logger.Fatalf("scan category page: %v", err)
			}
		}
		tasks, err := loadTasks(cfg.AutoTaskFile, cfg.BaseURL, logger)
		if err != nil {
			logger.Fatalf("load tasks: %v", err)
		}
		if err := app.processTasks(ctx, tasks); err != nil {
			logger.Fatalf("process tasks: %v", err)
		}
		return
	}

	logger.Printf("worker started, auto task file: %s, download concurrency: %d", cfg.AutoTaskFile, cfg.DownloadConcurrency)
	if strings.TrimSpace(cfg.CategoryPageURL) != "" && cfg.CategoryScanIntervalSeconds > 0 {
		go app.runCategoryScanner(ctx)
	}
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		if err := app.processTick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("tick failed: %v", err)
		}

		select {
		case <-ctx.Done():
			logger.Printf("worker stopped")
			return
		case <-ticker.C:
		}
	}
}

func versionString() string {
	return fmt.Sprintf("avd version=%s commit=%s buildTime=%s", version, commit, buildTime)
}

func newApp(cfg Config, logger *log.Logger) (*App, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if strings.TrimSpace(cfg.Proxy) != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	client := &http.Client{
		Timeout:   time.Duration(cfg.HTTPTimeoutSeconds) * time.Second,
		Transport: transport,
	}

	return &App{
		config:      cfg,
		client:      client,
		logger:      logger,
		activeTasks: map[string]bool{},
	}, nil
}

func loadConfig(configPath string) (Config, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	baseDir := filepath.Dir(absPath)
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://jable.tv"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.PollIntervalSeconds <= 0 {
		cfg.PollIntervalSeconds = 30
	}
	if cfg.DownloadConcurrency <= 0 {
		cfg.DownloadConcurrency = 5
	}
	if cfg.HTTPTimeoutSeconds <= 0 {
		cfg.HTTPTimeoutSeconds = 30
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.AutoTaskFile == "" {
		cfg.AutoTaskFile = filepath.Join(baseDir, "data", "auto-tasks.txt")
	}
	if cfg.StateFile == "" {
		cfg.StateFile = filepath.Join(baseDir, "data", "state.json")
	}
	if cfg.VideosRoot == "" {
		cfg.VideosRoot = filepath.Join(baseDir, "data", "videos")
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	}
	if cfg.AcceptLanguage == "" {
		cfg.AcceptLanguage = "zh-CN,zh;q=0.9,en;q=0.8"
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.CategoryPageURL == "" {
		cfg.CategoryPageURL = cfg.BaseURL + "/categories/chinese-subtitle/"
	}
	if cfg.CategoryScanIntervalSeconds == 0 {
		cfg.CategoryScanIntervalSeconds = 600
	}
	cfg.Proxy = resolveProxyOverride(cfg.Proxy)

	cfg.AutoTaskFile = resolveMaybeRelative(baseDir, cfg.AutoTaskFile)
	cfg.StateFile = resolveMaybeRelative(baseDir, cfg.StateFile)
	cfg.VideosRoot = resolveMaybeRelative(baseDir, cfg.VideosRoot)

	return cfg, nil
}

func resolveMaybeRelative(baseDir, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(baseDir, value))
}

func resolveProxyOverride(configProxy string) string {
	if value, ok := os.LookupEnv("AVD_PROXY"); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("avd_proxy"); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(configProxy)
}

func ensureRuntimePaths(cfg Config) error {
	for _, dir := range []string{
		filepath.Dir(cfg.AutoTaskFile),
		filepath.Dir(cfg.StateFile),
		cfg.VideosRoot,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if _, err := os.Stat(cfg.AutoTaskFile); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(cfg.AutoTaskFile, []byte("# auto discovered video ids\n"), 0o644); err != nil {
			return err
		}
	}

	if _, err := os.Stat(cfg.StateFile); errors.Is(err, os.ErrNotExist) {
		state := State{Tasks: map[string]*TaskState{}}
		return writeState(cfg.StateFile, state)
	}

	return nil
}

func loadTasks(taskFile, baseURL string, logger *log.Logger) ([]Task, error) {
	data, err := os.ReadFile(taskFile)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	tasks := make([]Task, 0, len(lines))
	seen := map[string]bool{}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		task, err := normalizeTask(line, baseURL)
		if err != nil {
			logger.Printf("skip invalid task %q: %v", line, err)
			continue
		}
		if seen[strings.ToLower(task.VideoID)] {
			continue
		}
		seen[strings.ToLower(task.VideoID)] = true
		tasks = append(tasks, task)
	}

	return tasks, nil
}

func normalizeTask(value, baseURL string) (Task, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Task{}, fmt.Errorf("empty task")
	}

	videoID := value
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		match := videoURLRe.FindStringSubmatch(value)
		if len(match) < 2 {
			return Task{}, fmt.Errorf("cannot extract video id from %s", value)
		}
		videoID = match[1]
	}

	videoID = strings.Trim(videoID, "/")
	if videoID == "" {
		return Task{}, fmt.Errorf("empty video id")
	}

	for _, ch := range videoID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return Task{}, fmt.Errorf("unsupported video id format: %s", videoID)
		}
	}

	return Task{
		RawValue: value,
		VideoID:  videoID,
		PageURL:  fmt.Sprintf("%s/videos/%s/", baseURL, videoID),
	}, nil
}

func (a *App) processTick(ctx context.Context) error {
	tasks, err := a.loadAutoTasks()
	if err != nil {
		return err
	}
	return a.dispatchTasks(ctx, tasks)
}

func (a *App) runCategoryScanner(ctx context.Context) {
	interval := time.Duration(a.config.CategoryScanIntervalSeconds) * time.Second
	a.logger.Printf("category scanner started: %s every %s", a.config.CategoryPageURL, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := a.scanCategoryPage(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Printf("category scan failed: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) scanCategoryPage(ctx context.Context) error {
	tasks, err := a.fetchCategoryTasks(ctx)
	if err != nil {
		return err
	}

	state, existingTasks, err := a.loadScanSnapshot()
	if err != nil {
		return err
	}

	existing := map[string]bool{}
	for _, task := range existingTasks {
		existing[strings.ToLower(task.VideoID)] = true
	}

	newTasks := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		taskKey := strings.ToLower(task.VideoID)
		if existing[taskKey] {
			continue
		}
		if a.outputReady(task.VideoID) {
			continue
		}
		if taskState := state.Tasks[taskKey]; taskState != nil {
			switch taskState.Status {
			case "completed":
				if a.outputReady(task.VideoID) {
					continue
				}
			case "running":
				continue
			}
		}
		existing[taskKey] = true
		newTasks = append(newTasks, task)
	}

	added, err := a.mergeTasksIntoAutoTaskFile(newTasks)
	if err != nil {
		return err
	}

	a.logger.Printf("category scan finished: found %d videos, queued %d new tasks", len(tasks), added)
	return nil
}

func (a *App) fetchCategoryTasks(ctx context.Context) ([]Task, error) {
	pageHTML, err := a.fetchText(ctx, a.config.CategoryPageURL, a.config.BaseURL)
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return nil, err
	}

	return extractTasksFromDocument(doc, a.config.CategoryPageURL, a.config.BaseURL), nil
}

func extractTasksFromDocument(doc *goquery.Document, pageURL, baseURL string) []Task {
	tasks := make([]Task, 0)
	seen := map[string]bool{}

	doc.Find(`a[href]`).Each(func(_ int, sel *goquery.Selection) {
		href := strings.TrimSpace(sel.AttrOr("href", ""))
		if href == "" || !strings.Contains(strings.ToLower(href), "/videos/") {
			return
		}

		task, err := normalizeTask(absolutizeURL(pageURL, href), baseURL)
		if err != nil {
			return
		}

		taskKey := strings.ToLower(task.VideoID)
		if seen[taskKey] {
			return
		}
		seen[taskKey] = true
		tasks = append(tasks, task)
	})

	return tasks
}

func mergeTasksIntoFile(taskFile string, tasks []Task) (int, error) {
	if len(tasks) == 0 {
		return 0, nil
	}

	data, err := os.ReadFile(taskFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	lines := make([]string, 0)
	existing := map[string]bool{}
	if len(data) > 0 {
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines = strings.Split(content, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}

		for _, rawLine := range lines {
			line := strings.TrimSpace(rawLine)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			existing[strings.ToLower(line)] = true
		}
	}

	added := 0
	for _, task := range tasks {
		taskKey := strings.ToLower(task.VideoID)
		if existing[taskKey] {
			continue
		}
		existing[taskKey] = true
		lines = append(lines, task.VideoID)
		added++
	}

	if added == 0 {
		return 0, nil
	}

	payload := strings.Join(lines, "\n")
	if payload != "" && !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}

	if err := os.MkdirAll(filepath.Dir(taskFile), 0o755); err != nil {
		return 0, err
	}

	tempPath := taskFile + ".tmp"
	if err := os.WriteFile(tempPath, []byte(payload), 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tempPath, taskFile); err != nil {
		return 0, err
	}

	return added, nil
}

func (a *App) loadAutoTasks() ([]Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return loadTasks(a.config.AutoTaskFile, a.config.BaseURL, a.logger)
}

func (a *App) loadScanSnapshot() (State, []Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	state, err := readState(a.config.StateFile)
	if err != nil {
		return State{}, nil, err
	}
	tasks, err := loadTasks(a.config.AutoTaskFile, a.config.BaseURL, a.logger)
	if err != nil {
		return State{}, nil, err
	}
	return state, tasks, nil
}

func (a *App) mergeTasksIntoAutoTaskFile(tasks []Task) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return mergeTasksIntoFile(a.config.AutoTaskFile, tasks)
}

func (a *App) availableDownloadSlots() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	slots := a.config.DownloadConcurrency - len(a.activeTasks)
	if slots < 0 {
		return 0
	}
	return slots
}

func (a *App) tryClaimTask(task Task) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	taskKey := strings.ToLower(task.VideoID)
	if a.activeTasks[taskKey] {
		return false, nil
	}

	state, err := readState(a.config.StateFile)
	if err != nil {
		return false, err
	}

	taskState := state.Tasks[taskKey]
	if taskState != nil && taskState.Status == "completed" && a.outputReady(task.VideoID) {
		return false, nil
	}
	if taskState != nil && taskState.Status == "failed" && taskState.Attempts >= a.config.MaxRetries {
		return false, nil
	}

	a.activeTasks[taskKey] = true
	return true, nil
}

func (a *App) releaseTask(videoID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activeTasks, strings.ToLower(videoID))
}

func (a *App) updateTaskState(videoID string, update func(*TaskState)) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	state, err := readState(a.config.StateFile)
	if err != nil {
		return err
	}
	taskKey := strings.ToLower(videoID)
	taskState := state.Tasks[taskKey]
	if taskState == nil {
		taskState = &TaskState{}
		state.Tasks[taskKey] = taskState
	}
	update(taskState)
	return writeState(a.config.StateFile, state)
}

func (a *App) failTask(videoID string, taskErr error) error {
	if err := a.updateTaskState(videoID, func(taskState *TaskState) {
		taskState.Status = "failed"
		taskState.LastError = taskErr.Error()
		taskState.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}); err != nil {
		return err
	}
	return taskErr
}

func (a *App) processTasks(ctx context.Context, tasks []Task) error {
	if len(tasks) == 0 {
		return nil
	}

	sem := make(chan struct{}, a.config.DownloadConcurrency)
	var wg sync.WaitGroup

	for _, task := range tasks {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}

		claimed, err := a.tryClaimTask(task)
		if err != nil {
			<-sem
			wg.Wait()
			return err
		}
		if !claimed {
			<-sem
			continue
		}

		wg.Add(1)
		go func(task Task) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := a.processTask(ctx, task); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Printf("[%s] failed: %v", task.VideoID, err)
			}
		}(task)
	}

	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (a *App) dispatchTasks(ctx context.Context, tasks []Task) error {
	if len(tasks) == 0 {
		return nil
	}

	slots := a.availableDownloadSlots()
	if slots <= 0 {
		return nil
	}

	started := 0
	for _, task := range tasks {
		if started >= slots {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		claimed, err := a.tryClaimTask(task)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}

		started++
		go func(task Task) {
			if err := a.processTask(ctx, task); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Printf("[%s] failed: %v", task.VideoID, err)
			}
		}(task)
	}

	return nil
}

func (a *App) processTask(ctx context.Context, task Task) error {
	defer a.releaseTask(task.VideoID)

	if err := a.updateTaskState(task.VideoID, func(taskState *TaskState) {
		taskState.Status = "running"
		taskState.Attempts++
		taskState.LastError = ""
		taskState.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}); err != nil {
		return err
	}

	metadata, err := a.fetchVideoMetadata(ctx, task)
	if err != nil {
		return a.failTask(task.VideoID, err)
	}

	videoDir := filepath.Join(a.config.VideosRoot, task.VideoID)
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		return a.failTask(task.VideoID, err)
	}

	if err := a.downloadAssets(ctx, &metadata, videoDir); err != nil {
		return a.failTask(task.VideoID, err)
	}

	videoPath := filepath.Join(videoDir, task.VideoID+".mp4")
	if a.config.OverwriteVideo || !fileExists(videoPath) {
		if err := a.downloadVideo(ctx, metadata, videoPath); err != nil {
			return a.failTask(task.VideoID, err)
		}
	}

	if err := a.writeNFO(metadata, videoDir); err != nil {
		return a.failTask(task.VideoID, err)
	}

	if err := a.updateTaskState(task.VideoID, func(taskState *TaskState) {
		taskState.Status = "completed"
		taskState.Title = metadata.Title
		taskState.OutputPath = videoPath
		taskState.LastError = ""
		taskState.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}); err != nil {
		return err
	}
	a.logger.Printf("[%s] completed -> %s", task.VideoID, videoPath)
	return nil
}

func (a *App) fetchVideoMetadata(ctx context.Context, task Task) (VideoMetadata, error) {
	pageHTML, err := a.fetchText(ctx, task.PageURL, a.config.BaseURL)
	if err != nil {
		return VideoMetadata{}, err
	}

	match := hlsURLRe.FindStringSubmatch(pageHTML)
	if len(match) < 2 {
		return VideoMetadata{}, fmt.Errorf("cannot find hlsUrl in page")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return VideoMetadata{}, err
	}

	title := strings.TrimSpace(doc.Find(`meta[property="og:title"]`).AttrOr("content", ""))
	if title == "" {
		title = strings.TrimSpace(doc.Find("h4").First().Text())
	}
	if title == "" {
		title = task.VideoID
	}

	backgroundURL := absolutizeURL(task.PageURL, strings.TrimSpace(doc.Find(`meta[property="og:image"]`).AttrOr("content", "")))
	if backgroundURL == "" {
		backgroundURL = absolutizeURL(task.PageURL, strings.TrimSpace(doc.Find("video#player").AttrOr("poster", "")))
	}
	releaseDate := ""
	if releaseMatch := releaseDateRe.FindStringSubmatch(pageHTML); len(releaseMatch) > 1 {
		releaseDate = releaseMatch[1]
	}

	genres := extractGenres(doc)
	models := extractModels(doc, task.PageURL)
	duration := a.estimateHLSDuration(ctx, match[1], task.PageURL)

	return VideoMetadata{
		VideoID:       task.VideoID,
		PageURL:       task.PageURL,
		Title:         title,
		HLSURL:        match[1],
		Duration:      duration,
		BackgroundURL: backgroundURL,
		ReleaseDate:   releaseDate,
		Genres:        genres,
		Models:        models,
	}, nil
}

func extractGenres(doc *goquery.Document) []string {
	genres := make([]string, 0)
	seen := map[string]bool{}

	doc.Find("h5.tags a").Each(func(_ int, sel *goquery.Selection) {
		label := strings.TrimSpace(sel.Text())
		if label == "" {
			return
		}
		if !seen[label] {
			seen[label] = true
			genres = append(genres, label)
		}
	})

	return genres
}

func extractModels(doc *goquery.Document, pageURL string) []ModelInfo {
	models := make([]ModelInfo, 0)
	seen := map[string]bool{}

	doc.Find(".models a.model").Each(func(_ int, sel *goquery.Selection) {
		href, ok := sel.Attr("href")
		if !ok {
			return
		}
		modelURL := absolutizeURL(pageURL, href)
		modelID := path.Base(strings.TrimRight(modelURL, "/"))
		if modelID == "" || seen[modelID] {
			return
		}

		name := strings.TrimSpace(sel.Find("[title]").First().AttrOr("title", ""))
		if name == "" {
			name = strings.TrimSpace(sel.Text())
		}
		if name == "" {
			name = modelID
		}

		seen[modelID] = true
		models = append(models, ModelInfo{
			ID:      modelID,
			Name:    name,
			PageURL: modelURL,
		})
	})

	return models
}

func (a *App) fetchText(ctx context.Context, rawURL, referer string) (string, error) {
	body, err := a.fetchBytes(ctx, rawURL, referer)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (a *App) fetchBytes(ctx context.Context, rawURL, referer string) ([]byte, error) {
	resp, err := a.doRequest(ctx, rawURL, referer)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (a *App) doRequest(ctx context.Context, rawURL, referer string) (*http.Response, error) {
	var lastErr error

	for attempt := 1; attempt <= a.config.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", a.config.UserAgent)
		req.Header.Set("Accept-Language", a.config.AcceptLanguage)
		req.Header.Set("Accept", "*/*")
		if referer != "" {
			req.Header.Set("Referer", referer)
			req.Header.Set("Origin", a.config.BaseURL)
		}

		resp, err := a.client.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err == nil {
				err = fmt.Errorf("unexpected status: %s", resp.Status)
			}
		}

		lastErr = err
		if attempt < a.config.MaxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	return nil, fmt.Errorf("request %s failed: %w", rawURL, lastErr)
}

func (a *App) estimateHLSDuration(ctx context.Context, rawURL, referer string) time.Duration {
	body, err := a.fetchText(ctx, rawURL, referer)
	if err != nil {
		return 0
	}

	matches := extinfRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return 0
	}

	var total float64
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		total += value
	}

	if total <= 0 {
		return 0
	}

	return time.Duration(total * float64(time.Second))
}

func (a *App) downloadAssets(ctx context.Context, metadata *VideoMetadata, videoDir string) error {
	if metadata.BackgroundURL != "" {
		backgroundPath, err := a.downloadAsset(ctx, metadata.BackgroundURL, metadata.PageURL, filepath.Join(videoDir, metadata.VideoID+"-background"))
		if err != nil {
			return err
		}
		if backgroundPath != "" {
			metadata.BackgroundLocal = filepath.ToSlash(filepath.Base(backgroundPath))
			coverPath, err := a.generateCoverFromBackground(ctx, backgroundPath, filepath.Join(videoDir, metadata.VideoID+"-cover"))
			if err != nil {
				return err
			}
			if coverPath != "" {
				metadata.CoverLocal = filepath.ToSlash(filepath.Base(coverPath))
			}
		}
	}

	if !a.config.SaveActorImages {
		return nil
	}

	for index := range metadata.Models {
		thumbURL, err := a.resolveModelThumb(ctx, metadata.Models[index].PageURL)
		if err != nil {
			a.logger.Printf("[%s] skip actor image for %s: %v", metadata.VideoID, metadata.Models[index].Name, err)
			continue
		}
		if thumbURL == "" {
			continue
		}

		metadata.Models[index].ThumbURL = thumbURL
		targetPrefix := filepath.Join(videoDir, "actors", "actor-"+metadata.Models[index].ID)
		thumbPath, err := a.downloadAsset(ctx, thumbURL, metadata.Models[index].PageURL, targetPrefix)
		if err != nil {
			a.logger.Printf("[%s] actor image download failed for %s: %v", metadata.VideoID, metadata.Models[index].Name, err)
			continue
		}
		if thumbPath != "" {
			relativePath := filepath.ToSlash(filepath.Join("actors", filepath.Base(thumbPath)))
			metadata.Models[index].LocalThumb = relativePath
		}
	}

	return nil
}

func (a *App) generateCoverFromBackground(ctx context.Context, backgroundPath, targetPrefix string) (string, error) {
	backgroundPath = strings.TrimSpace(backgroundPath)
	if backgroundPath == "" {
		return "", nil
	}

	ext := strings.ToLower(filepath.Ext(backgroundPath))
	if ext == "" {
		ext = ".jpg"
	}

	finalPath := targetPrefix + ext
	tempPath := targetPrefix + ".part" + ext
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", err
	}

	os.Remove(tempPath)

	cmd := exec.CommandContext(
		ctx,
		a.config.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", backgroundPath,
		"-vf", "crop=iw/2:ih:0:0",
		"-frames:v", "1",
		tempPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tempPath)
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("generate cover from %s failed: %s", backgroundPath, message)
	}

	if err := os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return "", err
	}

	return finalPath, nil
}

func (a *App) resolveModelThumb(ctx context.Context, pageURL string) (string, error) {
	pageHTML, err := a.fetchText(ctx, pageURL, a.config.BaseURL)
	if err != nil {
		return "", err
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return "", err
	}

	if metaImage := strings.TrimSpace(doc.Find(`meta[property="og:image"]`).AttrOr("content", "")); isUsefulImage(metaImage) {
		return absolutizeURL(pageURL, metaImage), nil
	}

	selectors := []string{
		".avatar img",
		".model img",
		"img.rounded-circle",
		"img[data-src*='contents/models']",
		"img[src*='contents/models']",
		"img[data-src*='profile']",
		"img[src*='profile']",
	}

	for _, selector := range selectors {
		var found string
		doc.Find(selector).EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			candidate := strings.TrimSpace(sel.AttrOr("src", ""))
			if candidate == "" {
				candidate = strings.TrimSpace(sel.AttrOr("data-src", ""))
			}
			if !isUsefulImage(candidate) {
				return true
			}
			found = absolutizeURL(pageURL, candidate)
			return false
		})
		if found != "" {
			return found, nil
		}
	}

	return "", nil
}

func isUsefulImage(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "placeholder") {
		return false
	}
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp")
}

func (a *App) downloadAsset(ctx context.Context, assetURL, referer, targetPrefix string) (string, error) {
	if strings.TrimSpace(assetURL) == "" {
		return "", nil
	}

	resp, err := a.doRequest(ctx, assetURL, referer)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	ext := guessExtension(assetURL, contentType)
	finalPath := targetPrefix + ext
	tempPath := finalPath + ".part"

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", err
	}

	if fileExists(finalPath) {
		return finalPath, nil
	}

	out, err := os.Create(tempPath)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tempPath)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return "", err
	}

	return finalPath, nil
}

func guessExtension(rawURL, contentType string) string {
	if parsed, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(parsed.Path)); ext != "" && len(ext) <= 5 {
			return ext
		}
	}

	if contentType != "" {
		if extensions, _ := mime.ExtensionsByType(contentType); len(extensions) > 0 {
			return extensions[0]
		}
	}

	return ".jpg"
}

func (a *App) downloadVideo(ctx context.Context, metadata VideoMetadata, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}

	ext := filepath.Ext(outputPath)
	tempPath := strings.TrimSuffix(outputPath, ext) + ".part" + ext
	os.Remove(tempPath)

	headers := fmt.Sprintf("Referer: %s\r\nOrigin: %s\r\nAccept: */*\r\n", metadata.PageURL, a.config.BaseURL)
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-stats_period", "30",
		"-progress", "pipe:2",
		"-nostats",
		"-y",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto,httpproxy",
		"-user_agent", a.config.UserAgent,
		"-headers", headers,
	}
	proxy := strings.TrimSpace(a.config.Proxy)
	if proxy != "" {
		args = append(args, "-http_proxy", proxy)
	}
	args = append(args,
		"-i", metadata.HLSURL,
		"-map", "0:v:0",
		"-map", "0:a?",
		"-c", "copy",
		"-movflags", "+faststart",
		tempPath,
	)

	if metadata.Duration > 0 {
		a.logger.Printf("[%s] 开始下载，视频时长约 %s", metadata.VideoID, formatClock(metadata.Duration))
	} else {
		a.logger.Printf("[%s] 开始下载", metadata.VideoID)
	}
	cmd := exec.CommandContext(ctx, a.config.FFmpegPath, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Env = proxyEnv(proxy)
	if err := cmd.Start(); err != nil {
		return err
	}

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- a.consumeFFmpegOutput(metadata, stderrPipe, &stderr)
	}()

	waitErr := cmd.Wait()
	readErr := <-readErrCh
	if readErr != nil && waitErr == nil {
		waitErr = readErr
	}
	if waitErr != nil {
		os.Remove(tempPath)
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return fmt.Errorf("ffmpeg failed: %s", message)
	}

	if err := os.Rename(tempPath, outputPath); err != nil {
		os.Remove(tempPath)
		return err
	}

	return nil
}

func (a *App) consumeFFmpegOutput(metadata VideoMetadata, reader io.Reader, stderr *bytes.Buffer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var progress ffmpegProgress

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			stderr.WriteString(line)
			stderr.WriteByte('\n')
			a.logger.Printf("[%s] ffmpeg: %s", metadata.VideoID, line)
			continue
		}

		switch key {
		case "speed":
			progress.Speed = value
		case "total_size":
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
				progress.TotalSize = parsed
			}
		case "out_time_us", "out_time_ms":
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
				progress.OutTime = time.Duration(parsed) * time.Microsecond
			}
		case "out_time":
			if progress.OutTime == 0 {
				if parsed, err := parseFFmpegClock(value); err == nil {
					progress.OutTime = parsed
				}
			}
		case "progress":
			a.logFFmpegProgress(metadata, progress, value)
		}
	}

	return scanner.Err()
}

func (a *App) logFFmpegProgress(metadata VideoMetadata, progress ffmpegProgress, stage string) {
	if progress.OutTime <= 0 && progress.TotalSize <= 0 {
		return
	}

	if stage == "end" {
		a.logger.Printf("[%s] 下载完成，正在写入最终文件", metadata.VideoID)
		return
	}

	parts := make([]string, 0, 5)
	if metadata.Duration > 0 && progress.OutTime > 0 {
		percent := float64(progress.OutTime) / float64(metadata.Duration) * 100
		if percent > 100 {
			percent = 100
		}
		parts = append(parts, fmt.Sprintf("%s / %s (%.1f%%)", formatClock(progress.OutTime), formatClock(metadata.Duration), percent))
	} else if progress.OutTime > 0 {
		parts = append(parts, "已下载 "+formatClock(progress.OutTime))
	}
	if progress.TotalSize > 0 {
		parts = append(parts, "文件 "+formatBytes(progress.TotalSize))
	}
	if speed := strings.TrimSpace(progress.Speed); speed != "" && speed != "N/A" {
		if metadata.Duration > 0 && progress.OutTime > 0 {
			if speedValue, ok := parseSpeed(speed); ok && speedValue > 0 && metadata.Duration > progress.OutTime {
				remaining := time.Duration(float64(metadata.Duration-progress.OutTime) / speedValue)
				parts = append(parts, "预计剩余 "+formatShortDuration(remaining))
			}
		}
	}
	if len(parts) == 0 {
		return
	}

	a.logger.Printf("[%s] 下载中: %s", metadata.VideoID, strings.Join(parts, "，"))
}

func (a *App) writeNFO(metadata VideoMetadata, videoDir string) error {
	nfoPath := filepath.Join(videoDir, metadata.VideoID+".nfo")
	movie := MovieNFO{
		Title:         metadata.Title,
		OriginalTitle: metadata.Title,
		Plot:          metadata.Title,
		Outline:       metadata.Title,
		Studio:        "Jable.TV",
		ID:            metadata.VideoID,
		UniqueID: UniqueID{
			Type:    "jable",
			Default: "true",
			Value:   metadata.VideoID,
		},
		Premiered: metadata.ReleaseDate,
		Genres:    metadata.Genres,
	}

	if coverFile := localAssetName(metadata.CoverLocal); coverFile != "" {
		movie.Thumbs = append(movie.Thumbs, ThumbNFO{
			Aspect: "poster",
			Value:  coverFile,
		})
	}
	if backgroundFile := localAssetName(metadata.BackgroundLocal); backgroundFile != "" && backgroundFile != localAssetName(metadata.CoverLocal) {
		movie.Thumbs = append(movie.Thumbs, ThumbNFO{
			Aspect: "fanart",
			Value:  backgroundFile,
		})
	}

	for _, model := range metadata.Models {
		actor := ActorNFO{Name: model.Name}
		if model.LocalThumb != "" {
			actor.Thumb = model.LocalThumb
		}
		movie.Actors = append(movie.Actors, actor)
	}

	xmlBytes, err := xml.MarshalIndent(movie, "", "  ")
	if err != nil {
		return err
	}

	payload := append([]byte(xml.Header), xmlBytes...)
	payload = append(payload, '\n')
	return os.WriteFile(nfoPath, payload, 0o644)
}

func localAssetName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return ""
	}
	return filepath.ToSlash(value)
}

func readState(statePath string) (State, error) {
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return State{Tasks: map[string]*TaskState{}}, nil
	}
	if err != nil {
		return State{}, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{Tasks: map[string]*TaskState{}}, nil
	}
	if state.Tasks == nil {
		state.Tasks = map[string]*TaskState{}
	}
	return state, nil
}

func writeState(statePath string, state State) error {
	if state.Tasks == nil {
		state.Tasks = map[string]*TaskState{}
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tempPath := statePath + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, statePath)
}

func (a *App) outputReady(videoID string) bool {
	videoDir := filepath.Join(a.config.VideosRoot, videoID)
	return fileExists(filepath.Join(videoDir, videoID+".mp4")) &&
		fileExists(filepath.Join(videoDir, videoID+".nfo"))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func proxyEnv(proxy string) []string {
	if strings.TrimSpace(proxy) == "" {
		return os.Environ()
	}

	base := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name := strings.SplitN(entry, "=", 2)[0]
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		base = append(base, entry)
	}
	base = append(base,
		"HTTP_PROXY="+proxy,
		"HTTPS_PROXY="+proxy,
		"http_proxy="+proxy,
		"https_proxy="+proxy,
	)
	return base
}

func parseFFmpegClock(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid ffmpeg clock: %s", value)
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, err
	}

	duration := time.Duration(hours) * time.Hour
	duration += time.Duration(minutes) * time.Minute
	duration += time.Duration(seconds * float64(time.Second))
	return duration, nil
}

func parseSpeed(value string) (float64, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(value, "x"))
	if value == "" || value == "N/A" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func formatClock(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int(duration.Round(time.Second) / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}

	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.2f %s", value, units[index])
}

func formatShortDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Round(time.Second)
	hours := duration / time.Hour
	duration -= hours * time.Hour
	minutes := duration / time.Minute
	duration -= minutes * time.Minute
	seconds := duration / time.Second

	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d分", minutes))
	}
	if seconds > 0 && hours == 0 {
		parts = append(parts, fmt.Sprintf("%d秒", seconds))
	}
	if len(parts) == 0 {
		return "0秒"
	}
	return strings.Join(parts, "")
}

func absolutizeURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.IsAbs() {
		return raw
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}
