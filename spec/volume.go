package spec

import (
	"math/big"
	"path"
	"strings"
	"unicode"
)

// SameVolumeRequest compares expanded, validated volume configurations.
// Runtimes use the same comparison before reattaching retained storage:
// storage identity ignores who declared it, but its settings cannot change
// silently when a new composition asks for the same destination.
func SameVolumeRequest(a, b Volume) bool {
	if path.Clean(a.Path) != path.Clean(b.Path) || a.Tmpfs != b.Tmpfs {
		return false
	}
	if strings.TrimLeft(a.Mode, "0") != strings.TrimLeft(b.Mode, "0") || (a.Mode == "") != (b.Mode == "") {
		return false
	}
	if a.Size == "" || b.Size == "" {
		return a.Size == b.Size
	}
	x, y := volumeBytes(a.Size), volumeBytes(b.Size)
	return x != nil && y != nil && x.Cmp(y) == 0
}

// Rational arithmetic preserves equivalence for decimal sizes and values
// beyond machine integers; floating point can silently equate two limits.
func volumeBytes(size string) *big.Rat {
	if !sizeBytes.MatchString(size) {
		return nil
	}
	number := strings.TrimRightFunc(size, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsLetter(r) })
	value, ok := new(big.Rat).SetString(number)
	if !ok {
		return nil
	}
	suffix := strings.ToLower(strings.TrimSpace(size[len(number):]))
	if suffix == "" {
		return value
	}
	power := strings.IndexByte("kmgt", suffix[0]) + 1
	factor := new(big.Int).Exp(big.NewInt(1024), big.NewInt(int64(power)), nil)
	return value.Mul(value, new(big.Rat).SetInt(factor))
}
