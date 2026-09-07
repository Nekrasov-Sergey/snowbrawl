// Package sim исполняет общий JS-модуль симуляции (web/sim/sim.js) на сервере
// через goja. Граница между Go и JS — JSON-строки: это дёшево в goja и не требует
// ручного маппинга структур.
//
// goja не потокобезопасен: один Match — одна goja.Runtime — одна горутина матча.
package sim

import (
	"encoding/json"

	"github.com/dop251/goja"
	"github.com/pkg/errors"
)

// Program — скомпилированный sim.js, разделяемый всеми матчами (компиляция один раз).
type Program struct {
	prog      *goja.Program
	version   string
	arenas    int
	roles     []string
	gameModes []string
}

// Compile компилирует исходник sim.js и проверяет, что модуль экспортирует нужный контракт.
func Compile(src []byte) (*Program, error) {
	prog, err := goja.Compile("sim.js", string(src), true)
	if err != nil {
		return nil, errors.Wrap(err, "compile sim.js")
	}
	p := &Program{prog: prog}
	// Пробный запуск: читаем версию и справочники.
	vm, err := p.newVM()
	if err != nil {
		return nil, err
	}
	simObj := vm.Get("SnowBrawlSim")
	if simObj == nil || goja.IsUndefined(simObj) {
		return nil, errors.New("sim.js: global SnowBrawlSim is not defined")
	}
	obj := simObj.ToObject(vm)
	for _, fn := range []string{"createMatch", "applyInput", "step", "snapshot", "setBot", "reason"} {
		if _, ok := goja.AssertFunction(obj.Get(fn)); !ok {
			return nil, errors.Errorf("sim.js: export %q is not a function", fn)
		}
	}
	p.version = obj.Get("SIM_VERSION").String()
	if arenas := obj.Get("ARENAS"); arenas != nil {
		p.arenas = int(arenas.ToObject(vm).Get("length").ToInteger())
	}
	if roles := obj.Get("ALL_ROLES"); roles != nil {
		var list []string
		if err := vm.ExportTo(roles, &list); err != nil {
			return nil, errors.Wrap(err, "sim.js: ALL_ROLES")
		}
		p.roles = list
	}
	if gm := obj.Get("GAME_MODES"); gm != nil && !goja.IsUndefined(gm) {
		var list []string
		if err := vm.ExportTo(gm, &list); err != nil {
			return nil, errors.Wrap(err, "sim.js: GAME_MODES")
		}
		p.gameModes = list
	}
	if p.arenas == 0 || len(p.roles) == 0 || len(p.gameModes) == 0 {
		return nil, errors.New("sim.js: ARENAS, ALL_ROLES or GAME_MODES is empty")
	}
	return p, nil
}

// Version возвращает SIM_VERSION модуля.
func (p *Program) Version() string { return p.version }

// ArenaCount возвращает число арен.
func (p *Program) ArenaCount() int { return p.arenas }

// Roles возвращает список ролей (бойцов).
func (p *Program) Roles() []string { return append([]string(nil), p.roles...) }

// HasRole проверяет, что роль известна модулю.
func (p *Program) HasRole(role string) bool {
	for _, r := range p.roles {
		if r == role {
			return true
		}
	}
	return false
}

// GameModes возвращает список режимов игры (pvp, survival, defense).
func (p *Program) GameModes() []string { return append([]string(nil), p.gameModes...) }

// HasGameMode проверяет, что режим известен модулю.
func (p *Program) HasGameMode(mode string) bool {
	for _, m := range p.gameModes {
		if m == mode {
			return true
		}
	}
	return false
}

func (p *Program) newVM() (*goja.Runtime, error) {
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	if _, err := vm.RunProgram(p.prog); err != nil {
		return nil, errors.Wrap(err, "run sim.js")
	}
	return vm, nil
}

// PlayerConfig — участник матча в конфигурации sim.js.
type PlayerConfig struct {
	ID       string `json:"id"`
	Team     string `json:"team"`
	Role     string `json:"role"`
	Bot      bool   `json:"bot"`
	Nick     string `json:"nick,omitempty"`
	BotLevel *int   `json:"botLevel,omitempty"`
}

// PveConfig — урезание PvE-кампании (только для тестов).
type PveConfig struct {
	Levels int `json:"levels,omitempty"`
	Waves  int `json:"waves,omitempty"`
}

// MatchConfig — конфигурация матча для createMatch.
type MatchConfig struct {
	GameMode   string     `json:"gameMode,omitempty"`
	Mode       int        `json:"mode"`
	ArenaIndex int        `json:"arenaIndex"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Difficulty int        `json:"difficulty,omitempty"`
	Campaign   *bool      `json:"campaign,omitempty"`
	Pve        *PveConfig `json:"pve,omitempty"`
	// Tutorial — режим обучения: состав может быть неполным, матч не заканчивается,
	// боец-человек не выбывает. Сервер такие матчи не создаёт (обучение идёт в браузере),
	// поле нужно тестам контракта.
	Tutorial bool           `json:"tutorial,omitempty"`
	Players  []PlayerConfig `json:"players"`
}

// Match — живой матч внутри собственной goja.Runtime. Не потокобезопасен.
type Match struct {
	vm         *goja.Runtime
	state      goja.Value
	applyInput goja.Callable
	step       goja.Callable
	snapshot   goja.Callable
	setBot     goja.Callable
	isOver     goja.Callable
	winner     goja.Callable
	reason     goja.Callable
	stringify  goja.Callable
	parse      goja.Callable

	tutorialSpawn  goja.Callable
	tutorialRemove goja.Callable
}

// NewMatch создаёт матч: новая VM, вызов createMatch(config, seed).
func (p *Program) NewMatch(cfg MatchConfig, seed uint32) (*Match, error) {
	vm, err := p.newVM()
	if err != nil {
		return nil, err
	}
	simObj := vm.Get("SnowBrawlSim").ToObject(vm)
	m := &Match{vm: vm}
	get := func(name string) (goja.Callable, error) {
		fn, ok := goja.AssertFunction(simObj.Get(name))
		if !ok {
			return nil, errors.Errorf("sim.js: %s is not a function", name)
		}
		return fn, nil
	}
	createMatch, err := get("createMatch")
	if err != nil {
		return nil, err
	}
	for name, dst := range map[string]*goja.Callable{
		"applyInput": &m.applyInput, "step": &m.step, "snapshot": &m.snapshot,
		"setBot": &m.setBot, "isOver": &m.isOver, "winner": &m.winner, "reason": &m.reason,
		"tutorialSpawn": &m.tutorialSpawn, "tutorialRemove": &m.tutorialRemove,
	} {
		fn, err := get(name)
		if err != nil {
			return nil, err
		}
		*dst = fn
	}
	jsonObj := vm.Get("JSON").ToObject(vm)
	m.stringify, _ = goja.AssertFunction(jsonObj.Get("stringify"))
	m.parse, _ = goja.AssertFunction(jsonObj.Get("parse"))

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "marshal match config")
	}
	cfgVal, err := m.parse(goja.Undefined(), vm.ToValue(string(cfgJSON)))
	if err != nil {
		return nil, errors.Wrap(err, "parse match config")
	}
	state, err := createMatch(goja.Undefined(), cfgVal, vm.ToValue(int64(seed)))
	if err != nil {
		return nil, wrapJS(err, "createMatch")
	}
	m.state = state
	return m, nil
}

// ApplyInput передаёт ввод игрока: input — JSON-объект {kind, x, y, power?}.
func (m *Match) ApplyInput(playerID string, input json.RawMessage) (bool, error) {
	val, err := m.parse(goja.Undefined(), m.vm.ToValue(string(input)))
	if err != nil {
		return false, wrapJS(err, "parse input")
	}
	res, err := m.applyInput(goja.Undefined(), m.state, m.vm.ToValue(playerID), val)
	if err != nil {
		return false, wrapJS(err, "applyInput")
	}
	return res.ToBoolean(), nil
}

// SetBot переключает бойца на ИИ и обратно.
func (m *Match) SetBot(playerID string, bot bool) error {
	if _, err := m.setBot(goja.Undefined(), m.state, m.vm.ToValue(playerID), m.vm.ToValue(bot)); err != nil {
		return wrapJS(err, "setBot")
	}
	return nil
}

// TutorialSpawnOpts — соперник в обучении.
type TutorialSpawnOpts struct {
	Role     string  `json:"role"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	BotLevel int     `json:"botLevel"`
	Bot      bool    `json:"bot"`
}

// TutorialSpawn ставит соперника в матче обучения и возвращает его id.
func (m *Match) TutorialSpawn(opts TutorialSpawnOpts) (string, error) {
	b, err := json.Marshal(opts)
	if err != nil {
		return "", errors.Wrap(err, "marshal tutorial opts")
	}
	val, err := m.parse(goja.Undefined(), m.vm.ToValue(string(b)))
	if err != nil {
		return "", wrapJS(err, "parse tutorial opts")
	}
	res, err := m.tutorialSpawn(goja.Undefined(), m.state, val)
	if err != nil {
		return "", wrapJS(err, "tutorialSpawn")
	}
	if goja.IsNull(res) || goja.IsUndefined(res) {
		return "", errors.New("tutorialSpawn: not a tutorial match")
	}
	return res.String(), nil
}

// TutorialRemove убирает соперника обучения.
func (m *Match) TutorialRemove(id string) error {
	if _, err := m.tutorialRemove(goja.Undefined(), m.state, m.vm.ToValue(id)); err != nil {
		return wrapJS(err, "tutorialRemove")
	}
	return nil
}

// Step продвигает симуляцию на dt секунд и возвращает события шага как JSON-массив.
func (m *Match) Step(dt float64) (json.RawMessage, error) {
	events, err := m.step(goja.Undefined(), m.state, m.vm.ToValue(dt))
	if err != nil {
		return nil, wrapJS(err, "step")
	}
	s, err := m.stringify(goja.Undefined(), events)
	if err != nil {
		return nil, wrapJS(err, "stringify events")
	}
	return json.RawMessage(s.String()), nil
}

// Snapshot возвращает снапшот состояния как JSON-объект.
func (m *Match) Snapshot() (json.RawMessage, error) {
	snap, err := m.snapshot(goja.Undefined(), m.state)
	if err != nil {
		return nil, wrapJS(err, "snapshot")
	}
	s, err := m.stringify(goja.Undefined(), snap)
	if err != nil {
		return nil, wrapJS(err, "stringify snapshot")
	}
	return json.RawMessage(s.String()), nil
}

// IsOver сообщает, закончен ли матч.
func (m *Match) IsOver() bool {
	v, err := m.isOver(goja.Undefined(), m.state)
	return err == nil && v.ToBoolean()
}

// Winner возвращает "A", "B" или "" (ничья / не закончен).
func (m *Match) Winner() string {
	v, err := m.winner(goja.Undefined(), m.state)
	if err != nil || v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return ""
	}
	return v.String()
}

// Reason возвращает причину завершения из симуляции:
// "" | "ko" | "timeout" (PvP) | "cleared" | "wiped" | "objective" | "expired" (PvE).
func (m *Match) Reason() string {
	if m.reason == nil {
		return ""
	}
	v, err := m.reason(goja.Undefined(), m.state)
	if err != nil || v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return ""
	}
	return v.String()
}

func wrapJS(err error, op string) error {
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return errors.Wrapf(err, "%s: %s", op, exc.Value().String())
	}
	return errors.Wrap(err, op)
}
