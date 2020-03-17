package macho

// High level access to low level data structures.

import (
	"bytes"
	"compress/zlib"
	"debug/dwarf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/apex/log"
	"github.com/blacktop/go-macho/commands"
	"github.com/blacktop/go-macho/header"
	"github.com/blacktop/go-macho/types"
)

type sections []*Section

// A File represents an open Mach-O file.
type File struct {
	header.FileHeader
	ByteOrder binary.ByteOrder
	Loads     []Load
	// Sections  []*Section
	Sections sections

	Symtab   *Symtab
	Dysymtab *Dysymtab

	closer io.Closer
}

// A Load represents any Mach-O load command.
type Load interface {
	Raw() []byte
}

// A LoadBytes is the uninterpreted bytes of a Mach-O load command.
type LoadBytes []byte

func (b LoadBytes) Raw() []byte { return b }

// A SegmentHeader is the header for a Mach-O 32-bit or 64-bit load segment command.
type SegmentHeader struct {
	Cmd     commands.LoadCmd
	Len     uint32
	Name    string
	Addr    uint64
	Memsz   uint64
	Offset  uint64
	Filesz  uint64
	Maxprot types.VmProtection
	Prot    types.VmProtection
	Nsect   uint32
	Flag    commands.SegFlag
}

// A Segment represents a Mach-O 32-bit or 64-bit load segment command.
type Segment struct {
	LoadBytes
	SegmentHeader

	// Embed ReaderAt for ReadAt method.
	// Do not embed SectionReader directly
	// to avoid having Read and Seek.
	// If a client wants Read and Seek it must use
	// Open() to avoid fighting over the seek offset
	// with other clients.
	io.ReaderAt
	sr *io.SectionReader
}

// Data reads and returns the contents of the segment.
func (s *Segment) Data() ([]byte, error) {
	dat := make([]byte, s.sr.Size())
	n, err := s.sr.ReadAt(dat, 0)
	if n == len(dat) {
		err = nil
	}
	return dat[0:n], err
}

// Open returns a new ReadSeeker reading the segment.
func (s *Segment) Open() io.ReadSeeker { return io.NewSectionReader(s.sr, 0, 1<<63-1) }

type SectionHeader struct {
	Name   string
	Seg    string
	Addr   uint64
	Size   uint64
	Offset uint32
	Align  uint32
	Reloff uint32
	Nreloc uint32
	Flags  commands.SectionFlag
}

// A Reloc represents a Mach-O relocation.
type Reloc struct {
	Addr  uint32
	Value uint32
	// when Scattered == false && Extern == true, Value is the symbol number.
	// when Scattered == false && Extern == false, Value is the section number.
	// when Scattered == true, Value is the value that this reloc refers to.
	Type      uint8
	Len       uint8 // 0=byte, 1=word, 2=long, 3=quad
	Pcrel     bool
	Extern    bool // valid if Scattered == false
	Scattered bool
}

type Section struct {
	SectionHeader
	Relocs []Reloc

	// Embed ReaderAt for ReadAt method.
	// Do not embed SectionReader directly
	// to avoid having Read and Seek.
	// If a client wants Read and Seek it must use
	// Open() to avoid fighting over the seek offset
	// with other clients.
	io.ReaderAt
	sr *io.SectionReader
}

// Data reads and returns the contents of the Mach-O section.
func (s *Section) Data() ([]byte, error) {
	dat := make([]byte, s.sr.Size())
	n, err := s.sr.ReadAt(dat, 0)
	if n == len(dat) {
		err = nil
	}
	return dat[0:n], err
}

// Open returns a new ReadSeeker reading the Mach-O section.
func (s *Section) Open() io.ReadSeeker { return io.NewSectionReader(s.sr, 0, 1<<63-1) }

type (
	// A Symtab represents a Mach-O symbol table command.
	Symtab struct {
		LoadBytes
		commands.SymtabCmd
		Syms []Symbol
	}
	// A Symbol is a Mach-O 32-bit or 64-bit symbol table entry.
	Symbol struct {
		Name  string
		Type  types.NLType
		Sect  uint8
		Desc  uint16
		Value uint64
	}
	// #define	LC_SYMSEG	0x3	/* link-edit gdb symbol table info (obsolete) */
	// #define	LC_THREAD	0x4	/* thread */
	// A UnixThread represents a Mach-O unix thread command.
	UnixThread struct {
		LoadBytes
	}
	// #define	LC_LOADFVMLIB	0x6	/* load a specified fixed VM shared library */
	// #define	LC_IDFVMLIB	0x7	/* fixed VM shared library identification */
	// #define	LC_IDENT	0x8	/* object identification info (obsolete) */
	// #define LC_FVMFILE	0x9	/* fixed VM file inclusion (internal use) */
	// #define LC_PREPAGE      0xa     /* prepage command (internal use) */
	// A Dysymtab represents a Mach-O dynamic symbol table command.
	Dysymtab struct {
		LoadBytes
		commands.DysymtabCmd
		IndirectSyms []uint32 // indices into Symtab.Syms
	}
	// A Dylib represents a Mach-O load dynamic library command.
	Dylib struct {
		LoadBytes
		Name           string
		Time           uint32
		CurrentVersion string
		CompatVersion  string
	}
	// A DylibID represents a Mach-O load dynamic library ident command.
	DylibID Dylib
	// #define LC_LOAD_DYLINKER 0xe	/* load a dynamic linker */
	// #define LC_ID_DYLINKER	0xf	/* dynamic linker identification */
	// #define	LC_PREBOUND_DYLIB 0x10	/* modules prebound for a dynamically */
	// 				/*  linked shared library */
	// #define	LC_ROUTINES	0x11	/* image routines */

	SubFramework struct {
		LoadBytes
		Framework string
	}
	// #define	LC_SUB_UMBRELLA 0x13	/* sub umbrella */
	// A SubClient is a Mach-O dynamic sub client command.
	SubClient struct {
		LoadBytes
		Name string
	}

	// #define	LC_SUB_LIBRARY  0x15	/* sub library */
	// #define	LC_TWOLEVEL_HINTS 0x16	/* two-level namespace lookup hints */
	// #define	LC_PREBIND_CKSUM  0x17	/* prebind checksum */

	// A WeakDylib represents a Mach-O load weak dynamic library command.
	WeakDylib Dylib

	Routines64 struct {
		LoadBytes
		InitAddress uint64
		InitModule  uint64
	}
	// A uuid represents a Mach-O uuid command.
	UUID struct {
		LoadBytes
		ID string
	}
	// A Rpath represents a Mach-O rpath command.
	Rpath struct {
		LoadBytes
		Path string
	}
	// #define LC_CODE_SIGNATURE 0x1d	/* local of code signature */
	CodeSignature struct {
		LoadBytes
		Offset uint32
		Size   uint32
	}
	// #define LC_SEGMENT_SPLIT_INFO 0x1e /* local of info to split segments */
	SplitInfo struct {
		LoadBytes
		Offset uint32
		Size   uint32
	}

	ReExportDylib Dylib
	// #define	LC_LAZY_LOAD_DYLIB 0x20	/* delay load of dylib until first use */
	// #define	LC_ENCRYPTION_INFO 0x21	/* encrypted segment information */

	// A DyldInfo represents a Mach-O id dyld info command.
	DyldInfo struct {
		LoadBytes
		RebaseOff    uint32 // file offset to rebase info
		RebaseSize   uint32 //  size of rebase info
		BindOff      uint32 // file offset to binding info
		BindSize     uint32 // size of binding info
		WeakBindOff  uint32 // file offset to weak binding info
		WeakBindSize uint32 //  size of weak binding info
		LazyBindOff  uint32 // file offset to lazy binding info
		LazyBindSize uint32 //  size of lazy binding info
		ExportOff    uint32 // file offset to export info
		ExportSize   uint32 //  size of export info
	}

	// #define	LC_DYLD_INFO_ONLY (0x22|LC_REQ_DYLD)	/* compressed dyld information only */

	// A UpwardDylib represents a Mach-O load upward dylib command.
	UpwardDylib Dylib
	// #define LC_VERSION_MIN_MACOSX 0x24   /* build for MacOSX min OS version */
	VersionMinMacosx struct {
		LoadBytes
		Version string
		Sdk     string
	}
	// #define LC_VERSION_MIN_IPHONEOS 0x25 /* build for iPhoneOS min OS version */
	VersionMinIphoneos struct {
		LoadBytes
		Version string
		Sdk     string
	}
	// A FunctionStarts represents a Mach-O function starts command.
	FunctionStarts struct {
		LoadBytes
		Offset uint32
		Size   uint32
	}
	// #define LC_DYLD_ENVIRONMENT 0x27 /* string for dyld to treat
	// 				    like environment variable */
	// #define LC_MAIN (0x28|LC_REQ_DYLD) /* replacement for LC_UNIXTHREAD */

	// A DataInCode represents a Mach-O data in code command.
	DataInCode struct {
		LoadBytes
		Entries []types.DataInCodeEntry
	}

	// A SourceVersion represents a Mach-O source version.
	SourceVersion struct {
		LoadBytes
		Version string
	}
	// #define LC_DYLIB_CODE_SIGN_DRS 0x2B /* Code signing DRs copied from linked dylibs */
	// #define	LC_ENCRYPTION_INFO_64 0x2C /* 64-bit encrypted segment information */
	// #define LC_LINKER_OPTION 0x2D /* linker options in MH_OBJECT files */
	// #define LC_LINKER_OPTIMIZATION_HINT 0x2E /* optimization hints in MH_OBJECT files */
	// #define LC_VERSION_MIN_TVOS 0x2F /* build for AppleTV min OS version */
	// #define LC_VERSION_MIN_WATCHOS 0x30 /* build for Watch min OS version */
	// #define LC_NOTE 0x31 /* arbitrary data included within a Mach-O file */

	// A BuildVersion represents a Mach-O build for platform min OS version.
	BuildVersion struct {
		LoadBytes
		Platform    string /* platform */
		Minos       string /* X.Y.Z is encoded in nibbles xxxx.yy.zz */
		Sdk         string /* X.Y.Z is encoded in nibbles xxxx.yy.zz */
		NumTools    uint32 /* number of tool entries following this */
		Tool        string
		ToolVersion string
	}

// #define LC_DYLD_EXPORTS_TRIE (0x33 | LC_REQ_DYLD) /* used with linkedit_data_command, payload is trie */
// #define LC_DYLD_CHAINED_FIXUPS (0x34 | LC_REQ_DYLD) /* used with linkedit_data_command */
)

// A LinkEditData represents a Mach-O linkedit data command.
type LinkEditData struct {
	LoadBytes
	Offset uint32
	Size   uint32
}

/*
 * Mach-O reader
 */

// FormatError is returned by some operations if the data does
// not have the correct format for an object file.
type FormatError struct {
	off int64
	msg string
	val interface{}
}

func (e *FormatError) Error() string {
	msg := e.msg
	if e.val != nil {
		msg += fmt.Sprintf(" '%v'", e.val)
	}
	msg += fmt.Sprintf(" in record at byte %#x", e.off)
	return msg
}

// Open opens the named file using os.Open and prepares it for use as a Mach-O binary.
func Open(name string) (*File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	ff, err := NewFile(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	ff.closer = f
	return ff, nil
}

// Close closes the File.
// If the File was created using NewFile directly instead of Open,
// Close has no effect.
func (f *File) Close() error {
	var err error
	if f.closer != nil {
		err = f.closer.Close()
		f.closer = nil
	}
	return err
}

// NewFile creates a new File for accessing a Mach-O binary in an underlying reader.
// The Mach-O binary is expected to start at position 0 in the ReaderAt.
func NewFile(r io.ReaderAt) (*File, error) {
	f := new(File)
	sr := io.NewSectionReader(r, 0, 1<<63-1)

	// Read and decode Mach magic to determine byte order, size.
	// Magic32 and Magic64 differ only in the bottom bit.
	var ident [4]byte
	if _, err := r.ReadAt(ident[0:], 0); err != nil {
		return nil, err
	}
	be := binary.BigEndian.Uint32(ident[0:])
	le := binary.LittleEndian.Uint32(ident[0:])
	switch header.Magic32.Int() &^ 1 {
	case be &^ 1:
		f.ByteOrder = binary.BigEndian
		f.Magic = header.Magic(be)
	case le &^ 1:
		f.ByteOrder = binary.LittleEndian
		f.Magic = header.Magic(le)
	default:
		return nil, &FormatError{0, "invalid magic number", nil}
	}

	// Read entire file header.
	if err := binary.Read(sr, f.ByteOrder, &f.FileHeader); err != nil {
		return nil, err
	}

	// Then load commands.
	offset := int64(header.FileHeaderSize32)
	if f.Magic == header.Magic64 {
		offset = header.FileHeaderSize64
	}
	dat := make([]byte, f.Cmdsz)
	if _, err := r.ReadAt(dat, offset); err != nil {
		return nil, err
	}
	f.Loads = make([]Load, f.Ncmd)
	bo := f.ByteOrder
	for i := range f.Loads {
		// Each load command begins with uint32 command and length.
		if len(dat) < 8 {
			return nil, &FormatError{offset, "command block too small", nil}
		}
		cmd, siz := commands.LoadCmd(bo.Uint32(dat[0:4])), bo.Uint32(dat[4:8])
		if siz < 8 || siz > uint32(len(dat)) {
			return nil, &FormatError{offset, "invalid command block size", nil}
		}
		var cmddat []byte
		cmddat, dat = dat[0:siz], dat[siz:]
		offset += int64(siz)
		var s *Segment
		switch cmd {
		default:
			log.Warnf("found NEW load command: %s", cmd)
			f.Loads[i] = LoadBytes(cmddat)
		case commands.LoadCmdSegment:
			var seg32 commands.Segment32
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &seg32); err != nil {
				return nil, err
			}
			s = new(Segment)
			s.LoadBytes = cmddat
			s.Cmd = cmd
			s.Len = siz
			s.Name = cstring(seg32.Name[0:])
			s.Addr = uint64(seg32.Addr)
			s.Memsz = uint64(seg32.Memsz)
			s.Offset = uint64(seg32.Offset)
			s.Filesz = uint64(seg32.Filesz)
			s.Maxprot = seg32.Maxprot
			s.Prot = seg32.Prot
			s.Nsect = seg32.Nsect
			s.Flag = seg32.Flag
			f.Loads[i] = s
			for i := 0; i < int(s.Nsect); i++ {
				var sh32 commands.Section32
				if err := binary.Read(b, bo, &sh32); err != nil {
					return nil, err
				}
				sh := new(Section)
				sh.Name = cstring(sh32.Name[0:])
				sh.Seg = cstring(sh32.Seg[0:])
				sh.Addr = uint64(sh32.Addr)
				sh.Size = uint64(sh32.Size)
				sh.Offset = sh32.Offset
				sh.Align = sh32.Align
				sh.Reloff = sh32.Reloff
				sh.Nreloc = sh32.Nreloc
				sh.Flags = sh32.Flags
				if err := f.pushSection(sh, r); err != nil {
					return nil, err
				}
			}
		case commands.LoadCmdSegment64:
			var seg64 commands.Segment64
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &seg64); err != nil {
				return nil, err
			}
			s = new(Segment)
			s.LoadBytes = cmddat
			s.Cmd = cmd
			s.Len = siz
			s.Name = cstring(seg64.Name[0:])
			s.Addr = seg64.Addr
			s.Memsz = seg64.Memsz
			s.Offset = seg64.Offset
			s.Filesz = seg64.Filesz
			s.Maxprot = seg64.Maxprot
			s.Prot = seg64.Prot
			s.Nsect = seg64.Nsect
			s.Flag = seg64.Flag
			f.Loads[i] = s
			for i := 0; i < int(s.Nsect); i++ {
				var sh64 commands.Section64
				if err := binary.Read(b, bo, &sh64); err != nil {
					return nil, err
				}
				sh := new(Section)
				sh.Name = cstring(sh64.Name[0:])
				sh.Seg = cstring(sh64.Seg[0:])
				sh.Addr = sh64.Addr
				sh.Size = sh64.Size
				sh.Offset = sh64.Offset
				sh.Align = sh64.Align
				sh.Reloff = sh64.Reloff
				sh.Nreloc = sh64.Nreloc
				sh.Flags = sh64.Flags
				if err := f.pushSection(sh, r); err != nil {
					return nil, err
				}
			}
		case commands.LoadCmdSymtab:
			var hdr commands.SymtabCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			strtab := make([]byte, hdr.Strsize)
			if _, err := r.ReadAt(strtab, int64(hdr.Stroff)); err != nil {
				return f, nil
				// return nil, err
			}
			var symsz int
			if f.Magic == header.Magic64 {
				symsz = 16
			} else {
				symsz = 12
			}
			symdat := make([]byte, int(hdr.Nsyms)*symsz)
			if _, err := r.ReadAt(symdat, int64(hdr.Symoff)); err != nil {
				return nil, err
			}
			st, err := f.parseSymtab(symdat, strtab, cmddat, &hdr, offset)
			if err != nil {
				return nil, err
			}
			f.Loads[i] = st
			f.Symtab = st
		// case commands.LoadCmdSymseg:
		// case commands.LoadCmdThread:
		case commands.LoadCmdUnixThread:
			var ut commands.UnixThreadCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &ut); err != nil {
				return nil, err
			}
			l := new(UnixThread)
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdLoadfvmlib:
		// case commands.LoadCmdIdfvmlib:
		// case commands.LoadCmdIdent:
		// case commands.LoadCmdFvmfile:
		// case commands.LoadCmdPrepage:
		case commands.LoadCmdDysymtab:
			var hdr commands.DysymtabCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			dat := make([]byte, hdr.Nindirectsyms*4)
			if _, err := r.ReadAt(dat, int64(hdr.Indirectsymoff)); err != nil {
				return nil, err
			}
			x := make([]uint32, hdr.Nindirectsyms)
			if err := binary.Read(bytes.NewReader(dat), bo, x); err != nil {
				return nil, err
			}
			st := new(Dysymtab)
			st.LoadBytes = LoadBytes(cmddat)
			st.DysymtabCmd = hdr
			st.IndirectSyms = x
			f.Loads[i] = st
			f.Dysymtab = st
		case commands.LoadCmdDylib:
			var hdr commands.DylibCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(Dylib)
			if hdr.Name >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid name in dynamic library command", hdr.Name}
			}
			l.Name = cstring(cmddat[hdr.Name:])
			l.Time = hdr.Time
			l.CurrentVersion = hdr.CurrentVersion.String()
			l.CompatVersion = hdr.CompatVersion.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdDylibID:
			var hdr commands.DylibCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(DylibID)
			if hdr.Name >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid name in dynamic library ident command", hdr.Name}
			}
			l.Name = cstring(cmddat[hdr.Name:])
			l.Time = hdr.Time
			l.CurrentVersion = hdr.CurrentVersion.String()
			l.CompatVersion = hdr.CompatVersion.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdDylinker:
		// case commands.LoadCmdDylinkerID:
		// case commands.LoadCmdPreboundDylib:
		// case commands.LoadCmdRoutines:
		case commands.LoadCmdSubFramework:
			var sf commands.SubFrameworkCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &sf); err != nil {
				return nil, err
			}
			l := new(SubFramework)
			if sf.Framework >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid framework in subframework command", sf.Framework}
			}
			l.Framework = cstring(cmddat[sf.Framework:])
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdSubUmbrella:
		case commands.LoadCmdSubClient:
			var sc commands.SubClientCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &sc); err != nil {
				return nil, err
			}
			l := new(SubClient)
			if sc.Client >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid path in sub client command", sc.Client}
			}
			l.Name = cstring(cmddat[sc.Client:])
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdSubLibrary:
		// case commands.LoadCmdTwolevelHints:
		// case commands.LoadCmdPrebindCksum:
		case commands.LoadCmdLoadWeakDylib:
			var hdr commands.DylibCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(WeakDylib)
			if hdr.Name >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid name in weak dynamic library command", hdr.Name}
			}
			l.Name = cstring(cmddat[hdr.Name:])
			l.Time = hdr.Time
			l.CurrentVersion = hdr.CurrentVersion.String()
			l.CompatVersion = hdr.CompatVersion.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdRoutines64:
			var r64 commands.Routines64Cmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &r64); err != nil {
				return nil, err
			}
			l := new(Routines64)
			l.InitAddress = r64.InitAddress
			l.InitModule = r64.InitModule
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdUUID:
			var u commands.UUIDCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &u); err != nil {
				return nil, err
			}
			l := new(UUID)
			l.ID = u.UUID.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdRpath:
			var hdr commands.RpathCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(Rpath)
			if hdr.Path >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid path in rpath command", hdr.Path}
			}
			l.Path = cstring(cmddat[hdr.Path:])
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdCodeSignature:
			var hdr commands.CodeSignatureCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(CodeSignature)
			l.Offset = hdr.Offset
			l.Size = hdr.Size
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdSegmentSplitInfo:
			var hdr commands.SegmentSplitInfoCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(SplitInfo)
			l.Offset = hdr.Offset
			l.Size = hdr.Size
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdReexportDylib:
			var hdr commands.ReExportDylibCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(ReExportDylib)
			if hdr.Name >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid name in dynamic library command", hdr.Name}
			}
			l.Name = cstring(cmddat[hdr.Name:])
			l.Time = hdr.Time
			l.CurrentVersion = hdr.CurrentVersion.String()
			l.CompatVersion = hdr.CompatVersion.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdLazyLoadDylib:
		// case commands.LoadCmdEncryptionInfo:
		case commands.LoadCmdDyldInfo:
		case commands.LoadCmdDyldInfoOnly:
			var info commands.DyldInfoCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &info); err != nil {
				return nil, err
			}
			l := new(DyldInfo)
			l.RebaseOff = info.RebaseOff
			l.RebaseSize = info.RebaseSize
			l.BindOff = info.BindOff
			l.BindSize = info.BindSize
			l.WeakBindOff = info.WeakBindOff
			l.WeakBindSize = info.WeakBindSize
			l.LazyBindOff = info.LazyBindOff
			l.LazyBindSize = info.LazyBindSize
			l.ExportOff = info.ExportOff
			l.ExportSize = info.ExportSize
			f.Loads[i] = l
		case commands.LoadCmdLoadUpwardDylib:
			var hdr commands.UpwardDylibCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &hdr); err != nil {
				return nil, err
			}
			l := new(UpwardDylib)
			if hdr.Name >= uint32(len(cmddat)) {
				return nil, &FormatError{offset, "invalid name in load upwardl dylib command", hdr.Name}
			}
			l.Name = cstring(cmddat[hdr.Name:])
			l.Time = hdr.Time
			l.CurrentVersion = hdr.CurrentVersion.String()
			l.CompatVersion = hdr.CompatVersion.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdVersionMinMacosx:
			var verMin commands.VersionMinMacOSCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &verMin); err != nil {
				return nil, err
			}
			l := new(VersionMinMacosx)
			l.Version = verMin.Version.String()
			l.Sdk = verMin.Sdk.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdVersionMinIphoneos:
			var verMin commands.VersionMinIPhoneOSCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &verMin); err != nil {
				return nil, err
			}
			l := new(VersionMinIphoneos)
			l.Version = verMin.Version.String()
			l.Sdk = verMin.Sdk.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdFunctionStarts:
			var led commands.LinkEditDataCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &led); err != nil {
				return nil, err
			}
			l := new(FunctionStarts)
			l.Offset = led.Offset
			l.Size = led.Size
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdDyldEnvironment:
		// case commands.LoadCmdMain:
		case commands.LoadCmdDataInCode:
			var led commands.LinkEditDataCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &led); err != nil {
				return nil, err
			}
			l := new(DataInCode)
			// var e DataInCodeEntry

			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		case commands.LoadCmdSourceVersion:
			var sv commands.SourceVersionCmd
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &sv); err != nil {
				return nil, err
			}
			l := new(SourceVersion)
			l.Version = sv.Version.String()
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
		// case commands.LoadCmdDylibCodeSignDrs:
		// case commands.LoadCmdEncryptionInfo64:
		// case commands.LoadCmdLinkerOption:
		// case commands.LoadCmdLinkerOptimizationHint:
		// case commands.LoadCmdVersionMinTvos:
		// case commands.LoadCmdVersionMinWatchos:
		// case commands.LoadCmdNote:
		case commands.LoadCmdBuildVersion:
			var build commands.BuildVersionCmd
			var buildTool types.BuildToolVersion
			b := bytes.NewReader(cmddat)
			if err := binary.Read(b, bo, &build); err != nil {
				return nil, err
			}
			l := new(BuildVersion)
			l.Platform = build.Platform.String()
			l.Minos = build.Minos.String()
			l.Sdk = build.Sdk.String()
			l.NumTools = build.NumTools
			if build.NumTools > 0 {
				if err := binary.Read(b, bo, &buildTool); err != nil {
					return nil, err
				}
				l.Tool = buildTool.Tool.String()
				l.ToolVersion = buildTool.Version.String()
			}
			l.LoadBytes = LoadBytes(cmddat)
			f.Loads[i] = l
			// case commands.LoadCmdDyldExportsTrie:
			// case commands.LoadCmdDyldChainedFixups:
		}
		if s != nil {
			s.sr = io.NewSectionReader(r, int64(s.Offset), int64(s.Filesz))
			s.ReaderAt = s.sr
		}
	}
	return f, nil
}

func (f *File) parseSymtab(symdat, strtab, cmddat []byte, hdr *commands.SymtabCmd, offset int64) (*Symtab, error) {
	bo := f.ByteOrder
	symtab := make([]Symbol, hdr.Nsyms)
	b := bytes.NewReader(symdat)
	for i := range symtab {
		var n types.Nlist64
		if f.Magic == header.Magic64 {
			if err := binary.Read(b, bo, &n); err != nil {
				return nil, err
			}
		} else {
			var n32 types.Nlist32
			if err := binary.Read(b, bo, &n32); err != nil {
				return nil, err
			}
			n.Name = n32.Name
			n.Type = n32.Type
			n.Sect = n32.Sect
			n.Desc = n32.Desc
			n.Value = uint64(n32.Value)
		}
		sym := &symtab[i]
		if n.Name >= uint32(len(strtab)) {
			continue // TODO: remove this
			return nil, &FormatError{offset, "invalid name in symbol table", n.Name}
		}
		// We add "_" to Go symbols. Strip it here. See issue 33808.
		name := cstring(strtab[n.Name:])
		if strings.Contains(name, ".") && name[0] == '_' {
			name = name[1:]
		}
		sym.Name = name
		sym.Type = n.Type
		sym.Sect = n.Sect
		sym.Desc = n.Desc
		sym.Value = n.Value
	}
	st := new(Symtab)
	st.LoadBytes = LoadBytes(cmddat)
	st.Syms = symtab
	return st, nil
}

type relocInfo struct {
	Addr   uint32
	Symnum uint32
}

func (f *File) pushSection(sh *Section, r io.ReaderAt) error {
	f.Sections = append(f.Sections, sh)
	sh.sr = io.NewSectionReader(r, int64(sh.Offset), int64(sh.Size))
	sh.ReaderAt = sh.sr

	if sh.Nreloc > 0 {
		reldat := make([]byte, int(sh.Nreloc)*8)
		if _, err := r.ReadAt(reldat, int64(sh.Reloff)); err != nil {
			return err
		}
		b := bytes.NewReader(reldat)

		bo := f.ByteOrder

		sh.Relocs = make([]Reloc, sh.Nreloc)
		for i := range sh.Relocs {
			rel := &sh.Relocs[i]

			var ri relocInfo
			if err := binary.Read(b, bo, &ri); err != nil {
				return err
			}

			if ri.Addr&(1<<31) != 0 { // scattered
				rel.Addr = ri.Addr & (1<<24 - 1)
				rel.Type = uint8((ri.Addr >> 24) & (1<<4 - 1))
				rel.Len = uint8((ri.Addr >> 28) & (1<<2 - 1))
				rel.Pcrel = ri.Addr&(1<<30) != 0
				rel.Value = ri.Symnum
				rel.Scattered = true
			} else {
				switch bo {
				case binary.LittleEndian:
					rel.Addr = ri.Addr
					rel.Value = ri.Symnum & (1<<24 - 1)
					rel.Pcrel = ri.Symnum&(1<<24) != 0
					rel.Len = uint8((ri.Symnum >> 25) & (1<<2 - 1))
					rel.Extern = ri.Symnum&(1<<27) != 0
					rel.Type = uint8((ri.Symnum >> 28) & (1<<4 - 1))
				case binary.BigEndian:
					rel.Addr = ri.Addr
					rel.Value = ri.Symnum >> 8
					rel.Pcrel = ri.Symnum&(1<<7) != 0
					rel.Len = uint8((ri.Symnum >> 5) & (1<<2 - 1))
					rel.Extern = ri.Symnum&(1<<4) != 0
					rel.Type = uint8(ri.Symnum & (1<<4 - 1))
				default:
					panic("unreachable")
				}
			}
		}
	}

	return nil
}

func cstring(b []byte) string {
	i := bytes.IndexByte(b, 0)
	if i == -1 {
		i = len(b)
	}
	return string(b[0:i])
}

// Segment returns the first Segment with the given name, or nil if no such segment exists.
func (f *File) Segment(name string) *Segment {
	for _, l := range f.Loads {
		if s, ok := l.(*Segment); ok && s.Name == name {
			return s
		}
	}
	return nil
}

// Segments returns all Segments.
func (f *File) Segments() []*Segment {
	var segs []*Segment
	for _, l := range f.Loads {
		if s, ok := l.(*Segment); ok {
			segs = append(segs, s)
		}
	}
	return segs
}

// Section returns the first section with the given name, or nil if no such
// section exists.
func (f *File) Section(name string) *Section {
	for _, s := range f.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// UUID returns the UUID load command, or nil if no UUID exists.
func (f *File) UUID() *UUID {
	for _, l := range f.Loads {
		if u, ok := l.(*UUID); ok {
			return u
		}
	}
	return nil
}

// DylibID returns the dylib ID load command, or nil if no dylib ID exists.
func (f *File) DylibID() *DylibID {
	for _, l := range f.Loads {
		if s, ok := l.(*DylibID); ok {
			return s
		}
	}
	return nil
}

// DyldInfo returns the dyld info load command, or nil if no dyld info exists.
func (f *File) DyldInfo() *DyldInfo {
	for _, l := range f.Loads {
		if s, ok := l.(*DyldInfo); ok {
			return s
		}
	}
	return nil
}

// SourceVersion returns the source version load command, or nil if no source version exists.
func (f *File) SourceVersion() *SourceVersion {
	for _, l := range f.Loads {
		if s, ok := l.(*SourceVersion); ok {
			return s
		}
	}
	return nil
}

// BuildVersion returns the build version load command, or nil if no build version exists.
func (f *File) BuildVersion() *BuildVersion {
	for _, l := range f.Loads {
		if s, ok := l.(*BuildVersion); ok {
			return s
		}
	}
	return nil
}

// DWARF returns the DWARF debug information for the Mach-O file.
func (f *File) DWARF() (*dwarf.Data, error) {
	dwarfSuffix := func(s *Section) string {
		switch {
		case strings.HasPrefix(s.Name, "__debug_"):
			return s.Name[8:]
		case strings.HasPrefix(s.Name, "__zdebug_"):
			return s.Name[9:]
		default:
			return ""
		}

	}
	sectionData := func(s *Section) ([]byte, error) {
		b, err := s.Data()
		if err != nil && uint64(len(b)) < s.Size {
			return nil, err
		}

		if len(b) >= 12 && string(b[:4]) == "ZLIB" {
			dlen := binary.BigEndian.Uint64(b[4:12])
			dbuf := make([]byte, dlen)
			r, err := zlib.NewReader(bytes.NewBuffer(b[12:]))
			if err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, dbuf); err != nil {
				return nil, err
			}
			if err := r.Close(); err != nil {
				return nil, err
			}
			b = dbuf
		}
		return b, nil
	}

	// There are many other DWARF sections, but these
	// are the ones the debug/dwarf package uses.
	// Don't bother loading others.
	var dat = map[string][]byte{"abbrev": nil, "info": nil, "str": nil, "line": nil, "ranges": nil}
	for _, s := range f.Sections {
		suffix := dwarfSuffix(s)
		if suffix == "" {
			continue
		}
		if _, ok := dat[suffix]; !ok {
			continue
		}
		b, err := sectionData(s)
		if err != nil {
			return nil, err
		}
		dat[suffix] = b
	}

	d, err := dwarf.New(dat["abbrev"], nil, nil, dat["info"], dat["line"], nil, dat["ranges"], dat["str"])
	if err != nil {
		return nil, err
	}

	// Look for DWARF4 .debug_types sections.
	for i, s := range f.Sections {
		suffix := dwarfSuffix(s)
		if suffix != "types" {
			continue
		}

		b, err := sectionData(s)
		if err != nil {
			return nil, err
		}

		err = d.AddTypes(fmt.Sprintf("types-%d", i), b)
		if err != nil {
			return nil, err
		}
	}

	return d, nil
}

// ImportedSymbols returns the names of all symbols
// referred to by the binary f that are expected to be
// satisfied by other libraries at dynamic load time.
func (f *File) ImportedSymbols() ([]string, error) {
	if f.Dysymtab == nil || f.Symtab == nil {
		return nil, &FormatError{0, "missing symbol table", nil}
	}

	st := f.Symtab
	dt := f.Dysymtab
	var all []string
	for _, s := range st.Syms[dt.Iundefsym : dt.Iundefsym+dt.Nundefsym] {
		all = append(all, s.Name)
	}
	return all, nil
}

// ImportedLibraries returns the paths of all libraries
// referred to by the binary f that are expected to be
// linked with the binary at dynamic link time.
func (f *File) ImportedLibraries() ([]string, error) {
	var all []string
	for _, l := range f.Loads {
		if lib, ok := l.(*Dylib); ok {
			all = append(all, lib.Name)
		}
	}
	return all, nil
}

func (f *File) FindSymbolAddress(symbol string) (uint64, error) {
	for _, sym := range f.Symtab.Syms {
		if strings.EqualFold(sym.Name, symbol) {
			return sym.Value, nil
		}
	}
	return 0, fmt.Errorf("symbol not found in macho symtab")
}

func (f *File) FindAddressSymbol(addr uint64) (string, error) {
	for _, sym := range f.Symtab.Syms {
		if sym.Value == addr {
			return sym.Name, nil
		}
	}
	return "", fmt.Errorf("symbol not found in macho symtab")
}
