// Package snowbrawl — корень модуля. Здесь живёт встроенная статика клиента,
// потому что go:embed не умеет подниматься выше каталога пакета.
package snowbrawl

import "embed"

// Web — клиент игры (index.html, client/, sim/). Отдаётся сервером напрямую
// или подменяется каталогом с диска флагом --web-dir в разработке.
//
//go:embed web
var Web embed.FS

// SimPath — путь к общему модулю симуляции внутри Web.
const SimPath = "web/sim/sim.js"
