package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvFromRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	if err := os.Mkdir(backend, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("VTB_TEST_DOTENV_VALUE=from_file\nVTB_TEST_DOTENV_OVERRIDE=from_file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VTB_TEST_DOTENV_VALUE", "")
	if err := os.Unsetenv("VTB_TEST_DOTENV_VALUE"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VTB_TEST_DOTENV_OVERRIDE", "from_process")
	t.Chdir(backend)
	if err := loadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VTB_TEST_DOTENV_VALUE") != "from_file" {
		t.Fatal("root .env not loaded")
	}
	if os.Getenv("VTB_TEST_DOTENV_OVERRIDE") != "from_process" {
		t.Fatal("process environment overwritten")
	}
}

func TestLoadDotEnvMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := loadDotEnv(); err != nil {
		t.Fatalf("missing .env should be optional: %v", err)
	}
}

func TestLoadDotEnvMalformed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("VTB_TEST_DOTENV_VALUE=\"unterminated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := loadDotEnv(); err == nil {
		t.Fatal("malformed .env accepted")
	}
}
