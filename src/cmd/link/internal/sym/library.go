// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sym

type Library struct {
	Objref        string
	Srcref        string
	File          string
	Pkg           string
	Shlib         string
	Hash          string
	ImportStrings []string
	Imports       []*Library
	Main          bool
	Safe          bool
	Units         []*CompilationUnit

	Textp2       []LoaderSym // text syms defined in this library
	DupTextSyms2 []LoaderSym // dupok text syms defined in this library
}

func (l Library) String() string {
	return l.Pkg
}
