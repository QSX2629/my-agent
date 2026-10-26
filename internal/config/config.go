package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host         string
	Port         int
	DatabasePath string
	APIKey       string
	ModelBaseURL string
	ModelName    string
	ModelTimeout time.Duration
}

func Load(root string) (Config, error) {
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return Config{}, fmt.Errorf("resolve project root: %w", err)
	}

	envPath := filepath.Join(rootPath, ".env")

	if err := loadEnvFile(envPath); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	port, err := positiveInt("SERVER_PORT", 8000)
	if err != nil {
		return Config{}, err
	}

	if port > 65535 {
		return Config{}, errors.New("SERVER_PORT must be between 1 and 65535")
	}

	timeoutSeconds, err := positiveInt("MODEL_TIMEOUT_SECONDS", 60)
	if err != nil {
		return Config{}, err
	}

	if timeoutSeconds > 86400 {
		return Config{}, errors.New(
			"MODEL_TIMEOUT_SECONDS must not exceed 86400",
		)
	}

	cfg := Config{
		Host:         envOrDefault("SERVER_HOST", "127.0.0.1"),
		Port:         port,
		DatabasePath: envOrDefault("DATABASE_PATH", "var/chat.sqlite3"),
		APIKey:       strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")),
		ModelBaseURL: envOrDefault(
			"MODEL_BASE_URL",
			"https://dashscope.aliyuncs.com/compatible-mode/v1",
		),
		ModelName:    envOrDefault("MODEL_NAME", "qwen-plus"),
		ModelTimeout: time.Duration(timeoutSeconds) * time.Second,
	}

	if cfg.APIKey == "" {
		return Config{}, errors.New(
			"DASHSCOPE_API_KEY is missing: configure it in .env or environment variables",
		)
	}

	modelURL, err := url.Parse(cfg.ModelBaseURL)
	if err != nil {
		return Config{}, errors.New("MODEL_BASE_URL is not a valid URL")
	}

	if (modelURL.Scheme != "http" && modelURL.Scheme != "https") ||
		modelURL.Hostname() == "" ||
		modelURL.User != nil ||
		modelURL.RawQuery != "" ||
		modelURL.Fragment != "" {
		return Config{}, errors.New(
			"MODEL_BASE_URL must be an HTTP/HTTPS URL without credentials, query parameters or fragments",
		)
	}

	if !filepath.IsAbs(cfg.DatabasePath) {
		cfg.DatabasePath = filepath.Join(rootPath, cfg.DatabasePath)
	}

	return cfg, nil
}

func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func envOrDefault(key string, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))

	if value == "" {
		return defaultValue
	}

	return value
}

func positiveInt(key string, defaultValue int) (int, error) {
	value := envOrDefault(key, strconv.Itoa(defaultValue))

	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}

	return number, nil
}

// loadEnvFile 支持简单的 KEY=VALUE 格式。
// 已存在的环境变量优先，文件不存在时允许只使用环境变量。
func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++

		line := strings.TrimSpace(
			strings.TrimPrefix(scanner.Text(), "\uFEFF"),
		)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("invalid .env syntax at line %d", lineNumber)
		}

		// 允许值使用成对的单引号或双引号。
		if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return fmt.Errorf(
					"unclosed quote in .env at line %d",
					lineNumber,
				)
			}

			value = value[1 : len(value)-1]
		}

		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf(
					"set environment variable at line %d: %w",
					lineNumber,
					err,
				)
			}
		}
	}

	return scanner.Err()
}
