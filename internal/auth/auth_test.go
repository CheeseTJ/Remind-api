package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// keySet 一对测试密钥及对应 kid（模拟 Ucenter：kid = base64url(sha256(x))[:16]）。
type keySet struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	kid  string
}

func newKeySet(t *testing.T) keySet {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥对失败: %v", err)
	}
	sum := sha256.Sum256(pub)
	return keySet{priv: priv, pub: pub, kid: base64.RawURLEncoding.EncodeToString(sum[:])[:16]}
}

// newEnv 起一个模拟 JWKS 端点（返回 signing 的公钥），带请求计数器，
// 并创建指向它的 Fetcher（时钟可由测试注入覆盖 f.now）。
func newEnv(t *testing.T, signing keySet) (f *Fetcher, counter *atomic.Int32) {
	t.Helper()
	counter = &atomic.Int32{}
	handler := http.NewServeMux()
	handler.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		doc := map[string]any{
			"keys": []map[string]string{{
				"kty": "OKP",
				"crv": "Ed25519",
				"kid": signing.kid,
				"x":   base64.RawURLEncoding.EncodeToString(signing.pub),
				"alg": "EdDSA",
				"use": "sig",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewFetcher(srv.URL), counter
}

// signToken 用指定密钥与 kid 签出 Ucenter 风格的 access token。
func signToken(t *testing.T, k keySet, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = k.kid
	s, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatalf("签名 token 失败: %v", err)
	}
	return s
}

// newApp 挂上中间件的路由，回显中间件写入的 uid/email 便于断言。
func newApp(aud string, f *Fetcher) *gin.Engine {
	r := gin.New()
	r.GET("/protected", Middleware(f, aud), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"uid": c.GetString("uid"), "email": c.GetString("email")})
	})
	return r
}

func doReq(r *gin.Engine, header string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	r.ServeHTTP(w, req)
	return w
}

func baseClaims(aud string, expOffset int64) jwt.MapClaims {
	now := time.Now().Unix()
	return jwt.MapClaims{
		"iss":       "https://ucenter.june-t.top",
		"sub":       "user-123",
		"aud":       aud,
		"iat":       now,
		"exp":       now + expOffset,
		"device_id": "dev-abc",
		"email":     "u@example.com",
		"role":      "user",
	}
}

func TestMiddleware_Success(t *testing.T) {
	ks := newKeySet(t)
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	w := doReq(r, "Bearer "+signToken(t, ks, baseClaims("remind", 900)))
	if w.Code != http.StatusOK {
		t.Fatalf("合法 token 应 200，得到 %d: %s", w.Code, w.Body.String())
	}
	var body struct{ UID, Email string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.UID != "user-123" || body.Email != "u@example.com" {
		t.Fatalf("context 中的 uid/email 不正确: %+v", body)
	}
}

func TestMiddleware_Expired(t *testing.T) {
	ks := newKeySet(t)
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	w := doReq(r, "Bearer "+signToken(t, ks, baseClaims("remind", -60)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("过期 token 应 401，得到 %d", w.Code)
	}
	if w.Body.String() != `{"error":"unauthorized"}` {
		t.Fatalf("401 响应体不符: %s", w.Body.String())
	}
}

func TestMiddleware_WrongAud(t *testing.T) {
	ks := newKeySet(t)
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	w := doReq(r, "Bearer "+signToken(t, ks, baseClaims("music", 900)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("aud=music 应 401，得到 %d", w.Code)
	}
}

func TestMiddleware_BadSignature(t *testing.T) {
	ks := newKeySet(t)
	other := newKeySet(t) // 用另一把私钥签名，但 kid 指向 JWKS 中公钥
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, baseClaims("remind", 900))
	tok.Header["kid"] = ks.kid
	s, err := tok.SignedString(other.priv)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	w := doReq(r, "Bearer "+s)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误签名应 401，得到 %d", w.Code)
	}
}

func TestMiddleware_BadHeaders(t *testing.T) {
	ks := newKeySet(t)
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	for name, header := range map[string]string{
		"无 Authorization 头": "",
		"Bearer 后无 token":   "Bearer ",
		"非 Bearer 认证方案":     "Basic dXNlcjpwYXNz",
		"token 格式损坏":        "Bearer abc.def",
	} {
		t.Run(name, func(t *testing.T) {
			w := doReq(r, header)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("应 401，得到 %d", w.Code)
			}
		})
	}
}

func TestMiddleware_UnknownKid(t *testing.T) {
	ks := newKeySet(t)
	unknown := newKeySet(t) // JWKS 中没有这个 kid
	f, _ := newEnv(t, ks)
	r := newApp("remind", f)

	w := doReq(r, "Bearer "+signToken(t, unknown, baseClaims("remind", 900)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("kid 不存在应 401，得到 %d", w.Code)
	}
}

// TestFetcher_CacheHit 验证缓存有效期内重复请求不再拉取 JWKS。
func TestFetcher_CacheHit(t *testing.T) {
	ks := newKeySet(t)
	f, counter := newEnv(t, ks)

	for i := 0; i < 5; i++ {
		if _, err := f.GetKey(ks.kid); err != nil {
			t.Fatalf("GetKey 失败: %v", err)
		}
	}
	if n := counter.Load(); n != 1 {
		t.Fatalf("缓存命中时 JWKS 应只请求 1 次，实际 %d 次", n)
	}
}

// TestFetcher_RefreshAfterTTL 验证超过 1 小时后自动重新拉取，
// 且拉取失败时回退旧缓存。
func TestFetcher_RefreshAfterTTL(t *testing.T) {
	ks := newKeySet(t)
	f, counter := newEnv(t, ks)
	now := time.Now()
	f.now = func() time.Time { return now } // 注入时钟

	// 首次拉取 + 缓存期内命中
	if _, err := f.GetKey(ks.kid); err != nil {
		t.Fatalf("GetKey 失败: %v", err)
	}
	if _, err := f.GetKey(ks.kid); err != nil {
		t.Fatalf("GetKey 失败: %v", err)
	}
	if n := counter.Load(); n != 1 {
		t.Fatalf("刷新前应只请求 1 次，实际 %d 次", n)
	}

	// 推进 61 分钟：应重新拉取
	now = now.Add(61 * time.Minute)
	if _, err := f.GetKey(ks.kid); err != nil {
		t.Fatalf("过期后 GetKey 失败: %v", err)
	}
	if n := counter.Load(); n != 2 {
		t.Fatalf("超过 1 小时应重新拉取，实际请求 %d 次", n)
	}

	// 再次过期且 JWKS 端点故障：应回退旧缓存
	now = now.Add(2 * time.Hour)
	// 通过自定义 Transport 模拟 500：直接把 baseURL 指向坏端点更简单
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	f.mu.Lock()
	f.baseURL = srv.URL
	f.mu.Unlock()

	pub, err := f.GetKey(ks.kid)
	if err != nil {
		t.Fatalf("拉取失败时应回退旧缓存: %v", err)
	}
	if string(pub) != string(ks.pub) {
		t.Fatal("回退的旧公钥不符")
	}
	if n := counter.Load(); n != 3 {
		t.Fatalf("故障端点应被请求 1 次，实际 %d 次", n)
	}
}

// TestFetcher_RefreshDropsKid 验证刷新后 JWKS 中不存在的 kid 会报错。
func TestFetcher_RefreshDropsKid(t *testing.T) {
	ks := newKeySet(t)
	gone := newKeySet(t)
	f, counter := newEnv(t, ks)
	now := time.Now()
	f.now = func() time.Time { return now }

	if _, err := f.GetKey(ks.kid); err != nil {
		t.Fatalf("GetKey 失败: %v", err)
	}
	now = now.Add(2 * time.Hour) // 触发重新拉取
	if _, err := f.GetKey(gone.kid); err == nil {
		t.Fatal("刷新后不存在的 kid 应报错")
	}
	if n := counter.Load(); n != 2 {
		t.Fatalf("应重新拉取 1 次，实际 %d 次", n)
	}
}
