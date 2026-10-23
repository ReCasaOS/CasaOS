//go:build !linux

package fileroots

// Open: confinement needs openat2; no roots stays unconfined.
func Open(paths []string) (*Roots, error) {
	if len(paths) > 0 {
		return nil, ErrUnsupported
	}
	return &Roots{}, nil
}

func (r *Roots) Close() {}

func (r *Roots) Check(path string) (string, error) {
	if !r.Confined() {
		return path, nil
	}
	return "", ErrUnsupported
}
