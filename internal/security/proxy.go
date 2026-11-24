// Package security 提供路径规范化、trusted proxy、IP 解析等安全基础能力。
package security

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// ProxyResolver 按 trusted_proxies 配置解析客户端真实 IP（README §9.3）。
// 只有请求直接来自可信代理时才信任 X-Forwarded-For。
type ProxyResolver struct {
	trusted []*net.IPNet
}

// NewProxyResolver 解析 trusted_proxies 列表，支持单 IP 和 CIDR。
func NewProxyResolver(entries []string) *ProxyResolver {
	r := &ProxyResolver{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			if ip := net.ParseIP(e); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				e = ip.String() + "/" + strconv.Itoa(bits)
			}
		}
		if _, ipnet, err := net.ParseCIDR(e); err == nil {
			r.trusted = append(r.trusted, ipnet)
		}
	}
	return r
}

func (r *ProxyResolver) isTrusted(ip net.IP) bool {
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP 返回客户端真实 IP。
// 请求来自可信代理时取 X-Forwarded-For 最后一个不可信地址，否则取 RemoteAddr。
func (r *ProxyResolver) ClientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil || !r.isTrusted(remote) {
		return host
	}

	xff := req.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	// 从右向左找第一个不可信 IP，即真实客户端。
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if !r.isTrusted(ip) {
			return ip.String()
		}
	}
	// 全部可信时取最左（链路起点）。
	if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
		return ip.String()
	}
	return host
}
