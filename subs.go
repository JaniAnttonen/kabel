package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Subtitle preference: by default pick a track matching the region's
// languages; once the user cycles subtitles manually the choice (language
// or "off") is remembered across channels and sessions.

// regionSubLangs maps home country to preferred DVB subtitle languages
// (ISO 639-2/B codes as used in broadcasts, in preference order).
var regionSubLangs = map[string][]string{
	"FI": {"fin", "swe"},
	"SE": {"swe"},
	"NO": {"nor", "nob"},
	"DK": {"dan"},
	"EE": {"est"},
	"DE": {"ger", "deu"},
	"AT": {"ger", "deu"},
	"CH": {"ger", "deu", "fre", "fra", "ita"},
	"NL": {"dut", "nld"},
	"BE": {"dut", "nld", "fre", "fra"},
	"FR": {"fre", "fra"},
	"ES": {"spa"},
	"PT": {"por"},
	"IT": {"ita"},
	"PL": {"pol"},
	"CZ": {"cze", "ces"},
	"GB": {"eng"},
	"IE": {"eng"},
	"US": {"eng"},
}

// defaultSubPref returns the region-based preference ("" when unknown,
// which behaves like off until the user picks something).
func defaultSubPref(home string) string {
	if langs, ok := regionSubLangs[home]; ok {
		return strings.Join(langs, ",")
	}
	return ""
}

func subPrefPath() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "subtitles"), nil
}

// loadSubPref returns the persisted preference and whether one exists.
func loadSubPref() (string, bool) {
	path, err := subPrefPath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func saveSubPref(pref string) {
	path, err := subPrefPath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(pref+"\n"), 0o644)
}
