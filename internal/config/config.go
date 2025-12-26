package config

import (
	"os"
)

type Config struct {
	TSAuthKey string
	DataDir   string
}

func Load() *Config {
	return &Config{
		TSAuthKey: getEnv("TS_AUTH_KEY", ""),
		DataDir:   getEnv("DATA_DIR", "./data"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
