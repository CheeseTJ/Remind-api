package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// unauthorized 统一返回 401 并终止请求链。
func unauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
}

// bearerToken 从 Authorization 头提取 Bearer 令牌，格式不符返回空串。
func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

// Middleware 校验 Ucenter 签发的 JWT：Bearer 令牌须用 JWKS 中公钥
// 通过 EdDSA 验签，且 exp 有效、aud 等于参数 aud。
// 成功后把 sub 写入 context("uid")、email 写入 context("email")。
func Middleware(f *Fetcher, aud string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			unauthorized(c)
			return
		}

		// 先不验签，解析出 header 里的 kid 用于查公钥
		unverified, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
		if err != nil {
			unauthorized(c)
			return
		}
		kid, _ := unverified.Header["kid"].(string)

		pub, err := f.GetKey(kid)
		if err != nil {
			unauthorized(c)
			return
		}

		// 验签 + 校验 exp（必须存在、未过期）与 aud（须匹配）
		parsed, err := jwt.NewParser(
			jwt.WithValidMethods([]string{"EdDSA"}),
			jwt.WithExpirationRequired(),
			jwt.WithAudience(aud),
		).Parse(token, func(*jwt.Token) (any, error) {
			return pub, nil
		})
		if err != nil || !parsed.Valid {
			unauthorized(c)
			return
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			unauthorized(c)
			return
		}

		sub, _ := claims["sub"].(string)
		email, _ := claims["email"].(string)
		c.Set("uid", sub)
		c.Set("email", email)
		c.Next()
	}
}
