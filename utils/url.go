package utils

import (
	"net"
	"net/url"
	"strings"
)

// IsSocialMediaURL checks if a given URL is from a known social media site
func IsSocialMediaURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")

	socialDomains := []string{
		"youtube.com", "youtu.be",
		"tiktok.com",
		"facebook.com", "fb.watch", "fb.com",
		"instagram.com", "instagr.am",
		"twitter.com", "x.com",
		"twitch.tv",
		"vimeo.com",
		"dailymotion.com",
		"soundcloud.com",
		"reddit.com",
		"threads.net",
		"bilibili.com",
		"douyin.com",
		"kuai.com",
		"kuaishou.com",
	}

	for _, domain := range socialDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}

	return false
}

// IsPrivateIP checks if a URL points to a private/local IP address (SSRF protection)
func IsPrivateIP(urlStr string) bool {
	u, err := url.Parse(urlStr)
	if err != nil {
		return true
	}
	hostname := u.Hostname()
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
		return true
	}

	// Check if the hostname is directly an IP address
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		// On some environments, DNS lookup might fail. If we can't look it up, we allow it to proceed.
		// If it's truly an invalid domain, the HTTP client will fail to connect anyway.
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
			return true
		}
	}
	return false
}
