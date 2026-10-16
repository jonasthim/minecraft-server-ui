// Package config loads process settings. Mutable application settings live in SQLite.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Address         string
	DataDir         string
	BaseURL         string
	InsecureCookies bool
}

func FromEnvironment() (Config, error) {
	c := Config{Address: "127.0.0.1:8080", DataDir: "./data"}
	if s := os.Getenv("MINECRAFT_UI_ADDR"); s != "" {
		c.Address = s
	}
	if s := os.Getenv("MINECRAFT_UI_DATA_DIR"); s != "" {
		c.DataDir = s
	}
	_, port, err := net.SplitHostPort(c.Address)
	if err != nil {
		return Config{}, fmt.Errorf("MINECRAFT_UI_ADDR: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return Config{}, fmt.Errorf("MINECRAFT_UI_ADDR must contain a port between 1 and 65535")
	}
	c.BaseURL = os.Getenv("MINECRAFT_UI_BASE_URL")
	if c.BaseURL == "" {
		c.BaseURL = "http://" + c.Address
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.Trim(base.Path, "/") != "" {
		return Config{}, fmt.Errorf("MINECRAFT_UI_BASE_URL must be an HTTP(S) origin")
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	ip := net.ParseIP(base.Hostname())
	loopback := base.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	c.InsecureCookies = base.Scheme == "http" && loopback
	if s := os.Getenv("MINECRAFT_UI_INSECURE_COOKIES"); s != "" {
		c.InsecureCookies, err = strconv.ParseBool(s)
		if err != nil {
			return Config{}, fmt.Errorf("MINECRAFT_UI_INSECURE_COOKIES: %w", err)
		}
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("data directory: %w", err)
	}
	return c, nil
}
