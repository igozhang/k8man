package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	Addr   string
	DBPath string
	DataDir string
}

func Load() Config {
	dataDir := getenv("K8MAN_DATA_DIR", "./data")
	dbPath := getenv("K8MAN_DB_PATH", filepath.Join(dataDir, "k8man.db"))
	return Config{
		Addr:    getenv("K8MAN_ADDR", ":8080"),
		DBPath:  dbPath,
		DataDir: dataDir,
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
