// Inferno utils/8l/asm.c
// https://bitbucket.org/inferno-os/inferno-os/src/default/utils/8l/asm.c
//
//	Copyright © 1994-1999 Lucent Technologies Inc.  All rights reserved.
//	Portions Copyright © 1995-1997 C H Forsyth (forsyth@terzarima.net)
//	Portions Copyright © 1997-1999 Vita Nuova Limited
//	Portions Copyright © 2000-2007 Vita Nuova Holdings Limited (www.vitanuova.com)
//	Portions Copyright © 2004,2006 Bruce Ellis
//	Portions Copyright © 2005-2007 C H Forsyth (forsyth@terzarima.net)
//	Revisions Copyright © 2000-2007 Lucent Technologies Inc. and others
//	Portions Copyright © 2009 The Go Authors. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.  IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package x86

import (
	"cmd/internal/objabi"
	"cmd/internal/sys"
	"cmd/link/internal/ld"
	"cmd/link/internal/loader"
	"cmd/link/internal/sym"
	"debug/elf"
	"log"
	"sync"
)

func gentext2(ctxt *ld.Link, ldr *loader.Loader) {
	if ctxt.DynlinkingGo() {
		// We need get_pc_thunk.
	} else {
		switch ctxt.BuildMode {
		case ld.BuildModeCArchive:
			if !ctxt.IsELF {
				return
			}
		case ld.BuildModePIE, ld.BuildModeCShared, ld.BuildModePlugin:
			// We need get_pc_thunk.
		default:
			return
		}
	}

	// Generate little thunks that load the PC of the next instruction into a register.
	thunks := make([]loader.Sym, 0, 7+len(ctxt.Textp2))
	for _, r := range [...]struct {
		name string
		num  uint8
	}{
		{"ax", 0},
		{"cx", 1},
		{"dx", 2},
		{"bx", 3},
		// sp
		{"bp", 5},
		{"si", 6},
		{"di", 7},
	} {
		thunkfunc := ldr.CreateSymForUpdate("__x86.get_pc_thunk."+r.name, 0)
		thunkfunc.SetType(sym.STEXT)
		ldr.SetAttrLocal(thunkfunc.Sym(), true)
		o := func(op ...uint8) {
			for _, op1 := range op {
				thunkfunc.AddUint8(op1)
			}
		}
		// 8b 04 24	mov    (%esp),%eax
		// Destination register is in bits 3-5 of the middle byte, so add that in.
		o(0x8b, 0x04+r.num<<3, 0x24)
		// c3		ret
		o(0xc3)

		thunks = append(thunks, thunkfunc.Sym())
	}
	ctxt.Textp2 = append(thunks, ctxt.Textp2...) // keep Textp2 in dependency order

	initfunc, addmoduledata := ld.PrepareAddmoduledata(ctxt)
	if initfunc == nil {
		return
	}

	o := func(op ...uint8) {
		for _, op1 := range op {
			initfunc.AddUint8(op1)
		}
	}

	// go.link.addmoduledata:
	//      53                      push %ebx
	//      e8 00 00 00 00          call __x86.get_pc_thunk.cx + R_CALL __x86.get_pc_thunk.cx
	//      8d 81 00 00 00 00       lea 0x0(%ecx), %eax + R_PCREL ctxt.Moduledata
	//      8d 99 00 00 00 00       lea 0x0(%ecx), %ebx + R_GOTPC _GLOBAL_OFFSET_TABLE_
	//      e8 00 00 00 00          call runtime.addmoduledata@plt + R_CALL runtime.addmoduledata
	//      5b                      pop %ebx
	//      c3                      ret

	o(0x53)

	o(0xe8)
	initfunc.AddSymRef(ctxt.Arch, ldr.Lookup("__x86.get_pc_thunk.cx", 0), 0, objabi.R_CALL, 4)

	o(0x8d, 0x81)
	initfunc.AddPCRelPlus(ctxt.Arch, ctxt.Moduledata2, 6)

	o(0x8d, 0x99)
	gotsym := ldr.LookupOrCreateSym("_GLOBAL_OFFSET_TABLE_", 0)
	initfunc.AddSymRef(ctxt.Arch, gotsym, 12, objabi.R_PCREL, 4)
	o(0xe8)
	initfunc.AddSymRef(ctxt.Arch, addmoduledata, 0, objabi.R_CALL, 4)

	o(0x5b)

	o(0xc3)
}

func adddynrel(target *ld.Target, ldr *loader.Loader, syms *ld.ArchSyms, s *sym.Symbol, r *sym.Reloc) bool {
	targ := r.Sym

	switch r.Type {
	default:
		if r.Type >= objabi.ElfRelocOffset {
			ld.Errorf(s, "unexpected relocation type %d (%s)", r.Type, sym.RelocName(target.Arch, r.Type))
			return false
		}

		// Handle relocations found in ELF object files.
	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_PC32):
		if targ.Type == sym.SDYNIMPORT {
			ld.Errorf(s, "unexpected R_386_PC32 relocation for dynamic symbol %s", targ.Name)
		}
		// TODO(mwhudson): the test of VisibilityHidden here probably doesn't make
		// sense and should be removed when someone has thought about it properly.
		if (targ.Type == 0 || targ.Type == sym.SXREF) && !targ.Attr.VisibilityHidden() {
			ld.Errorf(s, "unknown symbol %s in pcrel", targ.Name)
		}
		r.Type = objabi.R_PCREL
		r.Add += 4
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_PLT32):
		r.Type = objabi.R_PCREL
		r.Add += 4
		if targ.Type == sym.SDYNIMPORT {
			addpltsym(target, syms, targ)
			r.Sym = syms.PLT
			r.Add += int64(targ.Plt())
		}

		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_GOT32),
		objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_GOT32X):
		if targ.Type != sym.SDYNIMPORT {
			// have symbol
			if r.Off >= 2 && s.P[r.Off-2] == 0x8b {
				// turn MOVL of GOT entry into LEAL of symbol address, relative to GOT.
				s.P[r.Off-2] = 0x8d

				r.Type = objabi.R_GOTOFF
				return true
			}

			if r.Off >= 2 && s.P[r.Off-2] == 0xff && s.P[r.Off-1] == 0xb3 {
				// turn PUSHL of GOT entry into PUSHL of symbol itself.
				// use unnecessary SS prefix to keep instruction same length.
				s.P[r.Off-2] = 0x36

				s.P[r.Off-1] = 0x68
				r.Type = objabi.R_ADDR
				return true
			}

			ld.Errorf(s, "unexpected GOT reloc for non-dynamic symbol %s", targ.Name)
			return false
		}

		addgotsym(target, syms, targ)
		r.Type = objabi.R_CONST // write r->add during relocsym
		r.Sym = nil
		r.Add += int64(targ.Got())
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_GOTOFF):
		r.Type = objabi.R_GOTOFF
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_GOTPC):
		r.Type = objabi.R_PCREL
		r.Sym = syms.GOT
		r.Add += 4
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_386_32):
		if targ.Type == sym.SDYNIMPORT {
			ld.Errorf(s, "unexpected R_386_32 relocation for dynamic symbol %s", targ.Name)
		}
		r.Type = objabi.R_ADDR
		return true

	case objabi.MachoRelocOffset + ld.MACHO_GENERIC_RELOC_VANILLA*2 + 0:
		r.Type = objabi.R_ADDR
		if targ.Type == sym.SDYNIMPORT {
			ld.Errorf(s, "unexpected reloc for dynamic symbol %s", targ.Name)
		}
		return true

	case objabi.MachoRelocOffset + ld.MACHO_GENERIC_RELOC_VANILLA*2 + 1:
		if targ.Type == sym.SDYNIMPORT {
			addpltsym(target, syms, targ)
			r.Sym = syms.PLT
			r.Add = int64(targ.Plt())
			r.Type = objabi.R_PCREL
			return true
		}

		r.Type = objabi.R_PCREL
		return true

	case objabi.MachoRelocOffset + ld.MACHO_FAKE_GOTPCREL:
		if targ.Type != sym.SDYNIMPORT {
			// have symbol
			// turn MOVL of GOT entry into LEAL of symbol itself
			if r.Off < 2 || s.P[r.Off-2] != 0x8b {
				ld.Errorf(s, "unexpected GOT reloc for non-dynamic symbol %s", targ.Name)
				return false
			}

			s.P[r.Off-2] = 0x8d
			r.Type = objabi.R_PCREL
			return true
		}

		addgotsym(target, syms, targ)
		r.Sym = syms.GOT
		r.Add += int64(targ.Got())
		r.Type = objabi.R_PCREL
		return true
	}

	// Handle references to ELF symbols from our own object files.
	if targ.Type != sym.SDYNIMPORT {
		return true
	}
	switch r.Type {
	case objabi.R_CALL,
		objabi.R_PCREL:
		if target.IsExternal() {
			// External linker will do this relocation.
			return true
		}
		addpltsym(target, syms, targ)
		r.Sym = syms.PLT
		r.Add = int64(targ.Plt())
		return true

	case objabi.R_ADDR:
		if s.Type != sym.SDATA {
			break
		}
		if target.IsElf() {
			ld.Adddynsym(target, syms, targ)
			rel := syms.Rel
			rel.AddAddrPlus(target.Arch, s, int64(r.Off))
			rel.AddUint32(target.Arch, ld.ELF32_R_INFO(uint32(targ.Dynid), uint32(elf.R_386_32)))
			r.Type = objabi.R_CONST // write r->add during relocsym
			r.Sym = nil
			return true
		}

		if target.IsDarwin() && s.Size == int64(target.Arch.PtrSize) && r.Off == 0 {
			// Mach-O relocations are a royal pain to lay out.
			// They use a compact stateful bytecode representation
			// that is too much bother to deal with.
			// Instead, interpret the C declaration
			//	void *_Cvar_stderr = &stderr;
			// as making _Cvar_stderr the name of a GOT entry
			// for stderr. This is separate from the usual GOT entry,
			// just in case the C code assigns to the variable,
			// and of course it only works for single pointers,
			// but we only need to support cgo and that's all it needs.
			ld.Adddynsym(target, syms, targ)

			got := syms.GOT
			s.Type = got.Type
			s.Attr |= sym.AttrSubSymbol
			s.Outer = got
			s.Sub = got.Sub
			got.Sub = s
			s.Value = got.Size
			got.AddUint32(target.Arch, 0)
			syms.LinkEditGOT.AddUint32(target.Arch, uint32(targ.Dynid))
			r.Type = objabi.ElfRelocOffset // ignore during relocsym
			return true
		}
	}

	return false
}

func elfreloc1(ctxt *ld.Link, r *sym.Reloc, sectoff int64) bool {
	ctxt.Out.Write32(uint32(sectoff))

	elfsym := r.Xsym.ElfsymForReloc()
	switch r.Type {
	default:
		return false
	case objabi.R_ADDR:
		if r.Siz == 4 {
			ctxt.Out.Write32(uint32(elf.R_386_32) | uint32(elfsym)<<8)
		} else {
			return false
		}
	case objabi.R_GOTPCREL:
		if r.Siz == 4 {
			ctxt.Out.Write32(uint32(elf.R_386_GOTPC))
			if r.Xsym.Name != "_GLOBAL_OFFSET_TABLE_" {
				ctxt.Out.Write32(uint32(sectoff))
				ctxt.Out.Write32(uint32(elf.R_386_GOT32) | uint32(elfsym)<<8)
			}
		} else {
			return false
		}
	case objabi.R_CALL:
		if r.Siz == 4 {
			if r.Xsym.Type == sym.SDYNIMPORT {
				ctxt.Out.Write32(uint32(elf.R_386_PLT32) | uint32(elfsym)<<8)
			} else {
				ctxt.Out.Write32(uint32(elf.R_386_PC32) | uint32(elfsym)<<8)
			}
		} else {
			return false
		}
	case objabi.R_PCREL:
		if r.Siz == 4 {
			ctxt.Out.Write32(uint32(elf.R_386_PC32) | uint32(elfsym)<<8)
		} else {
			return false
		}
	case objabi.R_TLS_LE:
		if r.Siz == 4 {
			ctxt.Out.Write32(uint32(elf.R_386_TLS_LE) | uint32(elfsym)<<8)
		} else {
			return false
		}
	case objabi.R_TLS_IE:
		if r.Siz == 4 {
			ctxt.Out.Write32(uint32(elf.R_386_GOTPC))
			ctxt.Out.Write32(uint32(sectoff))
			ctxt.Out.Write32(uint32(elf.R_386_TLS_GOTIE) | uint32(elfsym)<<8)
		} else {
			return false
		}
	}

	return true
}

func machoreloc1(arch *sys.Arch, out *ld.OutBuf, s *sym.Symbol, r *sym.Reloc, sectoff int64) bool {
	return false
}

func pereloc1(arch *sys.Arch, out *ld.OutBuf, s *sym.Symbol, r *sym.Reloc, sectoff int64) bool {
	var v uint32

	rs := r.Xsym

	if rs.Dynid < 0 {
		ld.Errorf(s, "reloc %d (%s) to non-coff symbol %s type=%d (%s)", r.Type, sym.RelocName(arch, r.Type), rs.Name, rs.Type, rs.Type)
		return false
	}

	out.Write32(uint32(sectoff))
	out.Write32(uint32(rs.Dynid))

	switch r.Type {
	default:
		return false

	case objabi.R_DWARFSECREF:
		v = ld.IMAGE_REL_I386_SECREL

	case objabi.R_ADDR:
		v = ld.IMAGE_REL_I386_DIR32

	case objabi.R_CALL,
		objabi.R_PCREL:
		v = ld.IMAGE_REL_I386_REL32
	}

	out.Write16(uint16(v))

	return true
}

func archreloc(target *ld.Target, syms *ld.ArchSyms, r *sym.Reloc, s *sym.Symbol, val int64) (int64, bool) {
	if target.IsExternal() {
		return val, false
	}
	switch r.Type {
	case objabi.R_CONST:
		return r.Add, true
	case objabi.R_GOTOFF:
		return ld.Symaddr(r.Sym) + r.Add - ld.Symaddr(syms.GOT), true
	}

	return val, false
}

func archrelocvariant(target *ld.Target, syms *ld.ArchSyms, r *sym.Reloc, s *sym.Symbol, t int64) int64 {
	log.Fatalf("unexpected relocation variant")
	return t
}

func elfsetupplt(ctxt *ld.Link, plt, got *loader.SymbolBuilder, dynamic loader.Sym) {
	if plt.Size() == 0 {
		// pushl got+4
		plt.AddUint8(0xff)

		plt.AddUint8(0x35)
		plt.AddAddrPlus(ctxt.Arch, got.Sym(), 4)

		// jmp *got+8
		plt.AddUint8(0xff)

		plt.AddUint8(0x25)
		plt.AddAddrPlus(ctxt.Arch, got.Sym(), 8)

		// zero pad
		plt.AddUint32(ctxt.Arch, 0)

		// assume got->size == 0 too
		got.AddAddrPlus(ctxt.Arch, dynamic, 0)

		got.AddUint32(ctxt.Arch, 0)
		got.AddUint32(ctxt.Arch, 0)
	}
}

func addpltsym(target *ld.Target, syms *ld.ArchSyms, s *sym.Symbol) {
	if s.Plt() >= 0 {
		return
	}

	ld.Adddynsym(target, syms, s)

	if target.IsElf() {
		plt := syms.PLT
		got := syms.GOTPLT
		rel := syms.RelPLT
		if plt.Size == 0 {
			panic("plt is not set up")
		}

		// jmpq *got+size
		plt.AddUint8(0xff)

		plt.AddUint8(0x25)
		plt.AddAddrPlus(target.Arch, got, got.Size)

		// add to got: pointer to current pos in plt
		got.AddAddrPlus(target.Arch, plt, plt.Size)

		// pushl $x
		plt.AddUint8(0x68)

		plt.AddUint32(target.Arch, uint32(rel.Size))

		// jmp .plt
		plt.AddUint8(0xe9)

		plt.AddUint32(target.Arch, uint32(-(plt.Size + 4)))

		// rel
		rel.AddAddrPlus(target.Arch, got, got.Size-4)

		rel.AddUint32(target.Arch, ld.ELF32_R_INFO(uint32(s.Dynid), uint32(elf.R_386_JMP_SLOT)))

		s.SetPlt(int32(plt.Size - 16))
	} else if target.IsDarwin() {
		// Same laziness as in 6l.

		plt := syms.PLT

		addgotsym(target, syms, s)

		syms.LinkEditPLT.AddUint32(target.Arch, uint32(s.Dynid))

		// jmpq *got+size(IP)
		s.SetPlt(int32(plt.Size))

		plt.AddUint8(0xff)
		plt.AddUint8(0x25)
		plt.AddAddrPlus(target.Arch, syms.GOT, int64(s.Got()))
	} else {
		ld.Errorf(s, "addpltsym: unsupported binary format")
	}
}

func addgotsym(target *ld.Target, syms *ld.ArchSyms, s *sym.Symbol) {
	if s.Got() >= 0 {
		return
	}

	ld.Adddynsym(target, syms, s)
	got := syms.GOT
	s.SetGot(int32(got.Size))
	got.AddUint32(target.Arch, 0)

	if target.IsElf() {
		rel := syms.Rel
		rel.AddAddrPlus(target.Arch, got, int64(s.Got()))
		rel.AddUint32(target.Arch, ld.ELF32_R_INFO(uint32(s.Dynid), uint32(elf.R_386_GLOB_DAT)))
	} else if target.IsDarwin() {
		syms.LinkEditGOT.AddUint32(target.Arch, uint32(s.Dynid))
	} else {
		ld.Errorf(s, "addgotsym: unsupported binary format")
	}
}

func asmb(ctxt *ld.Link) {
	if ctxt.IsELF {
		ld.Asmbelfsetup()
	}

	var wg sync.WaitGroup
	sect := ld.Segtext.Sections[0]
	offset := sect.Vaddr - ld.Segtext.Vaddr + ld.Segtext.Fileoff
	f := func(ctxt *ld.Link, out *ld.OutBuf, start, length int64) {
		ld.CodeblkPad(ctxt, out, start, length, []byte{0xCC})
	}
	ld.WriteParallel(&wg, f, ctxt, offset, sect.Vaddr, sect.Length)

	for _, sect := range ld.Segtext.Sections[1:] {
		offset := sect.Vaddr - ld.Segtext.Vaddr + ld.Segtext.Fileoff
		ld.WriteParallel(&wg, ld.Datblk, ctxt, offset, sect.Vaddr, sect.Length)
	}

	if ld.Segrodata.Filelen > 0 {
		ld.WriteParallel(&wg, ld.Datblk, ctxt, ld.Segrodata.Fileoff, ld.Segrodata.Vaddr, ld.Segrodata.Filelen)
	}

	if ld.Segrelrodata.Filelen > 0 {
		ld.WriteParallel(&wg, ld.Datblk, ctxt, ld.Segrelrodata.Fileoff, ld.Segrelrodata.Vaddr, ld.Segrelrodata.Filelen)
	}

	ld.WriteParallel(&wg, ld.Datblk, ctxt, ld.Segdata.Fileoff, ld.Segdata.Vaddr, ld.Segdata.Filelen)

	ld.WriteParallel(&wg, ld.Dwarfblk, ctxt, ld.Segdwarf.Fileoff, ld.Segdwarf.Vaddr, ld.Segdwarf.Filelen)
	wg.Wait()
}

func asmb2(ctxt *ld.Link) {
	machlink := uint32(0)
	if ctxt.HeadType == objabi.Hdarwin {
		machlink = uint32(ld.Domacholink(ctxt))
	}

	ld.Symsize = 0
	ld.Spsize = 0
	ld.Lcsize = 0
	symo := uint32(0)
	if !*ld.FlagS {
		// TODO: rationalize
		switch ctxt.HeadType {
		default:
			if ctxt.IsELF {
				symo = uint32(ld.Segdwarf.Fileoff + ld.Segdwarf.Filelen)
				symo = uint32(ld.Rnd(int64(symo), int64(*ld.FlagRound)))
			}

		case objabi.Hplan9:
			symo = uint32(ld.Segdata.Fileoff + ld.Segdata.Filelen)

		case objabi.Hdarwin:
			symo = uint32(ld.Segdwarf.Fileoff + uint64(ld.Rnd(int64(ld.Segdwarf.Filelen), int64(*ld.FlagRound))) + uint64(machlink))

		case objabi.Hwindows:
			symo = uint32(ld.Segdwarf.Fileoff + ld.Segdwarf.Filelen)
			symo = uint32(ld.Rnd(int64(symo), ld.PEFILEALIGN))
		}

		ctxt.Out.SeekSet(int64(symo))
		switch ctxt.HeadType {
		default:
			if ctxt.IsELF {
				ld.Asmelfsym(ctxt)
				ctxt.Out.Write(ld.Elfstrdat)

				if ctxt.LinkMode == ld.LinkExternal {
					ld.Elfemitreloc(ctxt)
				}
			}

		case objabi.Hplan9:
			ld.Asmplan9sym(ctxt)

			sym := ctxt.Syms.Lookup("pclntab", 0)
			if sym != nil {
				ld.Lcsize = int32(len(sym.P))
				ctxt.Out.Write(sym.P)
			}

		case objabi.Hwindows:
			// Do nothing

		case objabi.Hdarwin:
			if ctxt.LinkMode == ld.LinkExternal {
				ld.Machoemitreloc(ctxt)
			}
		}
	}

	ctxt.Out.SeekSet(0)
	switch ctxt.HeadType {
	default:
	case objabi.Hplan9: /* plan9 */
		magic := int32(4*11*11 + 7)

		ctxt.Out.Write32b(uint32(magic))              /* magic */
		ctxt.Out.Write32b(uint32(ld.Segtext.Filelen)) /* sizes */
		ctxt.Out.Write32b(uint32(ld.Segdata.Filelen))
		ctxt.Out.Write32b(uint32(ld.Segdata.Length - ld.Segdata.Filelen))
		ctxt.Out.Write32b(uint32(ld.Symsize))          /* nsyms */
		ctxt.Out.Write32b(uint32(ld.Entryvalue(ctxt))) /* va of entry */
		ctxt.Out.Write32b(uint32(ld.Spsize))           /* sp offsets */
		ctxt.Out.Write32b(uint32(ld.Lcsize))           /* line offsets */

	case objabi.Hdarwin:
		ld.Asmbmacho(ctxt)

	case objabi.Hlinux,
		objabi.Hfreebsd,
		objabi.Hnetbsd,
		objabi.Hopenbsd:
		ld.Asmbelf(ctxt, int64(symo))

	case objabi.Hwindows:
		ld.Asmbpe(ctxt)
	}
}
