package trie

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"

	"github.com/blacktop/go-macho/types"
)

type Node struct {
	Offset uint64
	Data   []byte
}

type TrieExport struct {
	Name         string
	ReExport     string
	Flags        types.ExportFlag
	Other        uint64
	Address      uint64
	FoundInDylib string
}

func (e TrieExport) String() string {
	if e.Flags.ReExport() {
		if len(e.ReExport) == 0 {
			return fmt.Sprintf("%#09x:\t(from %s)\t%s", e.Address, filepath.Base(e.FoundInDylib), e.Name)
		} else {
			return fmt.Sprintf("%#09x:\t(%s re-exported from %s)\t%s", e.Address, e.ReExport, filepath.Base(e.FoundInDylib), e.Name)
		}
	} else if e.Flags.StubAndResolver() {
		return fmt.Sprintf("%#09x:\t(resolver=%#8x)\t%s", e.Address, e.Other, e.Name)
	} else if len(e.FoundInDylib) > 0 {
		return fmt.Sprintf("%#09x: %s\t%s", e.Address, e.Name, e.FoundInDylib)
	}
	return fmt.Sprintf("%#09x:\t(%s)\t%s", e.Address, e.Flags, e.Name)
}

func ReadUleb128(r *bytes.Reader) (uint64, error) {
	var result uint64
	var shift uint64

	for {
		b, err := r.ReadByte()
		if err == io.EOF {
			return 0, err
		}
		if err != nil {
			return 0, fmt.Errorf("could not parse ULEB128 value: %v", err)
		}

		result |= uint64((uint(b) & 0x7f) << shift)

		// If high order bit is 1.
		if (b & 0x80) == 0 {
			break
		}

		shift += 7
	}

	return result, nil
}

func ReadSleb128(r *bytes.Reader) (int64, error) {
	var result int64
	var shift uint64

	for {
		b, err := r.ReadByte()
		if err == io.EOF {
			return 0, err
		}
		if err != nil {
			return 0, fmt.Errorf("could not parse SLEB128 value: %v", err)
		}

		result |= int64((int64(b) & 0x7f) << shift)
		shift += 7

		// If high order bit is 1.
		if (b & 0x80) == 0 {
			break
		}

		if (shift < 64) && ((b & 0x40) > 0) {
			result |= -(1 << shift)
		}
	}

	return result, nil
}

func ReadUleb128FromBuffer(buf *bytes.Buffer) (uint64, int, error) {

	var (
		result uint64
		shift  uint64
		length int
	)

	if buf.Len() == 0 {
		return 0, 0, nil
	}

	for {
		b, err := buf.ReadByte()
		if err != nil {
			return 0, 0, fmt.Errorf("could not parse ULEB128 value: %v", err)
		}
		length++

		result |= uint64((uint(b) & 0x7f) << shift)

		// If high order bit is 1.
		if (b & 0x80) == 0 {
			break
		}

		shift += 7
	}

	return result, length, nil
}

// EncodeUleb128 encodes input to the Unsigned Little Endian Base 128 format
func EncodeUleb128(out io.ByteWriter, x uint64) {
	for {
		b := byte(x & 0x7f)
		x = x >> 7
		if x != 0 {
			b = b | 0x80
		}
		out.WriteByte(b)
		if x == 0 {
			break
		}
	}
}

// EncodeSleb128 encodes input to the Signed Little Endian Base 128 format
func EncodeSleb128(out io.ByteWriter, x int64) {
	for {
		b := byte(x & 0x7f)
		x >>= 7

		signb := b & 0x40

		last := false
		if (x == 0 && signb == 0) || (x == -1 && signb != 0) {
			last = true
		} else {
			b = b | 0x80
		}
		out.WriteByte(b)

		if last {
			break
		}
	}
}

func ReadExport(r *bytes.Reader, symbol string, loadAddress uint64) (*TrieExport, error) {
	var symFlagInt, symValueInt, symOtherInt uint64
	var reExportSymBytes []byte
	var reExportSymName string

	symFlagInt, err := ReadUleb128(r)
	if err != nil {
		return nil, err
	}

	flags := types.ExportFlag(symFlagInt)

	if flags.ReExport() {
		symOtherInt, err = ReadUleb128(r)
		if err != nil {
			return nil, err
		}

		for {
			s, err := r.ReadByte()
			if err == io.EOF {
				break
			}
			if s == '\x00' {
				break
			}
			reExportSymBytes = append(reExportSymBytes, s)
		}

	} else if flags.StubAndResolver() {
		symOtherInt, err = ReadUleb128(r)
		if err != nil {
			return nil, err
		}
		symOtherInt += loadAddress
	}

	symValueInt, err = ReadUleb128(r)
	if err != nil {
		return nil, err
	}

	if (flags.Regular() || flags.ThreadLocal()) && !flags.ReExport() {
		symValueInt += loadAddress
	}

	if len(reExportSymBytes) > 0 {
		reExportSymName = string(reExportSymBytes)
	}

	return &TrieExport{
		Name:     symbol,
		ReExport: reExportSymName,
		Flags:    flags,
		Other:    symOtherInt,
		Address:  symValueInt,
	}, nil
}

func ParseTrieExports(r *bytes.Reader, loadAddress uint64) ([]TrieExport, error) {
	var exports []TrieExport

	nodes, err := ParseTrie(r)
	if err != nil {
		return nil, err
	}

	for _, node := range nodes {
		if _, err := r.Seek(int64(node.Offset), io.SeekStart); err != nil {
			return nil, err
		}
		export, err := ReadExport(r, string(node.Data), loadAddress)
		if err != nil {
			return nil, err
		}
		exports = append(exports, *export)
	}

	return exports, nil
}

func ParseTrie(r *bytes.Reader) ([]Node, error) {

	var tNode Node
	var outNodes []Node

	nodes := []Node{{
		Offset: 0,
		Data:   make([]byte, 0),
	}}

	for len(nodes) > 0 {
		tNode, nodes = nodes[len(nodes)-1], nodes[:len(nodes)-1]

		r.Seek(int64(tNode.Offset), io.SeekStart)

		terminalSize, err := ReadUleb128(r)
		if err != nil {
			return nil, err
		}

		if terminalSize != 0 {
			off, err := r.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			outNodes = append(outNodes, Node{
				Offset: uint64(off),
				Data:   tNode.Data,
			})
		}

		r.Seek(int64(tNode.Offset+terminalSize+1), io.SeekStart)

		childrenRemaining, err := r.ReadByte()
		if err == io.EOF {
			break
		}

		for i := 0; i < int(childrenRemaining); i++ {

			if len(tNode.Data) > 32768 {
				return nil, fmt.Errorf("possible malformed export trie: len(tNode.SymBytes)=%d > 32768", len(tNode.Data))
			}

			tmp := make([]byte, len(tNode.Data), 0x8000)
			copy(tmp, tNode.Data)

			for {
				s, err := r.ReadByte()
				if err == io.EOF {
					break
				}
				if s == '\x00' {
					break
				}
				tmp = append(tmp, s)
			}

			childNodeOffset, err := ReadUleb128(r)
			if err != nil {
				return nil, err
			}

			nodes = append(nodes, Node{
				Offset: childNodeOffset,
				Data:   tmp,
			})
		}

	}

	return outNodes, nil
}

func WalkTrie(r *bytes.Reader, symbol string) (uint64, error) {

	var strIndex int
	var offset, nodeOffset uint64

	for {
		r.Seek(int64(offset), io.SeekStart)

		terminalSize, err := binary.ReadUvarint(r)
		if err != nil {
			return 0, err
		}

		r.Seek(int64(offset+1), io.SeekStart)

		if terminalSize > 127 {
			r.Seek(int64(offset), io.SeekStart)

			terminalSize, err = ReadUleb128(r)
			if err != nil {
				return 0, err
			}
		}

		if int(strIndex) == len(symbol) && (terminalSize != 0) {
			// skip over zero terminator
			r.Seek(int64(offset+1), io.SeekStart)
			return offset + 1, nil
		}

		r.Seek(int64(offset+terminalSize+1), io.SeekStart)

		childrenRemaining, err := r.ReadByte()
		if err == io.EOF {
			break
		}

		nodeOffset = 0

		for i := childrenRemaining; i > 0; i-- {
			searchStrIndex := strIndex
			wrongEdge := false

			for {
				c, err := r.ReadByte()
				if err == io.EOF {
					break
				}
				if err != nil {
					return 0, err
				}
				if c == '\x00' {
					break
				}
				if !wrongEdge {
					if searchStrIndex != len(symbol) && c != symbol[searchStrIndex] {
						wrongEdge = true
					}
					searchStrIndex++
					if searchStrIndex > len(symbol) {
						return offset, fmt.Errorf("symbol not in trie")
					}
				}
			}

			if wrongEdge { // advance to next child
				// skip over last byte of uleb128
				_, err = ReadUleb128(r)
				if err != nil {
					return 0, err
				}
			} else { // the symbol so far matches this edge (child)
				// so advance to the child's node
				nodeOffset, err = ReadUleb128(r)
				if err != nil {
					return 0, err
				}

				strIndex = searchStrIndex
				break
			}
		}

		if nodeOffset != 0 {
			offset = nodeOffset
		} else {
			break
		}
	}

	return offset, fmt.Errorf("symbol not in trie")
}
