package modules

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

// moduleNameRe is intentionally looser than commandNameRe — it allows hyphen
// because a module name is only a catalog key and a collection name, never a
// Telegram command. It must stay identical to storage's collectionNameRe: the
// providers re-validate the name and hand back an always-failing store for
// anything outside that alphabet.
//
// Telegram command names still need the stricter [a-z0-9_]{1,32} alphabet
// (commandNameRe in validate.go).
var moduleNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Registry holds the resolved set of modules selected by the MODULES env var.
// It is built once at startup; Build fails fast on validation or conflict.
//
// Read-only after Build returns. Callers must not mutate any field —
// dispatchers and handlers capture *Registry by pointer and assume the maps
// are stable. A future hot-reload feature would need an explicit mutation API.
type Registry struct {
	Modules      []Module           // in MODULES-env order
	AllCommands  map[string]Command // name → Command, deduped across modules
	publicCmds   map[string]Command
	protected    map[string]Command
	private      map[string]Command
	crons        map[string]Cron // name → Cron, unique across modules
	cronDeps     map[string]Deps // cron name → owning module's Deps
	callbacks    map[string]Callback
	commandHooks []func(ctx context.Context, name string, update *models.Update)
	fallback     *CommandFallback // at most one; owner tracked in Build
	inline       *InlineQuery     // at most one; owner tracked in Build
	botUsername  string           // without "@"; empty when startup could not learn it
}

// Fallback returns the single command fallback, or nil when no module declares
// one.
func (r *Registry) Fallback() *CommandFallback { return r.fallback }

// Inline returns the single inline-query handler, or nil when no module
// declares one.
func (r *Registry) Inline() *InlineQuery { return r.inline }

// addCommands validates and indexes one module's commands.
//
// Mirrors addCallbacks: the per-item validation, the cross-module uniqueness
// check, and the fan-out into the visibility indexes all belong to the item,
// not to Build's module loop.
func (r *Registry) addCommands(name string, cmds []Command, owners map[string]string) error {
	for _, cmd := range cmds {
		if err := validateCommand(cmd); err != nil {
			return fmt.Errorf("module %q: %w", name, err)
		}
		if prev, dup := owners[cmd.Name]; dup {
			return fmt.Errorf("command conflict: /%s defined in %q and %q", cmd.Name, prev, name)
		}
		owners[cmd.Name] = name
		r.AllCommands[cmd.Name] = cmd
		switch cmd.Visibility {
		case VisibilityPublic:
			r.publicCmds[cmd.Name] = cmd
		case VisibilityProtected:
			r.protected[cmd.Name] = cmd
		case VisibilityPrivate:
			r.private[cmd.Name] = cmd
		}
	}
	return nil
}

// addSingletons claims the at-most-one Fallback and Inline slots.
//
// Two modules answering the same un-registered command, or the same inline
// query, is a configuration bug worth catching here rather than leaving the
// winner to map iteration order. A declared slot with a nil handler is rejected
// for the same reason: it would panic at dispatch instead of at startup.
//
// Extracted from Build rather than inlined so Build stays under the project's
// cyclomatic cap; the owner strings are threaded through because they are
// Build's loop state, not registry state.
func (r *Registry) addSingletons(name string, mod Module, fallbackOwner, inlineOwner *string) error {
	if mod.Fallback != nil {
		if *fallbackOwner != "" {
			return fmt.Errorf("fallback conflict: defined in %q and %q", *fallbackOwner, name)
		}
		if mod.Fallback.Handler == nil {
			return fmt.Errorf("module %q: fallback has no handler", name)
		}
		*fallbackOwner = name
		r.fallback = mod.Fallback
	}
	if mod.Inline != nil {
		if *inlineOwner != "" {
			return fmt.Errorf("inline conflict: defined in %q and %q", *inlineOwner, name)
		}
		if mod.Inline.Handler == nil {
			return fmt.Errorf("module %q: inline has no handler", name)
		}
		*inlineOwner = name
		r.inline = mod.Inline
	}
	return nil
}

// PublicCommands returns commands tagged VisibilityPublic, sorted by name.
func (r *Registry) PublicCommands() []Command { return sortedCommands(r.publicCmds) }

// ProtectedCommands returns commands tagged VisibilityProtected, sorted by name.
func (r *Registry) ProtectedCommands() []Command { return sortedCommands(r.protected) }

// PrivateCommands returns commands tagged VisibilityPrivate, sorted by name.
func (r *Registry) PrivateCommands() []Command { return sortedCommands(r.private) }

// Cron looks up a cron by global name across all loaded modules.
func (r *Registry) Cron(name string) (Cron, bool) {
	c, ok := r.crons[name]
	return c, ok
}

// CronDeps returns the Deps the cron's owning module received from Build.
// The cron dispatcher passes them to the handler.
func (r *Registry) CronDeps(name string) (Deps, bool) {
	d, ok := r.cronDeps[name]
	return d, ok
}

// RunCommandHooks calls every CommandHook registered by loaded modules in
// order. Errors are not returned — hooks are best-effort (e.g., stats
// counters) and must not fail the command handler. update may be nil (e.g.
// when invoked directly from tests); hooks must tolerate that.
func (r *Registry) RunCommandHooks(ctx context.Context, name string, update *models.Update) {
	for _, h := range r.commandHooks {
		h(ctx, name, update)
	}
}

// Crons returns all loaded crons, sorted by name. Allocates a fresh slice on
// every call — fine for startup-time logging, not for hot paths.
func (r *Registry) Crons() []Cron {
	out := make([]Cron, 0, len(r.crons))
	for _, c := range r.crons {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuildOptions bundles the optional dependencies threaded into every Module
// Factory's Deps. Adding new optional deps here keeps Build's signature
// stable as the dep list grows.
type BuildOptions struct {
	Bot         *bot.Bot
	BotUsername string
}

// Build constructs a Registry from the requested module names. The Provider
// supplies a per-module-isolated Collection (one MongoDB collection per module;
// MemoryProvider keeps one map per module). Build validates every command/cron
// and aborts on duplicate command names across the union of all visibilities.
//
// Names not present in factories are reported as a single error so a typo in
// MODULES does not silently load a smaller bot than intended. Duplicate names
// in MODULES are also a hard error to keep startup deterministic.
func Build(enabled []string, factories map[string]Factory, provider storage.Provider, opts BuildOptions) (*Registry, error) {
	if provider == nil {
		return nil, fmt.Errorf("modules: storage Provider is required")
	}

	// Empty/unset MODULES means "load every registered module" — the documented
	// contract (.env.example, compose.yml, deploy docs). Expand to the
	// full catalog in sorted order so the load order (and thus CommandHook
	// registration order) is deterministic across restarts.
	if len(enabled) == 0 {
		enabled = make([]string, 0, len(factories))
		for name := range factories {
			enabled = append(enabled, name)
		}
		sort.Strings(enabled)
	}

	reg := &Registry{
		AllCommands: map[string]Command{},
		publicCmds:  map[string]Command{},
		protected:   map[string]Command{},
		private:     map[string]Command{},
		crons:       map[string]Cron{},
		cronDeps:    map[string]Deps{},
		callbacks:   map[string]Callback{},
		botUsername: opts.BotUsername,
	}

	owners := map[string]string{} // command name → module that registered it
	cronOwners := map[string]string{}
	callbackOwners := map[string]string{}
	var fallbackOwner, inlineOwner string
	seenModule := map[string]bool{}
	var unknown []string

	for _, name := range enabled {
		if !moduleNameRe.MatchString(name) {
			return nil, fmt.Errorf("modules: invalid name %q in MODULES env (must match %s)", name, moduleNameRe)
		}
		if seenModule[name] {
			return nil, fmt.Errorf("modules: duplicate name %q in MODULES env", name)
		}
		seenModule[name] = true

		factory, ok := factories[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}

		moduleDeps := Deps{
			Store:       provider.Collection(name),
			Registry:    reg,
			Bot:         opts.Bot,
			BotUsername: opts.BotUsername,
		}
		mod := factory(moduleDeps)
		// A factory that hardcodes its own Name is a bug: the registry key is
		// the source of truth and a mismatch means the catalog and module
		// disagree about identity. Surface the conflict rather than silently
		// overwriting it.
		if mod.Name != "" && mod.Name != name {
			return nil, fmt.Errorf("module %q: factory returned mismatched Name=%q", name, mod.Name)
		}
		mod.Name = name
		if mod.CommandHook != nil {
			reg.commandHooks = append(reg.commandHooks, mod.CommandHook)
		}

		if err := reg.addCommands(name, mod.Commands, owners); err != nil {
			return nil, err
		}

		for _, cron := range mod.Crons {
			if err := validateCron(cron); err != nil {
				return nil, fmt.Errorf("module %q: %w", name, err)
			}
			if prev, dup := cronOwners[cron.Name]; dup {
				return nil, fmt.Errorf("cron conflict: %q defined in %q and %q", cron.Name, prev, name)
			}
			cronOwners[cron.Name] = name
			reg.crons[cron.Name] = cron
			reg.cronDeps[cron.Name] = moduleDeps
		}

		if err := reg.addCallbacks(name, mod.Callbacks, callbackOwners); err != nil {
			return nil, err
		}

		if err := reg.addSingletons(name, mod, &fallbackOwner, &inlineOwner); err != nil {
			return nil, err
		}

		reg.Modules = append(reg.Modules, mod)
	}

	if len(unknown) > 0 {
		return nil, fmt.Errorf("modules: unknown name(s) in MODULES env: %v", unknown)
	}

	return reg, nil
}

func (r *Registry) addCallbacks(module string, callbacks []Callback, owners map[string]string) error {
	for _, callback := range callbacks {
		if err := validateCallback(callback); err != nil {
			return fmt.Errorf("module %q: %w", module, err)
		}
		for prefix, owner := range owners {
			if strings.HasPrefix(prefix, callback.Prefix) || strings.HasPrefix(callback.Prefix, prefix) {
				return fmt.Errorf("callback prefix conflict: %q in %q overlaps %q in %q", callback.Prefix, module, prefix, owner)
			}
		}
		owners[callback.Prefix] = module
		r.callbacks[callback.Prefix] = callback
	}
	return nil
}

func sortedCommands(m map[string]Command) []Command {
	out := make([]Command, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
