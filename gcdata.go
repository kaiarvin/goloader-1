package goloader

import (
	"cmd/objfile/gcprog"
	"cmd/objfile/sys"
	"fmt"
	"sort"
	"unsafe"
)

const (
	KindGCProg = 1 << 6
)

// copy from $GOROOT/src/cmd/internal/ld/decodesym.go
func decodeInuxi(arch *sys.Arch, p []byte, sz int) uint64 {
	switch sz {
	case 2:
		return uint64(arch.ByteOrder.Uint16(p))
	case 4:
		return uint64(arch.ByteOrder.Uint32(p))
	case 8:
		return arch.ByteOrder.Uint64(p)
	default:
		panic("unreachable")
	}
}

func decodetypePtrdata(linker *Linker, p []byte) int64 {
	return int64(decodeInuxi(linker.Arch, p[linker.Arch.PtrSize:], linker.Arch.PtrSize)) // 0x8 / 0x10
}

// Type.commonType.kind
func decodetypeUsegcprog(linker *Linker, p []byte) uint8 {
	return p[2*linker.Arch.PtrSize+7] & KindGCProg //  0x13 / 0x1f
}

func decodetypeGcprogShlib(linker *Linker, data []byte) uint64 {
	return decodeInuxi(linker.Arch, data[2*int32(linker.Arch.PtrSize)+8+1*int32(linker.Arch.PtrSize):], linker.Arch.PtrSize)
}

func decodeReloc(relocs *[]Reloc, off int32) Reloc {
	for i := 0; i < len(*relocs); i++ {
		rel := (*relocs)[i]
		if int32(rel.Offset) == off {
			return rel
		}
	}
	return Reloc{}
}

func decodeRelocSym(relocs *[]Reloc, off int32) *Sym {
	return decodeReloc(relocs, off).Sym
}

func decodetypeGcmask(linker *Linker, s *ObjSymbol) []byte {
	//TODO shared library
	mask := decodeRelocSym(&s.Reloc, 2*int32(linker.Arch.PtrSize)+8+1*int32(linker.Arch.PtrSize))
	return linker.objsymbolMap[mask.Name].Data
}

// Type.commonType.gc
func decodetypeGcprog(linker *Linker, s *ObjSymbol) []byte {
	//TODO shared library
	rs := decodeRelocSym(&s.Reloc, 2*int32(linker.Arch.PtrSize)+8+1*int32(linker.Arch.PtrSize))
	return linker.objsymbolMap[rs.Name].Data
}

func genGCData(linker *Linker, codeModule *CodeModule, symbolMap map[string]uintptr, w *gcprog.Writer, sym *Sym) error {
	segment := &codeModule.segment
	//if symbol is in loader, ignore generate gc data
	if symbolMap[sym.Name] < uintptr(segment.dataBase) || symbolMap[sym.Name] > uintptr(segment.dataBase+segment.sumDataLen) {
		return nil
	}
	typeName := linker.objsymbolMap[sym.Name].Type
	if _, ok := linker.objsymbolMap[typeName]; !ok {
		return fmt.Errorf("type:%s not found\n", typeName)
	}
	start := int(symbolMap[typeName]) - (segment.codeBase)
	end := start + len(linker.objsymbolMap[typeName].Data)
	typeData := segment.codeByte[start:end]
	nptr := decodetypePtrdata(linker, typeData) / int64(linker.Arch.PtrSize)
	sval := int64(symbolMap[sym.Name] - uintptr(segment.dataBase))
	if int(sym.Kind) == SBSS {
		sval = sval - int64(segment.dataLen+segment.noptrdataLen)
	}
	if decodetypeUsegcprog(linker, typeData) == 0 {
		// Copy pointers from mask into program.
		mask := decodetypeGcmask(linker, linker.objsymbolMap[typeName])
		for i := int64(0); i < nptr; i++ {
			if (mask[i/8]>>uint(i%8))&1 != 0 {
				w.Ptr(sval/int64(linker.Arch.PtrSize) + i)
			}
		}
	} else {
		prog := decodetypeGcprog(linker, linker.objsymbolMap[typeName])
		w.ZeroUntil(sval / int64(linker.Arch.PtrSize))
		w.Append(prog[4:], nptr)
	}
	return nil
}

func getSortSym(symMap map[string]*Sym, kind int) []*Sym {
	syms := make(map[int]*Sym)
	keys := []int{}
	for _, sym := range symMap {
		if sym.Kind == kind {
			syms[sym.Offset] = sym
			keys = append(keys, sym.Offset)
		}
	}
	sort.Ints(keys)
	symbols := []*Sym{}
	for _, key := range keys {
		symbols = append(symbols, syms[key])
	}
	return symbols
}

func fillGCData(linker *Linker, codeModule *CodeModule, symbolMap map[string]uintptr) error {
	module := codeModule.module
	gcdata := []byte{}
	w := gcprog.Writer{}
	w.Init(func(x byte) {
		gcdata = append(gcdata, x)
	})
	for _, sym := range getSortSym(linker.symMap, SDATA) {
		err := genGCData(linker, codeModule, symbolMap, &w, sym)
		if err != nil {
			return err
		}
	}
	w.ZeroUntil(int64(module.edata-module.data) / int64(linker.Arch.PtrSize))
	w.End()
	module.gcdata = (*sliceHeader)(unsafe.Pointer(&gcdata)).Data
	module.gcdatamask = progToPointerMask((*byte)(adduintptr(module.gcdata, 0)), module.edata-module.data)

	gcdata = []byte{}
	w = gcprog.Writer{}
	w.Init(func(x byte) {
		gcdata = append(gcdata, x)
	})
	for _, sym := range getSortSym(linker.symMap, SBSS) {
		err := genGCData(linker, codeModule, symbolMap, &w, sym)
		if err != nil {
			return err
		}
	}
	w.ZeroUntil(int64(module.ebss-module.bss) / int64(linker.Arch.PtrSize))
	w.End()
	module.gcbss = (*sliceHeader)(unsafe.Pointer(&gcdata)).Data
	module.gcbssmask = progToPointerMask((*byte)(adduintptr(module.gcbss, 0)), module.ebss-module.bss)
	return nil
}
