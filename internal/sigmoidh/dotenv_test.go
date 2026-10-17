package sigmoidh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnvExpandsHome(t *testing.T) {
	if os.Getenv("TEST_DOTENV_HB_KEY") != "" {
		t.Skip("key already present in environment")
	}
	home := t.TempDir()
	sub := filepath.Join(home, "ssv")
	os.MkdirAll(sub, 0o755)
	file := filepath.Join(sub, "kv.env")
	os.WriteFile(file, []byte("TEST_DOTENV_HB_KEY=loaded\n"), 0o644)

	t.Setenv("HOME", home)
	os.Unsetenv("TEST_DOTENV_HB_KEY")

	t.Setenv("SIGMOIDH_ENV_FILE", "~/ssv/kv.env")
	os.Unsetenv("SIGMOIDH_ENV_FILE")
	os.Setenv("SIGMOIDH_ENV_FILE", filepath.Join(sub, "kv.env"))
	defer os.Unsetenv("SIGMOIDH_ENV_FILE")

	literal := strings.Replace(file, sub, "~/ssv", 1)
	if expandHome("~/ssv/kv.env") != file {
		t.Fatalf("expandHome returned %q, want %q", expandHome("~/ssv/kv.env"), file)
	}
	loadDotEnv(literal)
	if os.Getenv("TEST_DOTENV_HB_KEY") != "loaded" {
		t.Fatalf("~ path not expanded; key = %q", os.Getenv("TEST_DOTENV_HB_KEY"))
	}
	os.Unsetenv("TEST_DOTENV_HB_KEY")
}
