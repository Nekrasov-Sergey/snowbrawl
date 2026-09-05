// Package admin — /healthz, /api/version, /api/online и закрытая токеном админка /admin/*.
package admin

import (
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
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(adminPage))
	})
	_ = protocol.Version
}

const adminPage = `<!DOCTYPE html><html lang="ru"><head><meta charset="utf-8"><title>SnowBrawl admin</title>
<style>body{font-family:system-ui,sans-serif;background:#0e1a2b;color:#eaf2ff;padding:20px;max-width:900px;margin:0 auto}
h1{font-size:22px}table{border-collapse:collapse;width:100%;margin:10px 0}td,th{border:1px solid #3a5a80;padding:6px 8px;font-size:13px;text-align:left}
button{background:#4aa8ff;border:none;color:#06121f;padding:8px 14px;border-radius:6px;font-weight:700;cursor:pointer;margin-right:8px}
button.warn{background:#ff5b5b}.pill{display:inline-block;padding:2px 8px;border-radius:10px;background:#274363;font-size:12px;margin-right:6px}
.drain{color:#ffd166;font-weight:700}</style></head><body>
<h1>❄️ SnowBrawl — админка</h1>
<div id="sum"></div>
<p><button onclick="drain(true)" class="warn">Начать дренаж</button><button onclick="drain(false)">Отменить дренаж</button></p>
<h3>Матчи</h3><table id="matches"></table>
<h3>Комнаты</h3><table id="rooms"></table>
<h3>Очереди</h3><table id="queues"></table>
<script>
const token = new URLSearchParams(location.search).get('token') || '';
async function load(){
  const r = await fetch('/admin/state?token='+encodeURIComponent(token)); if(!r.ok){document.getElementById('sum').textContent='Ошибка: '+r.status;return;}
  const s = await r.json();
  document.getElementById('sum').innerHTML = '<span class="pill">build '+s.build+'</span><span class="pill">sim '+s.sim+'</span><span class="pill">proto '+s.proto+'</span>'+
    '<span class="pill">игроков '+s.players+' (онлайн '+s.online+')</span><span class="pill">матчей '+s.matchesLive+'</span>'+(s.draining?'<span class="drain">ДРЕНАЖ с '+new Date(s.drainSince).toLocaleTimeString()+'</span>':'');
  const tbl=(id,head,rows)=>{document.getElementById(id).innerHTML='<tr>'+head.map(h=>'<th>'+h+'</th>').join('')+'</tr>'+(rows||[]).map(r=>'<tr>'+r.map(c=>'<td>'+c+'</td>').join('')+'</tr>').join('');};
  tbl('matches',['id','комната','режим','арена','тик','людей','онлайн','начат'],(s.matches||[]).map(m=>[m.id,m.roomCode||'—',m.mode+'×'+m.mode,m.arena,m.tick,m.humans,m.online,new Date(m.created).toLocaleTimeString()]));
  tbl('rooms',['код','режим','арена','участников','в матче'],(s.rooms||[]).map(r=>[r.code,r.mode+'×'+r.mode,r.arena,r.members,r.inMatch?'да':'нет']));
  tbl('queues',['режим','игроков','до добора ботами'],(s.queues||[]).map(q=>[q.mode+'×'+q.mode,q.players,Math.ceil(q.waitLeftMs/1000)+' с']));
}
async function drain(on){ await fetch('/admin/drain?token='+encodeURIComponent(token),{method:on?'POST':'DELETE'}); load(); }
load(); setInterval(load, 2000);
</script></body></html>`
