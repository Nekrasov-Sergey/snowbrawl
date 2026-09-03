// Package web отдаёт клиент игры: встроенную статику или каталог с диска (--web-dir).
package web

import (
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	snowbrawl "github.com/Nekrasov-Sergey/snowbrawl"
)

// FS возвращает файловую систему клиента и путь к sim.js внутри неё.
func FS(webDir string) (fs.FS, error) {
	if webDir == "" {
		sub, err := fs.Sub(snowbrawl.Web, "web")
		if err != nil {
			return nil, errors.Wrap(err, "embedded web")
		}
		return sub, nil
	}
	st, err := os.Stat(webDir)
	if err != nil || !st.IsDir() {
		return nil, errors.Errorf("web dir %q is not a directory", webDir)
	}
	return os.DirFS(webDir), nil
}

// ReadSim читает исходник sim.js из файловой системы клиента.
func ReadSim(fsys fs.FS) ([]byte, error) {
	b, err := fs.ReadFile(fsys, "sim/sim.js")
	if err != nil {
		return nil, errors.Wrap(err, "read sim/sim.js")
	}
	return b, nil
}

// Register вешает отдачу статики на роутер. index.html не кэшируется и отдаётся
// с подставленной версией сборки (__BUILD__), остальные файлы кэшируются на час —
// клиент подставляет ?v=<build> к их URL, поэтому после деплоя кэш инвалидируется сам.
// Исключение — сборка "dev" (без git-тега) и отдача с диска: там ?v=dev не меняется
// между правками, поэтому статику не кэшируем, иначе F5 не подхватывает изменения.
func Register(r *gin.Engine, fsys fs.FS, fromDisk bool, build string) {
	noCacheStatic := fromDisk || build == "dev"
	fileServer := http.FileServer(http.FS(fsys))
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/ws") || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/admin") {
			c.Status(http.StatusNotFound)
			return
		}
		if p == "/" || p == "/index.html" {
			c.Header("Cache-Control", "no-store")
			// В разработке читаем с диска при каждом запросе, чтобы работал F5.
			b, err := fs.ReadFile(fsys, "index.html")
			if err != nil {
				c.String(http.StatusNotFound, "index.html not found")
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(strings.ReplaceAll(string(b), "__BUILD__", build)))
			return
		}
		if noCacheStatic {
			c.Header("Cache-Control", "no-store")
		} else {
			c.Header("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
