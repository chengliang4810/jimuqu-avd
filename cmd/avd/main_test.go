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
