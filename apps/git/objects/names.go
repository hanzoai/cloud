package objects

import (
	"bytes"
	"errors"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// names calls fn with each object the content of an object of type t names: a commit's
// tree and parents, a tag's target, a tree's entries (a submodule's commit lives in
// another repository, so it is not one). Content that is not that kind of object is an
// error.
func names(t plumbing.ObjectType, b []byte, fn func(plumbing.Hash)) error {
	switch t {
	case plumbing.CommitObject:
		for len(b) > 0 {
			line, rest, _ := bytes.Cut(b, []byte{'\n'})
			if len(line) == 0 {
				return nil
			}
			b = rest
			for _, key := range [...]string{"tree ", "parent "} {
				if v, ok := bytes.CutPrefix(line, []byte(key)); ok {
					if !hex40(v) {
						return errors.New("malformed commit")
					}
					fn(plumbing.NewHash(string(v)))
				}
			}
		}
		return nil
	case plumbing.TagObject:
		line, _, _ := bytes.Cut(b, []byte{'\n'})
		v, ok := bytes.CutPrefix(line, []byte("object "))
		if !ok || !hex40(v) {
			return errors.New("malformed tag")
		}
		fn(plumbing.NewHash(string(v)))
		return nil
	case plumbing.TreeObject:
		for len(b) > 0 {
			sp := bytes.IndexByte(b, ' ')
			if sp < 0 {
				return errors.New("malformed tree")
			}
			mode := b[:sp]
			nul := bytes.IndexByte(b[sp+1:], 0)
			if nul < 0 || len(b) < sp+1+nul+1+hashSize {
				return errors.New("malformed tree")
			}
			at := sp + 1 + nul + 1
			if string(mode) != "160000" {
				fn(hashOf(b[at : at+hashSize]))
			}
			b = b[at+hashSize:]
		}
		return nil
	}
	return nil
}

func hex40(b []byte) bool {
	if len(b) != 40 {
		return false
	}
	return !strings.ContainsFunc(string(b), func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	})
}
