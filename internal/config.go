package internal

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPAddr      string
	MySQLHost     string
	MySQLPort     string
	MySQLUser     string
	MySQLPassword string
	MySQLDatabase string
	MySQLParams   string
	AdminToken    string
	AppSecret     string
	SMTPHost      string
	SMTPPort      string
	SMTPUser      string
	SMTPPassword  string
	SMTPFrom      string
}

func LoadConfig() (Config, error) {
	c := Config{
		HTTPAddr:      env("HTTP_ADDR", "127.0.0.1:8787"),
		MySQLHost:     env("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:     env("MYSQL_PORT", "3306"),
		MySQLUser:     env("MYSQL_USER", "lawstudy"),
		MySQLPassword: os.Getenv("MYSQL_PASSWORD"),
		MySQLDatabase: env("MYSQL_DATABASE", "law_study"),
		MySQLParams:   env("MYSQL_PARAMS", "charset=utf8mb4&parseTime=true&loc=Local"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
		AppSecret:     os.Getenv("APP_SECRET"),
		SMTPHost:      os.Getenv("SMTP_HOST"),
		SMTPPort:      env("SMTP_PORT", "587"),
		SMTPUser:      os.Getenv("SMTP_USER"),
		SMTPPassword:  os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:      os.Getenv("SMTP_FROM"),
	}
	if c.MySQLUser == "" || c.MySQLDatabase == "" || c.MySQLPassword == "" {
		return Config{}, fmt.Errorf("MYSQL_USER、MYSQL_PASSWORD 和 MYSQL_DATABASE 必须配置")
	}
	if len(c.AppSecret) < 32 {
		return Config{}, fmt.Errorf("APP_SECRET 至少需要 32 个字符")
	}
	if _, err := strconv.Atoi(c.MySQLPort); err != nil {
		return Config{}, fmt.Errorf("MYSQL_PORT 无效: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR 无效: %w", err)
	}
	if c.SMTPHost != "" && c.SMTPFrom == "" {
		return Config{}, fmt.Errorf("配置 SMTP_HOST 时必须同时配置 SMTP_FROM")
	}
	return c, nil
}

func (c Config) MySQLDSN() string {
	params := strings.TrimPrefix(c.MySQLParams, "?")
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", c.MySQLUser, c.MySQLPassword, net.JoinHostPort(c.MySQLHost, c.MySQLPort), c.MySQLDatabase, params)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c Config) SMTPEnabled() bool { return c.SMTPHost != "" && c.SMTPFrom != "" }
