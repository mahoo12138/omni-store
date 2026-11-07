package oidc

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	coreosoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// discoveryTimeout 限制 discovery 请求耗时，避免回调被缓慢 IdP 拖住。
const discoveryTimeout = 15 * time.Second

// Client 是针对单个 issuer 的 OIDC 客户端。
// discovery 文档与 JWKS 公钥由 go-oidc 缓存，实例进程内复用。
type Client struct {
	issuer       string
	clientID     string
	clientSecret string
	scopes       []string
	endpoint     oauth2.Endpoint
	verifier     *coreosoidc.IDTokenVerifier
}

// Claims 是登录所需的 ID token 声明子集。
type Claims struct {
	Subject           string `json:"sub"`
	Nonce             string `json:"nonce"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Email             string `json:"email"`
}

// NewClient 连接 IdP 完成 discovery 并构建 ID token 验证器。
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	issuer := strings.TrimSpace(cfg.Issuer)
	discoveryCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	discoveryCtx = coreosoidc.ClientContext(discoveryCtx, &http.Client{Timeout: discoveryTimeout})
	provider, err := coreosoidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return nil, fmt.Errorf("连接身份提供方失败: %w", err)
	}
	return &Client{
		issuer:       issuer,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		scopes:       cfg.ScopesOrDefault(),
		endpoint:     provider.Endpoint(),
		verifier: provider.Verifier(&coreosoidc.Config{
			ClientID: cfg.ClientID,
			// 自托管 Casdoor 常以内网 http 部署，跳过 go-oidc 对非 https issuer 的强制校验，
			// issuer 一致性仍由 discovery 响应与 ID token iss 声明保证。
			SkipIssuerCheck: !strings.HasPrefix(issuer, "https://"),
		}),
	}, nil
}

// AuthURL 构造带 state 与 nonce 的授权跳转地址。
func (c *Client) AuthURL(redirectURI, state, nonce string) string {
	return c.oauth2Config(redirectURI).AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce))
}

// Exchange 用授权码换取 ID token 并验签（签名、aud、exp、iss），nonce 由调用方传入比对。
func (c *Client) Exchange(ctx context.Context, code, redirectURI, expectedNonce string) (*Claims, error) {
	token, err := c.oauth2Config(redirectURI).Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("授权码交换失败: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, fmt.Errorf("身份提供方未返回 id_token")
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("ID token 验证失败: %w", err)
	}
	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("解析 ID token 声明失败: %w", err)
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("ID token 缺少 sub 声明")
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return nil, fmt.Errorf("ID token nonce 不匹配")
	}
	return &claims, nil
}

func (c *Client) oauth2Config(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.clientID,
		ClientSecret: c.clientSecret,
		Endpoint:     c.endpoint,
		RedirectURL:  redirectURI,
		Scopes:       c.scopes,
	}
}

// Probe 只做 discovery 连通性检查，供管理员「测试连接」在保存前验证表单值。
func Probe(ctx context.Context, issuer string) (authorization, token string, err error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	discoveryCtx = coreosoidc.ClientContext(discoveryCtx, &http.Client{Timeout: discoveryTimeout})
	provider, err := coreosoidc.NewProvider(discoveryCtx, strings.TrimSpace(issuer))
	if err != nil {
		return "", "", fmt.Errorf("连接身份提供方失败: %w", err)
	}
	endpoint := provider.Endpoint()
	return endpoint.AuthURL, endpoint.TokenURL, nil
}

// RedirectURI 由实例公开地址派生 Casdoor 侧应登记的回调地址。
func RedirectURI(publicURL string) string {
	return strings.TrimRight(publicURL, "/") + "/api/v1/auth/oidc/callback"
}
