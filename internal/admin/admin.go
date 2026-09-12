// Package admin — /healthz, /api/version, /api/online и закрытая токеном админка /admin/*.
package admin

import (
	_ "embed"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Info — публичная информация о сборке.
type Info struct {
	Build      string `json:"build"`
	SimVersion string `json:"sim"`
	Proto      int    `json:"proto"`
}

// Register вешает маршруты на роутер и возвращает функцию остановки SSE-потока: её нужно
// вызвать при завершении работы, иначе висящий обработчик съест таймаут graceful shutdown.
//
// mod может быть nil — тогда ручки ролей и банов отвечают ошибкой, а игра работает как раньше.
// trustProxy определяет, верить ли X-Forwarded-For при проверке роли по адресу (см. вход по роли
// «Создатель» ниже) — то же правило, что у игрового WebSocket.
func Register(r *gin.Engine, h *hub.Hub, mod *moderation.Store, info Info, token string, started time.Time, trustProxy bool) func() {
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
		return func() {}
	}
	// Пускаем по токену (им ходят deploy.sh и человек со ссылкой) либо по роли «Создатель»:
	// у него кнопка «Админка» есть прямо в меню игры, и токен ему выдавать незачем. Защита
	// получается ровно такой же, как у самой роли, — она тоже по адресу.
	g := r.Group("/admin", func(c *gin.Context) {
		got := c.GetHeader("X-Admin-Token")
		if got == "" {
			got = c.Query("token")
		}
		if got == token {
			return
		}
		if mod.Rank(ws.ClientIP(c.Request, trustProxy)) == protocol.RankCreator {
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad token"})
	})
	g.GET("/state", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, h.Stats())
	})
	// Ряд онлайна отдаём отдельной ручкой, а не в составе сводки: 10080 точек в каждом SSE-кадре
	// — десятки килобайт впустую, и ряд гарантированно меняется каждую минуту, то есть диффинг
	// потока перестал бы работать.
	g.GET("/online-series", func(c *gin.Context) {
		series := h.OnlineSeries()
		now := time.Now()
		to := unixParam(c.Query("to"), now)
		from := unixParam(c.Query("from"), to.Add(-6*time.Hour))
		if !from.Before(to) {
			from = to.Add(-time.Hour)
		}
		// Потолок поднят под кэш админки: она один раз тянет неделю поминутно (10080 точек,
		// около 150 КБ) и дальше рисует панораму и зум из памяти, не трогая сеть.
		maxPoints := 720
		if v, err := strconv.Atoi(c.Query("max")); err == nil && v > 0 {
			maxPoints = min(v, 12000)
		}
		step, points := series.Points(from, to, maxPoints)
		// Пары вместо объектов: у ряда две величины, а размер ответа это уменьшает втрое.
		pairs := make([][2]int64, 0, len(points))
		for _, p := range points {
			pairs = append(pairs, [2]int64{p.At.Unix(), int64(p.N)})
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"step":   int(step / time.Second),
			"from":   from.Unix(),
			"to":     to.Unix(),
			"points": pairs,
			"broken": series.Broken(),
		})
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

	// Поток состояния: страница не опрашивает сервер, а получает сводку сразу при изменении.
	st := newStreamer(h)
	go st.run(time.Second)
	g.GET("/stream", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-store")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no") // на случай буферизующего прокси
		ch, first, cancel := st.subscribe()
		defer cancel()
		if len(first) > 0 {
			if _, err := c.Writer.Write(sseFrame(first)); err != nil {
				return
			}
			c.Writer.Flush()
		}
		hb := time.NewTicker(20 * time.Second)
		defer hb.Stop()
		for {
			// Три выхода: вкладку закрыли, сервер останавливается, запись не удалась.
			// Без последнего мёртвый клиент за прокси держал бы горутину до перезапуска.
			select {
			case <-c.Request.Context().Done():
				return
			case <-st.stop:
				return
			case body, ok := <-ch:
				if !ok {
					return
				}
				if _, err := c.Writer.Write(sseFrame(body)); err != nil {
					return
				}
				c.Writer.Flush()
			case <-hb.C:
				if _, err := io.WriteString(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	})

	// Модерация. Файл пишем ДО вызовов hub: под его мьютексом ходить в файловую систему нельзя.
	g.POST("/rank", func(c *gin.Context) {
		var req struct {
			IP   string `json:"ip"`
			Rank string `json:"rank"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || net.ParseIP(req.IP) == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad ip"})
			return
		}
		switch req.Rank {
		case protocol.RankPlayer, protocol.RankAdmin, protocol.RankCreator:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad rank"})
			return
		}
		if err := mod.SetRank(req.IP, req.Rank, nickByIP(h, req.IP), time.Now()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		h.ApplyRank(req.IP)
		c.JSON(http.StatusOK, gin.H{"ip": req.IP, "rank": req.Rank})
	})
	g.POST("/ban", func(c *gin.Context) {
		var req struct {
			IP     string `json:"ip"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || net.ParseIP(req.IP) == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad ip"})
			return
		}
		if mod.Rank(req.IP) == protocol.RankCreator {
			c.JSON(http.StatusConflict, gin.H{"error": "нельзя забанить создателя: сначала снимите роль"})
			return
		}
		if err := mod.Ban(req.IP, nickByIP(h, req.IP), req.Reason, time.Now()); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ip": req.IP, "kicked": h.ApplyBan(req.IP)})
	})
	g.DELETE("/ban", func(c *gin.Context) {
		ip := c.Query("ip")
		if net.ParseIP(ip) == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad ip"})
			return
		}
		if err := mod.Unban(ip); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ip": ip, "banned": false})
	})
	g.POST("/chat/clear", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"cleared": h.ClearChat()})
	})
	return st.Close
}

// unixParam разбирает границу окна графика; мусор и пустая строка дают значение по умолчанию.
func unixParam(v string, def time.Time) time.Time {
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil || sec <= 0 {
		return def
	}
	return time.Unix(sec, 0)
}

// nickByIP — ник любой живой сессии с адреса: чтобы список ролей и банов читался глазами,
// а не был набором чисел.
func nickByIP(h *hub.Hub, ip string) string {
	if nicks := h.SessionsByIP(ip); len(nicks) > 0 {
		return nicks[0]
	}
	return ""
}

// Страница админки: обычный HTML без сборки, вшит в бинарник.
//
//go:embed admin.html
var adminPage []byte
