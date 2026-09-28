// Remind 备忘提醒服务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // 内嵌时区数据，保证单二进制可加载 Asia/Shanghai

	"github.com/gin-gonic/gin"

	"remind/internal/auth"
	"remind/internal/config"
	"remind/internal/mailer"
	"remind/internal/memo"
	"remind/internal/scheduler"
	"remind/internal/store"
)

func main() {
	// 解析启动参数
	configPath := flag.String("config", "./config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 打开数据库（自动建表）
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	// 邮件发送器
	mail := mailer.New(mailer.SMTPConfig{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
	})

	// 注入 next_date 钩子：从今天（北京）起向后找最多 500 天内的第一个发生日
	memo.NextOccurrenceHook = scheduler.NextOccurrence

	gin.SetMode(gin.ReleaseMode) // 生产模式：关闭路由表与调试日志
	r := gin.Default()

	// CORS：前端跨站部署（remind.taojun.top 的 Worker）时允许浏览器直连。
	// 认证走 Authorization 头、无 Cookie 依赖，放开来源即可；预检直接 204。
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// 健康检查
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// /api 路由组：Ucenter JWT 认证 + 备忘接口 + 测试邮件 + 元信息
	fetcher := auth.NewFetcher(cfg.UcenterBaseURL)
	api := r.Group("/api")
	api.Use(auth.Middleware(fetcher, cfg.AppName))
	memo.RegisterRoutes(api, db)

	// POST /api/test-email：向当前登录用户发一封测试邮件
	api.POST("/test-email", func(c *gin.Context) {
		email := c.GetString("email")
		if email == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法获取用户邮箱"})
			return
		}
		body := `<!DOCTYPE html><html lang="zh-CN"><body style="font-family:'PingFang SC','Microsoft YaHei',sans-serif;">` +
			`<p style="font-size:14px;color:#5a4a38;">这是一封来自「岁记」的测试邮件，收到即说明 SMTP 配置正常。</p>` +
			`</body></html>`
		if err := mail.SendWithRetry(email, "[岁记] 测试邮件", body); err != nil {
			log.Printf("测试邮件发送失败 to=%s: %v", email, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "发送失败，请稍后重试"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "已发送"})
	})

	// GET /api/meta：供前端展示发送时刻等元信息
	api.GET("/meta", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"app_name":        cfg.AppName,
			"daily_send_time": cfg.DailySendTime,
			"timezone":        "Asia/Shanghai",
		})
	})

	// 前后端分离：本服务只提供 API，无页面。/api/* 未匹配返回 404 JSON，其余返回纯文本
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.String(http.StatusNotFound, "not found")
	})

	// 每日调度器：ctx 随优雅退出取消
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	scheduler.New(db, mail, cfg.DailySendTime).Start(schedCtx)

	// 启动 HTTP 服务
	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: r,
	}
	go func() {
		log.Printf("Remind(%s) 启动，监听 %s", cfg.AppName, cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	// 监听退出信号，优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("收到退出信号，正在关闭...")

	schedCancel() // 先停调度器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP 服务关闭异常: %v", err)
	}
	log.Println("已退出")
}
