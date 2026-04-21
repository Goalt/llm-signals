package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type apiProxyConfig struct {
	RoutePrefix   string
	TargetBaseURL string
	Authorization string
	Name          string
}

func newAPIProxy(cfg apiProxyConfig) (http.Handler, error) {
	target, err := url.Parse(strings.TrimSpace(cfg.TargetBaseURL))
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	targetQuery := target.RawQuery
	targetPath := target.Path
	if targetPath == "" {
		targetPath = "/"
	}

	proxy.Director = func(req *http.Request) {
		sourceQuery := req.URL.RawQuery
		sourcePath := req.URL.Path

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		req.URL.Path = joinURLPath(targetPath, pathSuffix(sourcePath, cfg.RoutePrefix))
		if targetQuery == "" || sourceQuery == "" {
			req.URL.RawQuery = targetQuery + sourceQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + sourceQuery
		}

		req.Header.Del("Authorization")
		if auth := strings.TrimSpace(cfg.Authorization); auth != "" {
			req.Header.Set("Authorization", auth)
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy error (%s): %v", cfg.Name, err)
		http.Error(w, "Upstream request failed", http.StatusBadGateway)
	}

	return proxy, nil
}

func matchesProxyRoute(path, routePrefix string) bool {
	return path == routePrefix || strings.HasPrefix(path, routePrefix+"/")
}

func pathSuffix(path, routePrefix string) string {
	if !matchesProxyRoute(path, routePrefix) {
		return "/"
	}

	suffix := strings.TrimPrefix(path, routePrefix)
	if suffix == "" {
		return "/"
	}
	if !strings.HasPrefix(suffix, "/") {
		return "/" + suffix
	}
	return suffix
}

func joinURLPath(basePath, suffix string) string {
	if strings.HasSuffix(basePath, "/") {
		basePath = strings.TrimSuffix(basePath, "/")
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	joined := basePath + suffix
	if joined == "" {
		return "/"
	}
	return joined
}
