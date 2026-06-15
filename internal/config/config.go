package config

import (
	"os"
	"strconv"
)

const defaultBodyLimit = 50 * 1024 * 1024 * 1024 // 50GB

type Config struct {
	TSAuthKey    string
	DataDir      string
	Hostname     string // Tailscale node hostname (and DNS name)
	BodyLimit    int    // max request body size in bytes (legacy single-shot upload)
	LocalAddr    string // local debug listener address; empty disables it
	UserQuota    int64  // max total bytes a single user may store; 0 = unlimited
	MinFreeBytes int64  // refuse uploads that would leave less than this free; 0 = file must merely fit
}

func Load() *Config {
	return &Config{
		TSAuthKey:    getEnv("TS_AUTH_KEY", ""),
		DataDir:      getEnv("DATA_DIR", "./data"),
		Hostname:     getEnv("TS_HOSTNAME", "fastnas"),
		BodyLimit:    getEnvInt("MAX_UPLOAD_SIZE", defaultBodyLimit),
		LocalAddr:    getEnv("LOCAL_ADDR", ":8080"),
		UserQuota:    getEnvInt64("USER_QUOTA", 0),
		MinFreeBytes: getEnvInt64("MIN_FREE_BYTES", 0),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}
