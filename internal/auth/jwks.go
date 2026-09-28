// Package auth 实现 Ucenter JWT（EdDSA/Ed25519）的本地验签与 Gin 认证中间件。
package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// cacheTTL 公钥缓存有效期，超过后下次使用时重新拉取 JWKS。
const cacheTTL = time.Hour

// jwk JWKS 中单个公钥项（只关心 Ed25519 相关字段）。
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"` // base64url 编码的 32 字节公钥
}

// Fetcher 负责拉取并缓存 Ucenter 的 JWKS 公钥，按 kid 查询。
type Fetcher struct {
	baseURL string // Ucenter 服务地址，如 https://ucenter.june-t.top

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey // kid -> 公钥
	fetchedAt time.Time                    // 上次成功拉取时间
	now       func() time.Time             // 可注入时钟，便于测试
}

// NewFetcher 创建 JWKS 获取器。
func NewFetcher(baseURL string) *Fetcher {
	return &Fetcher{
		baseURL: baseURL,
		keys:    make(map[string]ed25519.PublicKey),
		now:     time.Now,
	}
}

// GetKey 返回 kid 对应的公钥。缓存缺失或已超过 cacheTTL 时重新拉取；
// 拉取失败时若仍有旧缓存则继续使用，否则返回错误。
func (f *Fetcher) GetKey(kid string) (ed25519.PublicKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// 命中且缓存未过期，直接返回（不产生网络请求）
	if pub, ok := f.keys[kid]; ok && f.now().Sub(f.fetchedAt) < cacheTTL {
		return pub, nil
	}

	if err := f.refreshLocked(); err != nil {
		// 拉取失败：回退旧缓存
		if pub, ok := f.keys[kid]; ok {
			return pub, nil
		}
		return nil, err
	}
	pub, ok := f.keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks 中不存在 kid=%q", kid)
	}
	return pub, nil
}

// refreshLocked 重新拉取 JWKS 并替换缓存，调用方须已持锁。
func (f *Fetcher) refreshLocked() error {
	url := strings.TrimRight(f.baseURL, "/") + "/.well-known/jwks.json"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("拉取 JWKS 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("拉取 JWKS 返回状态码 %d", resp.StatusCode)
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("解析 JWKS 失败: %w", err)
	}

	keys := make(map[string]ed25519.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		// 忽略非 OKP/Ed25519 或字段缺失的 key
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" || k.X == "" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys[k.Kid] = ed25519.PublicKey(raw)
	}
	if len(keys) == 0 {
		return fmt.Errorf("jwks 中没有可用的 Ed25519 公钥")
	}
	f.keys = keys
	f.fetchedAt = f.now()
	return nil
}
