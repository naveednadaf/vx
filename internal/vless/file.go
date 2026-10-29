package vless

import (
	"os"
	"strings"
)

func readFileOrDecode(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	if strings.Contains(text, "vless://") {
		return text, nil
	}
	if dec, ok := DecodeBase64(text); ok {
		return dec, nil
	}
	return text, nil
}
