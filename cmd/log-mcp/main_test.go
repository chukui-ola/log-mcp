package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLoadConfigValidatesSourcesAndHosts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
  "hosts": [{"id": "local", "type": "local"}],
  "sources": [{"id": "app", "host": "local", "name": "app", "path": "/tmp/app.log"}]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.sourcesByID["app"].Path != "/tmp/app.log" {
		t.Fatalf("source path was not loaded")
	}
	if cfg.MaxBytesPerResponse == 0 || cfg.DefaultLimit == 0 {
		t.Fatalf("defaults were not applied")
	}
}

func TestLoadConfigRejectsUnknownHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
  "hosts": [{"id": "local", "type": "local"}],
  "sources": [{"id": "app", "host": "missing", "name": "app", "path": "/tmp/app.log"}]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("expected unknown host error, got %v", err)
	}
}

func TestRedact(t *testing.T) {
	s := Server{cfg: Config{}}
	re, err := regexp.Compile(`(?i)(token=)[^&\s]+`)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.compiledRedactors = []*regexp.Regexp{re}

	got := s.redact("GET /x?token=abc123 user=42")
	if strings.Contains(got, "abc123") {
		t.Fatalf("secret was not redacted: %s", got)
	}
}
