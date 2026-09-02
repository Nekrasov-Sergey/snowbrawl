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
	prog    *goja.Program
	version string
	arenas  int
	roles   []string
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
	for _, fn := range []string{"createMatch", "applyInput", "step", "snapshot", "setBot"} {
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
	if p.arenas == 0 || len(p.roles) == 0 {
		return nil, errors.New("sim.js: ARENAS or ALL_ROLES is empty")
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
	ID   string `json:"id"`
	Team string `json:"team"`
	Role string `json:"role"`
	Bot  bool   `json:"bot"`
	Nick string `json:"nick,omitempty"`
}

// MatchConfig — конфигурация матча для createMatch.
type MatchConfig struct {
	Mode       int            `json:"mode"`
	ArenaIndex int            `json:"arenaIndex"`
	DurationMs int64          `json:"durationMs,omitempty"`
	Players    []PlayerConfig `json:"players"`
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
	stringify  goja.Callable
	parse      goja.Callable
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
		"setBot": &m.setBot, "isOver": &m.isOver, "winner": &m.winner,
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

func wrapJS(err error, op string) error {
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return errors.Wrapf(err, "%s: %s", op, exc.Value().String())
	}
	return errors.Wrap(err, op)
}
