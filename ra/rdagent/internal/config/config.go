package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/pion/webrtc/v4"
	"os"

	"github.com/google/uuid"
)

type Config struct {
	WSURL          string
	APIKey         string
	ClientID       string
	SessionID      string
	Token          string
	InstanceID     string
	PrivateKeyFile string

	LogDir        string
	LogLevel      string
	LogRetainDays int

	InsecureSkipVerify bool

	TURNJSON   string
	ICEServers []webrtc.ICEServer
}

func Parse(args []string) (Config, error) {
	var cfg Config

	fs := flag.NewFlagSet("rd-agent", flag.ContinueOnError)

	fs.StringVar(&cfg.WSURL, "ws-url", "", "WebSocket URL, for example wss://host/ws")
	fs.StringVar(&cfg.APIKey, "api-key", "", "NoIP client API key")
	fs.StringVar(&cfg.ClientID, "client-id", "", "Client UUID")
	fs.StringVar(&cfg.SessionID, "session-id", "", "RD/CMD session id")
	fs.StringVar(&cfg.Token, "token", "", "One-time session-bound RD token")
	fs.StringVar(&cfg.InstanceID, "instance-id", "", "Optional rd-agent instance id")
	fs.StringVar(&cfg.PrivateKeyFile, "private-key-file", "", "Path to Ed25519 private key PEM file")

	fs.StringVar(&cfg.LogDir, "log-dir", "data/logs", "Directory for rd-agent logs")
	fs.StringVar(&cfg.LogLevel, "log-level", "INFO", "Log level: DEBUG, INFO, WARNING, ERROR")
	fs.IntVar(&cfg.LogRetainDays, "log-retain-days", 14, "How many days rotated logs are retained")

	fs.BoolVar(&cfg.InsecureSkipVerify, "insecure-skip-verify", false, "Disable TLS certificate verification for wss")
	fs.StringVar(&cfg.TURNJSON, "turn-json", os.Getenv("RD_TURN_JSON"), "TURN/STUN JSON config")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if err := cfg.parseICEServers(); err != nil {
		return Config{}, err
	}

	if cfg.InstanceID == "" {
		cfg.InstanceID = uuid.NewString()
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) parseICEServers() error {
	if c.TURNJSON == "" {
		return nil
	}

	var raw struct {
		ICEServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username,omitempty"`
			Credential string   `json:"credential,omitempty"`
		} `json:"ice_servers"`
	}

	if err := json.Unmarshal([]byte(c.TURNJSON), &raw); err != nil {
		return fmt.Errorf("parse TURN JSON: %w", err)
	}

	for _, s := range raw.ICEServers {
		c.ICEServers = append(c.ICEServers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}
	return nil
}

func (c Config) Validate() error {
	if c.WSURL == "" {
		return errors.New("--ws-url is required")
	}
	if c.APIKey == "" {
		return errors.New("--api-key is required")
	}
	if c.ClientID == "" {
		return errors.New("--client-id is required")
	}
	if c.SessionID == "" {
		return errors.New("--session-id is required")
	}
	if c.Token == "" {
		return errors.New("--token is required")
	}
	if c.PrivateKeyFile == "" {
		return errors.New("--private-key-file is required")
	}
	if c.LogRetainDays <= 0 {
		return fmt.Errorf("--log-retain-days must be positive")
	}
	return nil
}
