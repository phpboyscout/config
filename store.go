package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/pflag"
)

var (
	// ErrNoSources is returned when a Store is built with nothing to read.
	ErrNoSources = errors.New("config: no sources configured")

	// ErrInvalidConfig is returned when a candidate configuration fails schema
	// validation. The previous configuration, if any, is retained.
	ErrInvalidConfig = errors.New("config: configuration is not valid")
)

// Store is the sole owner of configuration I/O.
//
// Nothing else reads, writes or watches a configuration source. That single
// rule is what removes the need for coordination between components: there is
// no protocol between a reader and a writer because there is only one of each,
// and it is this.
//
// Access is serialised internally. Concurrent loads and writes cannot
// interleave, so a reader never observes a partially applied change, and two
// writers cannot both decide to write and then overwrite one another.
type Store struct {
	// mu serialises everything that mutates state: loading, and later,
	// applying changes. It is deliberately a plain mutex rather than a
	// read-write lock — reads do not take it at all, because they go through
	// the snapshot pointer instead.
	mu sync.Mutex

	backends []Backend
	// requireFirst makes the first backend mandatory. A base file that has
	// gone missing is a broken installation; a missing overlay is normal.
	requireFirst bool

	// loaded records which backend produced which layers, in precedence
	// order. The snapshot alone cannot answer that: a backend's layers may be
	// named after something other than the backend itself, and a backend that
	// contributed nothing has no layer to be found by. Both matter — the first
	// for rebuilding after a write, the second for routing a new key at a file
	// that does not exist yet.
	loaded []backendLayers

	// current is read without the lock. Readers load the pointer and work
	// against an immutable snapshot, so a reload swapping it cannot disturb a
	// read already in progress.
	current atomic.Pointer[Snapshot]
	version atomic.Uint64

	notifier notifier

	// schema, when set, is checked against every candidate before it is
	// published. A configuration that does not satisfy it never becomes live.
	schema *Schema
}

// backendLayers pairs a backend with what it contributed.
type backendLayers struct {
	backend Backend
	layers  []Layer
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithBackend appends a backend. Backends are read in the order they are
// added, and later ones take precedence.
func WithBackend(b Backend) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, b)
	}
}

// WithFiles appends a file backend per path, in precedence order — the first
// is the base and the last wins.
func WithFiles(filesystem FS, paths ...string) StoreOption {
	return func(s *Store) {
		for _, p := range paths {
			s.backends = append(s.backends, NewFileBackend(filesystem, p))
		}
	}
}

// WithReaders appends in-memory sources, in precedence order.
//
// Compiled-in defaults belong here: they contribute a layer like any other, so
// they take part in precedence and provenance rather than being a special
// case that has to be remembered.
func WithReaders(sources ...NamedSource) StoreOption {
	return func(s *Store) {
		for _, src := range sources {
			s.backends = append(s.backends, NewReaderBackend(src.Name, src.Content))
		}
	}
}

// NamedSource is in-memory configuration with an identity for provenance.
type NamedSource struct {
	Name    string
	Content []byte
}

// WithEnv appends an environment backend reading variables under a prefix.
//
// Add it after the file sources: environment variables are expected to
// override what is on disk, and precedence follows the order backends are
// added.
func WithEnv(prefix string, opts ...EnvOption) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, NewEnvBackend(prefix, opts...))
	}
}

// WithFlags appends a backend contributing the flags the user actually
// changed. Add it last: an explicit flag is the most deliberate input there
// is.
func WithFlags(flags *pflag.FlagSet, opts ...FlagOption) StoreOption {
	return func(s *Store) {
		s.backends = append(s.backends, NewFlagBackend(flags, opts...))
	}
}

// WithSchema validates every candidate configuration before it is published.
//
// Validation is fail-closed and applies to the resolved configuration rather
// than to any single layer, because a layer can be legitimately incomplete on
// its own — a base file may omit a key that an overlay supplies, and judging
// it alone would reject a perfectly valid setup.
//
// A reload that fails validation leaves the previous configuration in place:
// running on the last known good values is better than running on values the
// application has said it cannot use.
func WithSchema(schema *Schema) StoreOption {
	return func(s *Store) {
		s.schema = schema
	}
}

// RequireFirstSource makes the first backend mandatory: if it is missing, the
// Store fails to load rather than starting up with a hole in its
// configuration.
func RequireFirstSource() StoreOption {
	return func(s *Store) {
		s.requireFirst = true
	}
}

// NewStore builds a Store and performs its first load.
//
// Construction and loading are deliberately not separable. A Store that exists
// but has not loaded is a state every caller would have to handle and most
// would forget to.
//
// A configuration that loads and parses but violates the schema is returned
// *alongside* ErrInvalidConfig rather than discarded. The usual
// `if err != nil { return }` still fails fast, so a service refuses to start on
// a bad config exactly as before. But a tool whose job is to repair
// configuration needs a Store to repair it through, and returning nil would
// make one missing key unfixable by the surface designed to fix it — see D15.
// Such a caller checks for ErrInvalidConfig specifically and proceeds:
//
//	s, err := config.NewStore(ctx, opts...)
//	if err != nil && !errors.Is(err, config.ErrInvalidConfig) {
//	    return err // genuinely unusable: no sources, unreadable, unparseable
//	}
//
// Every other failure — no sources, a source that cannot be read, one that
// cannot be parsed — still returns nil, because there is no configuration to
// hand back.
func NewStore(ctx context.Context, opts ...StoreOption) (*Store, error) {
	s := &Store{}

	for _, opt := range opts {
		opt(s)
	}

	if len(s.backends) == 0 {
		return nil, ErrNoSources
	}

	if err := s.Reload(ctx); err != nil {
		if errors.Is(err, ErrInvalidConfig) {
			return s, err
		}

		return nil, err
	}

	return s, nil
}

// AddLayer contributes configuration from an in-memory source at runtime, at
// the highest precedence.
//
// This is how a tool supplies configuration it computed rather than read: a
// resolved path, a value negotiated with a server, a toggle that applies to
// this run only. It is an ordinary layer, so precedence, provenance, merging
// and shadowing all work on it exactly as they do on a file, and nothing
// special has to be remembered about it.
//
// It is never a write target. An in-memory source has nowhere to persist to, so
// routing skips it — a later write lands in the file beneath and the added
// layer goes on winning, which the plan reports as a shadowed write rather than
// letting the caller believe their edit took effect.
//
// Compiled-in defaults belong in WithReaders at construction instead, where
// they sit at the bottom of the order rather than the top.
//
// The layer survives reloads: it is a source like any other, re-read each time.
// A layer that cannot be parsed is refused and not adopted, because leaving it
// in place would make every later reload fail on content the caller has no way
// to withdraw.
func (s *Store) AddLayer(ctx context.Context, name string, r io.Reader) error {
	if s.notifier.insideObserver() {
		return ErrWriteFromObserver
	}

	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: a layer needs a name to appear in provenance", ErrInvalidTarget)
	}

	content, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("config: reading layer %q: %w", name, err)
	}

	if err := s.adoptBackend(newOverrideBackend(name, content)); err != nil {
		return err
	}

	next, changed, err := s.reload(ctx)
	if err != nil {
		// The layer is withdrawn rather than left in place. Reload is
		// fail-closed, so the previous configuration still stands, and a
		// backend that cannot load would otherwise break every reload after
		// this one.
		s.withdrawBackend(name)

		return err
	}

	if !changed {
		return nil
	}

	s.notifier.notify(next)

	return nil
}

// adoptBackend appends a backend at the highest precedence.
func (s *Store) adoptBackend(b Backend) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.backends {
		if existing.ID() == b.ID() {
			return fmt.Errorf("%w: a source named %q is already loaded", ErrInvalidTarget, b.ID())
		}
	}

	s.backends = append(append([]Backend(nil), s.backends...), b)

	return nil
}

// withdrawBackend removes one backend by name.
//
// Reinstating the list captured before the layer was adopted would discard any
// layer another goroutine added in between — and that caller was told its
// AddLayer succeeded, so its configuration would silently read back as absent.
// Only the layer that failed is withdrawn.
func (s *Store) withdrawBackend(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Backend, 0, len(s.backends))

	for _, b := range s.backends {
		if b.ID() != name {
			out = append(out, b)
		}
	}

	s.backends = out
}

// Snapshot returns the current configuration.
//
// The returned snapshot is immutable and will not change underneath the
// caller, so a sequence of reads against one snapshot is coherent even if a
// reload lands midway.
func (s *Store) Snapshot() *Snapshot {
	return s.current.Load()
}

// Reload re-reads every backend and, on success, publishes a new snapshot.
//
// It is fail-closed: if any source fails to load or parse, the error is
// returned and the previous snapshot is retained. A configuration that is
// partly the old values and partly the new is worse than either, and worse
// than refusing.
func (s *Store) Reload(ctx context.Context) error {
	// Reloading from an observer cascades exactly as writing does: the reload
	// publishes, which notifies, which reloads again.
	if s.notifier.insideObserver() {
		return ErrWriteFromObserver
	}

	next, changed, err := s.reload(ctx)
	if err != nil {
		s.notifier.notifyError(err)

		return err
	}

	if !changed {
		return nil
	}

	// Notification happens outside the lock. An observer commonly reacts by
	// writing configuration back, and holding the lock across that call would
	// deadlock the Store against itself.
	s.notifier.notify(next)

	return nil
}

// reload swaps in a new configuration under the lock, reporting whether
// anything actually changed. The caller notifies, outside the lock.
func (s *Store) reload(ctx context.Context) (next *Snapshot, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	loaded, err := s.loadAll(ctx)
	if err != nil {
		// Fail-closed: the previous configuration stands, so observers are not
		// told of a change that did not happen. The rejection travels on the
		// error channel instead.
		return nil, false, err
	}

	if err := s.validate(loaded); err != nil {
		// A running Store keeps its last-known-good configuration: it has one,
		// and swapping in something known to be invalid would be strictly
		// worse than standing still.
		if s.current.Load() != nil {
			return nil, false, err
		}

		// The first load has no last-known-good to fall back on. Publishing the
		// invalid configuration anyway is what keeps it repairable — the
		// alternative is a Store with no snapshot, which cannot be read from or
		// written through, so the config could never be fixed through it (D15).
		// The error still travels, so a caller that wants to fail fast does.
		s.publish(loaded)

		return nil, false, err
	}

	previous := s.current.Load()
	next = s.publish(loaded)

	// A reload that produced identical configuration is not a change, and
	// telling observers otherwise would have them re-read values they already
	// hold. This also absorbs the noise filesystem notification produces:
	// permission changes, atomic renames and editors writing in several
	// passes all arrive as events and none of them alters configuration.
	return next, !sameConfiguration(previous, next), nil
}

// sameConfiguration reports whether two snapshots resolve identically.
func sameConfiguration(a, b *Snapshot) bool {
	if a == nil || b == nil {
		return false
	}

	return reflect.DeepEqual(a.values, b.values)
}

// validate checks a candidate against the schema without publishing it.
//
// Building a throwaway snapshot is deliberate: validation must see exactly
// what a caller would, including how the layers merge, rather than inspecting
// them individually and hoping the resolved result agrees.
func (s *Store) validate(loaded []backendLayers) error {
	errs := s.violations(loaded)
	if len(errs) == 0 {
		return nil
	}

	// Expressed through violations rather than beside it. The two used to build
	// their own candidate snapshot, so a change to how a candidate is assembled
	// had to be made twice — and the failure when it was not would be silent
	// divergence between what a reload rejects and what a write rejects, which
	// is precisely the drift validateChange promises cannot happen.
	result := &ValidationResult{Errors: errs}

	return fmt.Errorf("%w: %s", ErrInvalidConfig, result.Error())
}

// violations resolves a candidate and reports what the schema objects to.
//
// It builds a throwaway snapshot for the same reason validate does: a single
// layer may legitimately be invalid alone and valid once merged — a base
// omitting a key its overlay supplies — so only the resolved result can be
// judged.
func (s *Store) violations(loaded []backendLayers) []ValidationError {
	if s.schema == nil {
		return nil
	}

	flat := flatten(loaded)

	return validateSnapshot(newSnapshot(0, flat), s.schema).Errors
}

// validateChange refuses a write that introduces a schema violation, while
// allowing one that leaves an existing violation untouched.
//
// The asymmetry is the point. Validating the candidate outright would make an
// already-invalid configuration uneditable, so a single missing required key
// would lock the config against the very surface meant to repair it. Comparing
// before against after means a caller is answerable for what their change
// breaks and nothing else.
//
// The candidate is built by the same rebuild-and-merge path a reload uses, so
// validation here and validation after the commit cannot disagree. A separate
// path would drift, and the failure mode is nasty: a write that validates,
// lands, and is then rejected by the reload it triggers, leaving the file
// changed and the process on last-known-good.
func (s *Store) validateChange(before, after []backendLayers) error {
	if s.schema == nil {
		return nil
	}

	introduced, existing := diffViolations(s.violations(before), s.violations(after))
	if len(introduced) == 0 {
		return nil
	}

	var sb strings.Builder

	sb.WriteString("this change would make the configuration invalid:")

	for _, e := range introduced {
		sb.WriteString("\n  " + e.String())
	}

	// Pre-existing violations are reported but explicitly marked as not the
	// caller's doing. Without the distinction they would read as consequences
	// of the change, sending someone to debug an edit that was fine.
	if len(existing) > 0 {
		sb.WriteString("\n\nthe configuration was already invalid before this change, " +
			"and these are unaffected by it:")

		for _, e := range existing {
			sb.WriteString("\n  " + e.String())
		}
	}

	return fmt.Errorf("%w: %s", ErrInvalidConfig, sb.String())
}

// diffViolations splits the violations of a candidate into those the change
// introduced and those that were already there.
//
// Identity is key plus message: the same key can fail for different reasons,
// and a change that swaps one failure for another on the same key has
// introduced something the caller needs to hear about.
func diffViolations(before, after []ValidationError) (introduced, existing []ValidationError) {
	was := make(map[string]bool, len(before))
	for _, e := range before {
		was[e.Key+"\x00"+e.Message] = true
	}

	for _, e := range after {
		if was[e.Key+"\x00"+e.Message] {
			existing = append(existing, e)

			continue
		}

		introduced = append(introduced, e)
	}

	return introduced, existing
}

// loadAll reads every backend in order, building the layer list.
//
// The caller must hold the lock.
func (s *Store) loadAll(ctx context.Context) ([]backendLayers, error) {
	loaded := make([]backendLayers, 0, len(s.backends))

	for i, backend := range s.backends {
		got, err := backend.Load(ctx, flatten(loaded))
		if err == nil {
			loaded = append(loaded, backendLayers{backend: backend, layers: normalised(got)})

			continue
		}

		// A missing source is only fatal for the first backend, and only when
		// the caller asked for it to be. Everything else — a parse failure, an
		// unsafe document, a permissions problem — is fatal wherever it
		// happens, because silently dropping a layer changes the effective
		// configuration without telling anyone.
		if errors.Is(err, fs.ErrNotExist) {
			if i == 0 && s.requireFirst {
				return nil, fmt.Errorf("config: required source %s: %w", backend.ID(), err)
			}

			// A source that is not there contributes nothing but is still a
			// candidate for a write: creating it is how a new overlay comes
			// into being.
			loaded = append(loaded, backendLayers{backend: backend})

			continue
		}

		return nil, err
	}

	return loaded, nil
}

// AddObserver registers a component to be told when configuration changes.
func (s *Store) AddObserver(o Observable) { s.notifier.addObserver(o) }

// AddObserverFunc registers a function to be told when configuration changes.
func (s *Store) AddObserverFunc(f func(Observed) error) {
	s.notifier.addObserver(ObserverFunc(f))
}

// Observers returns the registered observers, for tests and diagnostics.
func (s *Store) Observers() []Observable {
	s.notifier.mu.Lock()
	defer s.notifier.mu.Unlock()

	return append([]Observable(nil), s.notifier.observers...)
}

// OnObserverError registers a callback for observers that failed to react.
//
// Separate from OnReloadError because the two mean different things: a
// rejected reload means the configuration did not change, whereas a failing
// observer means it did and one component could not keep up.
func (s *Store) OnObserverError(f func(error)) { s.notifier.addObserveErrorFunc(f) }

// OnReloadError registers a callback for reloads that were rejected.
//
// Rejections travel on their own channel because nothing changed: telling
// observers "configuration changed" when it did not would have them re-read
// values identical to the ones they hold.
func (s *Store) OnReloadError(f func(error)) { s.notifier.addErrorFunc(f) }

// publish installs a new snapshot. The caller must hold the lock.
func (s *Store) publish(loaded []backendLayers) *Snapshot {
	s.loaded = loaded

	flat := flatten(loaded)

	next := newSnapshot(s.version.Add(1), flat)
	s.current.Store(next)

	return next
}

// Sources returns the identity of every backend, in precedence order.
func (s *Store) Sources() []string {
	// Locked because AddLayer replaces the backend list wholesale, so reading
	// it unsynchronised races a write to the slice header itself.
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.backends))
	for _, b := range s.backends {
		out = append(out, b.ID())
	}

	return out
}

// Plan routes changes without writing anything.
//
// The result is exactly what Apply would execute, so a dry run cannot drift
// from the real thing the way a separate preview implementation would.
func (s *Store) Plan(changes ...Change) (*Plan, error) {
	// Snapshot taken under the same lock as the routing inputs. Releasing it
	// first let a concurrent write publish in between, so the plan could route
	// pre-write layers against a post-write snapshot and preview a target the
	// apply would not choose — the drift this method promises cannot happen.
	s.mu.Lock()
	order := s.sourceOrder()
	targets := writableOnly(order)
	snap := s.current.Load()
	s.mu.Unlock()

	return route(snap, targets, order, changes)
}

// Apply routes changes, writes them, and publishes the resulting snapshot.
//
// The snapshot is constructed from the content just written rather than by
// re-reading afterwards. That is what makes a write self-contained: there is
// no round trip through the filesystem, no waiting for a watcher, and no
// window in which the configuration in memory disagrees with the file on disk.
//
// Execution is prepare → verify → commit. Everything expensive or likely to
// fail happens while nothing is visible; commit is a sequence of renames.
func (s *Store) Apply(ctx context.Context, changes ...Change) (*Snapshot, error) {
	if s.notifier.insideObserver() {
		return nil, ErrWriteFromObserver
	}

	next, changed, err := s.apply(ctx, changes)
	if err != nil {
		return nil, err
	}

	// A write that resolved to the configuration already published is not a
	// change, and announcing it as one is how an observer that reacts by
	// writing gets stuck: its write lands, notifies, runs the observer, which
	// asks for the same write again, with nothing anywhere to say the value had
	// already settled. Reload has always filtered this; Apply had not, so the
	// two disagreed about what counts as a change.
	//
	// The file may still have been rewritten — comments reflowed, a key moved
	// into the layer that should own it — and that write is real. It just did
	// not alter the resolved configuration, which is what observers react to.
	if !changed {
		return next, nil
	}

	// Exactly one notification per logical change, however many sources it
	// touched — and delivered outside the lock, because an observer reacting
	// by writing configuration back is the ordinary case, not an exotic one.
	s.notifier.notify(next)

	return next, nil
}

func (s *Store) apply(ctx context.Context, changes []Change) (*Snapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.current.Load()

	sources := s.sourceOrder()

	plan, err := route(current, writableOnly(sources), sources, changes)
	if err != nil {
		return nil, false, err
	}

	pending, order, err := s.prepare(ctx, plan)
	if err != nil {
		return nil, false, err
	}

	// The candidate is what the Store will publish once this commits. Building
	// it now means the schema judges the same configuration a reader would see,
	// and it is judged while everything is still staged and abandonable.
	candidate, err := s.rebuild(ctx, pending)
	if err != nil {
		discardAll(ctx, pending)

		return nil, false, err
	}

	if err := s.validateChange(s.loaded, candidate); err != nil {
		discardAll(ctx, pending)

		return nil, false, err
	}

	if err := verifyAll(ctx, pending, order); err != nil {
		discardAll(ctx, pending)

		return nil, false, err
	}

	if err := commitAll(ctx, pending, order); err != nil {
		return nil, false, err
	}

	next := s.publish(candidate)

	// Whether the resolved configuration actually moved is decided here, on the
	// same comparison a reload uses, so the two cannot disagree about what
	// counts as a change.
	return next, !sameConfiguration(current, next), nil
}

// prepare groups a plan by backend and stages the edits for each.
//
// Grouping matters: a file is read, edited and written once per apply however
// many changes target it. Editing per change would reparse the same document
// repeatedly, and a settings screen saving fifty fields would pay for it.
func (s *Store) prepare(ctx context.Context, plan *Plan) (map[string]Pending, []string, error) {
	grouped := map[string][]Edit{}
	order := []string{}

	for _, op := range plan.Operations {
		id := op.Target.Name
		if _, seen := grouped[id]; !seen {
			order = append(order, id)
		}

		grouped[id] = append(grouped[id], Edit{
			Document: op.Target.Document,
			Path:     op.Change.Path,
			Value:    op.Change.Value,
			Remove:   op.Change.Remove,
		})
	}

	pending := make(map[string]Pending, len(grouped))

	for _, id := range order {
		backend, ok := s.backendByID(id)
		if !ok {
			discardAll(ctx, pending)

			// Reachable from a custom backend, not only from a bug here: write
			// routing finds a backend by matching a layer's Source.Name against
			// ID(), so a backend whose Load reports a different name than it
			// answers to cannot be found again. Saying "internal invariant" and
			// nothing else sent the one person who could fix it looking in the
			// wrong codebase.
			return nil, nil, fmt.Errorf(
				"%w: no backend answers to %q. A writable backend's ID() must equal "+
					"the Source.Name of the layers its Load returns",
				ErrInternal, id)
		}

		writable, ok := backend.(WritableBackend)
		if !ok {
			discardAll(ctx, pending)

			return nil, nil, fmt.Errorf("%w: %s", ErrNotWritable, id)
		}

		staged, err := writable.Prepare(ctx, grouped[id])
		if err != nil {
			discardAll(ctx, pending)

			return nil, nil, err
		}

		pending[id] = staged
	}

	return pending, order, nil
}

// verifyAll checks every source is unchanged before anything is committed.
func verifyAll(ctx context.Context, pending map[string]Pending, order []string) error {
	// Ordered so that a set with more than one stale target always reports the
	// same one, rather than whichever the map happened to yield first.
	for _, id := range order {
		if err := pending[id].Verify(ctx); err != nil {
			return err
		}
	}

	return nil
}

// commitAll commits every staged write, undoing those already committed if one
// fails.
//
// This is not cross-source transactionality — that needs a journal — but it
// narrows the window in which a partially applied set can be observed to the
// duration of the renames, and it fails in the safe direction: everything
// likely to go wrong has already happened during prepare.
func commitAll(ctx context.Context, pending map[string]Pending, order []string) error {
	committed := make([]Pending, 0, len(pending))

	// Ordered, not by map iteration. D14 commits sequentially so that a failure
	// leaves an "everything up to N succeeded" prefix; a random order forfeits
	// exactly that property, because which targets committed before the failure
	// would vary between runs and the error could not name a stable set.
	for _, id := range order {
		p := pending[id]
		if err := p.Commit(ctx); err != nil {
			rollbackErr := rollbackAll(ctx, committed)
			discardAll(ctx, pending)

			if rollbackErr != nil {
				// The caller must never be left guessing what is in which
				// state, so say so explicitly rather than returning the
				// original error alone.
				return fmt.Errorf("%w: %s failed (%w) and rolling back the rest failed: %w",
					ErrPartialCommit, id, err, rollbackErr)
			}

			return err
		}

		committed = append(committed, p)
	}

	return nil
}

func rollbackAll(ctx context.Context, committed []Pending) error {
	var firstErr error

	for _, p := range committed {
		if err := p.Rollback(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func discardAll(ctx context.Context, pending map[string]Pending) {
	for _, p := range pending {
		_ = p.Discard(ctx)
	}
}

// rebuild assembles the next layer set: written sources contribute what was
// just staged, and everything else is carried over untouched.
//
// Carrying layers over rather than re-reading them is deliberate. Re-reading
// would be slower, and it would open the possibility of picking up a foreign
// change halfway through an apply — turning one caller's write into a snapshot
// containing someone else's edits too.
//
// The pairing recorded at load is what makes this correct. Matching layers to
// backends by name would work for files and silently drop any backend whose
// layers are named after something else, such as an environment variable.
func (s *Store) rebuild(ctx context.Context, pending map[string]Pending) ([]backendLayers, error) {
	next := make([]backendLayers, 0, len(s.loaded))

	for _, bl := range s.loaded {
		if staged, ok := pending[bl.backend.ID()]; ok {
			next = append(next, backendLayers{backend: bl.backend, layers: normalised(staged.Layers())})

			continue
		}

		// Re-read rather than carried over. A backend whose reading of its own
		// input depends on the keys beneath it must see the keys this write
		// produced — the environment backend resolves a variable name against
		// the existing key space, so a write introducing a second key spelled
		// the same way makes that variable ambiguous.
		//
		// Carrying it over is what let a write validate, land, and then break
		// the reload it triggered, leaving the file changed and the process on
		// last-known-good: the outcome D15 exists to prevent.
		layers, err := bl.backend.Load(ctx, flatten(next))
		if err != nil {
			return nil, err
		}

		next = append(next, backendLayers{backend: bl.backend, layers: normalised(layers)})
	}

	return next, nil
}

func collectKeys(values map[string]any, prefix string, seen map[string]bool, out *[]string) {
	for k, v := range values {
		path := joinPath(prefix, k)

		nested, isMap := asStringMap(v)
		if isMap && len(nested) > 0 {
			collectKeys(nested, path, seen, out)

			continue
		}

		if !seen[path] {
			seen[path] = true

			*out = append(*out, path)
		}
	}
}

// writableOnly filters a precedence order down to the sources a write may be
// routed at, so the order is walked once rather than once per question.
func writableOnly(order []Source) []Source {
	var out []Source

	for _, src := range order {
		if src.Writable {
			out = append(out, src)
		}
	}

	return out
}

// sourceOrder returns every source in precedence order, including a synthesised
// entry for a writable backend that has not contributed a layer yet.
//
// Routing needs the order rather than only the layers, because a write can be
// aimed at a file that does not exist. Such a target matches nothing in the
// snapshot, so its position — and therefore what outranks it — cannot be
// recovered from the layers alone.
func (s *Store) sourceOrder() []Source {
	var out []Source

	for _, bl := range s.loaded {
		if len(bl.layers) > 0 {
			for _, l := range bl.layers {
				out = append(out, l.Source)
			}

			continue
		}

		if _, ok := bl.backend.(WritableBackend); ok {
			out = append(out, Source{
				Kind:     SourceFile,
				Name:     bl.backend.ID(),
				Writable: true,
			})
		}
	}

	return out
}

func (s *Store) backendByID(id string) (Backend, bool) {
	for _, b := range s.backends {
		if b.ID() == id {
			return b, true
		}
	}

	return nil, false
}

// Watch begins reacting to changes made outside this process.
//
// The Store's own writes never travel this path: Apply builds the next
// snapshot from what it just wrote and notifies directly, so a write cannot
// come back round through the watcher. That is what makes a
// write-then-react-then-write cascade unrepresentable rather than something to
// be detected and broken.
//
// Watching only covers file-backed sources. Environment variables and flags do
// not change under a running process, and an in-memory source is changed by
// the code that owns it.
//
// A watcher that cannot function fails here rather than silently doing
// nothing: an application that believes it will hear about changes and never
// does is worse off than one that knows it must restart.
func (s *Store) Watch(ctx context.Context, opts ...WatchOption) (stop func(), err error) {
	cfg := watchConfig{interval: DefaultPollInterval, settle: DefaultSettleInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	watchable := s.watchableBackends()
	if len(watchable) == 0 {
		return nil, fmt.Errorf("%w: no watchable sources", ErrWatchUnavailable)
	}

	// An injected watcher supplies precisely what the settle window exists to
	// compensate for — it says when to reload, rather than leaving the Store to
	// infer it from a burst of filesystem events. Defaulting it to no window
	// keeps WithWatcher deterministic, which is the whole reason to use one.
	if cfg.watcher != nil && !cfg.settleSet {
		cfg.settle = 0
	}

	// Foreign changes are coalesced; the Store's own writes are not, and never
	// reach here. Apply knows the extent of its batch and notifies directly, so
	// exactly-once for a write is by construction rather than by timing. This
	// window exists only because the filesystem reports that files changed and
	// not that they changed together.
	settle := newSettler(cfg.settle, func() {
		// A watch event means the sources *may* have changed. Reload decides
		// whether anything actually did, and only notifies observers if so.
		_ = s.Reload(ctx)
	})

	onChange := settle.trigger

	// An injected watcher stands in for the whole set, which is how a test
	// drives change detection without a real filesystem.
	if cfg.watcher != nil {
		stop, err := cfg.watcher.Watch(ctx, s.watchedPaths(), onChange)
		if err != nil {
			settle.stop()

			return nil, err
		}

		return func() { settle.stop(); stop() }, nil
	}

	var stops []func()

	for _, b := range watchable {
		if fb, ok := b.(*fileBackend); ok {
			// A watch that degrades and cannot fall back is a failure of the
			// hot-reload contract, so it travels on the channel already meant
			// for one.
			fb.onWatchError = s.notifier.notifyError
		}

		stop, err := b.Watch(ctx, cfg.interval, onChange)
		if err != nil {
			// One source that cannot be watched makes the set incomplete, and
			// reporting success would leave the caller believing it will hear
			// about sources it never will.
			stopAll(stops)
			settle.stop()

			return nil, err
		}

		stops = append(stops, stop)
	}

	return func() { settle.stop(); stopAll(stops) }, nil
}

// watchedPaths lists the paths of file-backed sources, for an injected watcher
// that expects them.
func (s *Store) watchedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []string

	for _, b := range s.backends {
		if fb, ok := b.(*fileBackend); ok {
			out = append(out, fb.path)
		}
	}

	return out
}

func stopAll(stops []func()) {
	for _, stop := range stops {
		stop()
	}
}

// WatchOption configures watching.
type WatchOption func(*watchConfig)

type watchConfig struct {
	interval time.Duration
	settle   time.Duration
	// settleSet records that the caller chose a window, so an injected watcher
	// can default to none without overriding an explicit choice.
	settleSet bool
	watcher   Watcher
}

// WithPollInterval sets how often a polling watcher checks for changes.
func WithPollInterval(d time.Duration) WatchOption {
	return func(c *watchConfig) { c.interval = d }
}

// WithSettleInterval sets how long a burst of foreign changes is allowed to
// settle before the Store reloads.
//
// A logical configuration change is not always one filesystem event: a deploy
// replacing two overlays produces two, and nothing in the filesystem says they
// were meant as one. Waiting for the burst to settle turns them into a single
// reload, and a single notification.
//
// Zero disables settling, reloading on each report. Tests driving an injected
// watcher usually want that, so the trigger and the reload stay in step
// without waiting on a timer.
//
// This does not make a multi-file change atomic. Writes spaced further apart
// than the window are still seen as separate changes — the guarantee belongs
// to whoever writes the files, by writing them atomically or by keeping
// settings that change together in one file.
func WithSettleInterval(d time.Duration) WatchOption {
	return func(c *watchConfig) { c.settle, c.settleSet = d, true }
}

// WithWatcher supplies a watcher, mainly so tests can drive change detection
// deterministically instead of waiting on a real filesystem.
func WithWatcher(w Watcher) WatchOption {
	return func(c *watchConfig) { c.watcher = w }
}

// watchableBackends returns the backends that can report their own changes.
//
// Asked by interface rather than by concrete type. Reaching into *fileBackend
// for its path and filesystem meant only a backend defined in this package
// could ever be watched, and a consumer supplying its own — the one thing
// WithBackend exists for — was silently omitted while Watch reported success.
func (s *Store) watchableBackends() []WatchableBackend {
	// Same reason as Sources: AddLayer reassigns s.backends under the lock.
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []WatchableBackend

	for _, b := range s.backends {
		if w, ok := b.(WatchableBackend); ok {
			out = append(out, w)
		}
	}

	return out
}

// flatten collects every layer in precedence order, discarding which backend
// produced it. The pairing matters when rebuilding after a write; merging only
// needs the order.
func flatten(loaded []backendLayers) []Layer {
	var flat []Layer
	for _, bl := range loaded {
		flat = append(flat, bl.layers...)
	}

	return flat
}
