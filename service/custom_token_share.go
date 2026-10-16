package service

import (
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/setting/system_setting"
)

func CustomTokenShareURL(code string) string {
	if code == "" {
		return ""
	}
	// Never derive credential-bearing links from Host/X-Forwarded-Host.
	base := strings.TrimSpace(os.Getenv("CUSTOM_TOKEN_SHARE_BASE_URL"))
	if base == "" {
		base = strings.TrimSpace(system_setting.ServerAddress)
	}
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		base = ""
	} else {
		parsed.RawQuery, parsed.Fragment = "", ""
		base = strings.TrimRight(parsed.String(), "/")
	}
	return base + "/ck/" + code
}
