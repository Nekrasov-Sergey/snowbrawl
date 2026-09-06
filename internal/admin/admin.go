// Package admin — /healthz, /api/version, /api/online и закрытая токеном админка /admin/*.
package admin

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// Info — публичная информация о сборке.
type Info struct {
	Build      string `json:"build"`
	SimVersion string `json:"sim"`
	Proto      int    `json:"proto"`
}

// Register вешает маршруты на роутер.
func Register(r *gin.Engine, h *hub.Hub, info Info, token string, started time.Time) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "build": info.Build, "uptime": time.Since(started).Round(time.Second).String()})
	})
	r.GET("/api/version", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, info)
	})
	// Публичный счётчик онлайна для главного меню клиента.
	r.GET("/api/online", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"online": h.Online()})
	})
	if token == "" {
		return
	}
	g := r.Group("/admin", func(c *gin.Context) {
		got := c.GetHeader("X-Admin-Token")
		if got == "" {
			got = c.Query("token")
		}
		if got != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad token"})
		}
	})
	g.GET("/state", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, h.Stats())
	})
	// Дренаж включает и снимает только deploy.sh при выкладке. Кнопок в интерфейсе нет:
	// вручную это нажимать незачем, а последствия (новые матчи не стартуют) неочевидны.
	g.POST("/drain", func(c *gin.Context) {
		h.SetDrain(true)
		c.JSON(http.StatusOK, gin.H{"draining": true})
	})
	g.DELETE("/drain", func(c *gin.Context) {
		h.SetDrain(false)
		c.JSON(http.StatusOK, gin.H{"draining": false})
	})
	g.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", adminPage)
	})
	_ = protocol.Version
}

// Страница админки: обычный HTML без сборки, вшит в бинарник.
//
//go:embed admin.html
var adminPage []byte
