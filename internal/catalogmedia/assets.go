package catalogmedia

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const (
	SourceSuffix = ".input"
	CoverSuffix  = ".cover"
)

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	id := hex.EncodeToString(raw[:])
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], nil
}

func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index := range len(id) {
		character := id[index]
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
				return false
			}
		}
	}
	return true
}

func PlaybackFormat(mediaType string) (suffix, contentType string) {
	if mediaType == "audio" {
		return ".m4a", "audio/mp4"
	}
	return ".mp4", "video/mp4"
}

func ParseOrigin(value, name string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("%s must be an HTTP(S) origin", name)
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if (parsed.Scheme == "https" && parsed.Port() == "443") || (parsed.Scheme == "http" && parsed.Port() == "80") {
		host := parsed.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		parsed.Host = host
	}
	return parsed, nil
}
