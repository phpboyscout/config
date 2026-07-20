// Package config loads, merges and writes layered configuration, with a single
// component owning every read and write.
//
// A [Store] is that component. It reads each source, merges them in a
// deterministic precedence order, records which layer supplied each value, and
// is the only thing that writes any of them back. Everything else is a view
// over the snapshot it publishes, so there is no protocol between a reader and
// a writer — there is only one of each, and it is the Store.
//
//	s, err := config.NewStore(ctx,
//		config.WithFiles(filesystem, "/etc/app.yaml", "/home/me/.app.yaml"),
//		config.WithEnv("APP"),
//		config.WithFlags(flags))
//
// Sources are added lowest precedence first, and a later one wins. Files,
// compiled-in defaults ([WithReaders]), environment variables and command-line
// flags are all ordinary layers, so precedence, provenance and shadowing work
// the same way for each.
//
// # Reading
//
// [Store.View] returns a [View] over the current snapshot, satisfying [Reader].
// A snapshot is immutable, so a sequence of reads against one is coherent even
// if a reload lands midway; [Store.With] pins a single snapshot across a block
// of reads when several values must agree with each other.
//
// There is a named accessor for the common types — [View.GetString],
// [View.GetInt] and the rest — and [Value] for everything else: it reads any
// type at all, including a consumer's own, so [Reader] does not have to grow a
// method per type. Durations, IP addresses, URLs, timezones and anything
// implementing encoding.TextUnmarshaler decode from their ordinary written form.
//
// # Writing
//
// [Store.Apply] persists changes, and [Store.Plan] shows what it would do
// without doing it. A write is routed at the layer that already owns the key,
// so it lands where it will be read back, and comments, key order and block
// style in the target file survive it. A write that cannot change the effective
// value — because a higher layer still defines the key — reports that rather
// than appearing to succeed.
//
//	if _, err := s.Apply(ctx, config.Set("server.port", 9090)); err != nil {
//		return err
//	}
//
// # Provenance
//
// [View.Origin] reports which source supplied a value, [View.Shadowed] lists
// every layer defining it, and [View.Explain] renders the whole chain for a
// diagnostic. These answer "why is this value what it is", which a merge-eager
// library cannot, because merging discards the information before anyone can
// ask.
//
// # Reacting to change
//
// [Store.Watch] notices changes made by anything other than this process, and
// observers registered with [Store.AddObserver] are told exactly once per
// logical change. An observer must not write configuration while reacting to a
// change: doing so returns [ErrWriteFromObserver], because each such write is
// itself a change and the two would not stop. Capture what is needed, return,
// and write from elsewhere.
//
// # Typed sections
//
// [ObserveSection] decodes a section into a struct and keeps it current across
// reloads, publishing immutable snapshots through [ObservedSection]. A package
// consuming those settings declares a one-method interface over its own struct
// and never imports this one, which is what keeps configuration from spreading
// through a codebase.
package config
