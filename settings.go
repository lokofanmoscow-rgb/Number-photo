package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings are remembered between runs.
type Settings struct {
	Start       int    `json:"start"`
	Order       int    `json:"order"`
	AutoSize    bool   `json:"auto_size"`
	DigitHeight int    `json:"digit_height"`
	FontName    string `json:"font"`
	Continue    bool   `json:"continue_numbering"`
	LastDir     string `json:"last_dir"`
	Width       int    `json:"window_width"`
	Height      int    `json:"window_height"`
	Maximized   bool   `json:"maximized"`
}

func defaultSettings() Settings {
	return Settings{Start: 1, AutoSize: true, DigitHeight: 48, Continue: true, Width: 1300, Height: 860}
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "NumberPhoto", "settings.json")
}

func loadSettings() Settings {
	s := defaultSettings()
	p := settingsPath()
	if p == "" {
		return s
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	if s.Width < 600 || s.Height < 400 {
		s.Width, s.Height = 1300, 860
	}
	if s.DigitHeight < 8 {
		s.DigitHeight = 48
	}
	return s
}

func saveSettings(s Settings) {
	p := settingsPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p, data, 0o644)
}
