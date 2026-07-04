package compress

// Registry maps a ContentType to its ordered, chainable compressors. It is a
// pure data structure: the concrete chains are wired by the caller (see the
// compressors package's DefaultChains) so this package never imports the
// concrete compressors — that would be an import cycle.
type Registry struct {
	chains map[ContentType][]Compressor
}

// NewRegistry builds a Registry from a type→chain map. The map is used as-is;
// callers should not mutate it after construction (the pipeline reads it
// concurrently without locking).
func NewRegistry(chains map[ContentType][]Compressor) *Registry {
	return &Registry{chains: chains}
}

// Chain returns the ordered compressors for a content type, or nil if none are
// registered (the pipeline treats nil as "no compressor available").
func (r *Registry) Chain(ct ContentType) []Compressor {
	if r == nil {
		return nil
	}
	return r.chains[ct]
}
