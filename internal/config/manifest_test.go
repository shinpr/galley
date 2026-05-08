package config

import (
	"path/filepath"
	"testing"
)

func TestLoadManifestExample(t *testing.T) {
	path, err := filepath.Abs("../../examples/repos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Defaults.PRBase != "main" {
		t.Fatalf("manifest got %#v", manifest)
	}
}
