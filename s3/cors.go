package s3

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"telecloud/database"
	"time"
)

const defaultS3CORSAllowedHeaders = "Authorization, Content-Type, Content-MD5, Range, X-Amz-Content-Sha256, X-Amz-Date, X-Amz-Security-Token, X-Amz-User-Agent, X-Amz-Copy-Source, X-Amz-Metadata-Directive, X-Amz-Acl, X-Amz-Meta-*"
const s3CORSAllowlistCacheTTL = 30 * time.Second

var s3CORSAllowlistCache struct {
	sync.Mutex
	value     string
	expiresAt time.Time
}

func applyCORSHeaders(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	appendVaryHeader(w, "Origin", "Access-Control-Request-Headers")

	allowed, allowedOrigin := corsAllowedOrigin(origin)
	if !allowed {
		return false
	}

	w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.Header().Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range, Content-Type, ETag, Last-Modified, x-amz-request-id, x-amz-version-id")

	requestedHeaders := r.Header.Get("Access-Control-Request-Headers")
	if requestedHeaders != "" {
		allowedHeaders, ok := allowedCORSRequestHeaders(requestedHeaders)
		if !ok {
			return false
		}
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
		return true
	}
	w.Header().Set("Access-Control-Allow-Headers", defaultS3CORSAllowedHeaders)
	return true
}

func corsAllowedOrigin(origin string) (bool, string) {
	allowedOrigins := resolvedCORSAllowedOrigins()
	if allowedOrigins == "" {
		return true, "*"
	}

	for _, item := range strings.Split(allowedOrigins, ",") {
		allowed := strings.TrimSpace(item)
		switch allowed {
		case "":
			continue
		case "*", "0.0.0.0":
			return true, "*"
		case origin:
			return true, origin
		}
	}
	return false, ""
}

func resolvedCORSAllowedOrigins() string {
	envAllowedOrigins := strings.TrimSpace(os.Getenv("S3_CORS_ALLOWED_ORIGINS"))
	if database.RODB == nil {
		return envAllowedOrigins
	}

	now := time.Now()
	s3CORSAllowlistCache.Lock()
	defer s3CORSAllowlistCache.Unlock()

	if now.Before(s3CORSAllowlistCache.expiresAt) {
		return s3CORSAllowlistCache.value
	}

	allowedOrigins := envAllowedOrigins
	if dbAllowedOrigins := strings.TrimSpace(database.GetSetting("s3_cors_allowed_origins")); dbAllowedOrigins != "" {
		allowedOrigins = dbAllowedOrigins
	}
	s3CORSAllowlistCache.value = allowedOrigins
	s3CORSAllowlistCache.expiresAt = now.Add(s3CORSAllowlistCacheTTL)
	return allowedOrigins
}

func allowedCORSRequestHeaders(requestedHeaders string) ([]string, bool) {
	requested := strings.Split(requestedHeaders, ",")
	allowed := make([]string, 0, len(requested))
	for _, header := range requested {
		header = strings.TrimSpace(header)
		if header == "" {
			continue
		}
		if !isAllowedCORSHeader(header) {
			return nil, false
		}
		allowed = append(allowed, header)
	}
	return allowed, true
}

func isAllowedCORSHeader(header string) bool {
	header = strings.ToLower(strings.TrimSpace(header))
	for _, allowed := range strings.Split(defaultS3CORSAllowedHeaders, ",") {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == header || allowed == "*" {
			return true
		}
		if strings.HasSuffix(allowed, "*") && strings.HasPrefix(header, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

func appendVaryHeader(w http.ResponseWriter, values ...string) {
	seen := make(map[string]bool)
	merged := make([]string, 0, len(values))

	for _, existing := range w.Header().Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, part)
		}
	}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, value)
	}

	w.Header().Set("Vary", strings.Join(merged, ", "))
}
