// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// shortObject is a plumbing.EncodedObject whose declared Size overstates
// what its Reader actually yields — the shape a truncated or corrupt
// object store presents. readBlob sizes its buffer from Size, so this is
// what drives io.ReadFull to fail.
type shortObject struct {
	content []byte
	size    int64
}

func (o *shortObject) Hash() plumbing.Hash         { return plumbing.ZeroHash }
func (o *shortObject) Type() plumbing.ObjectType   { return plumbing.BlobObject }
func (o *shortObject) SetType(plumbing.ObjectType) {}
func (o *shortObject) Size() int64                 { return o.size }
func (o *shortObject) SetSize(int64)               {}

func (o *shortObject) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.content)), nil
}

func (o *shortObject) Writer() (io.WriteCloser, error) {
	return nil, errors.New("read-only test object")
}

// readBlob promotes a Close error only when the read itself succeeded, so
// that a genuine read failure is never masked. The deferred Close here
// returns nil, so getting that condition backwards would replace the read
// error with nil and hand back a silently truncated flow — exactly the
// failure the function's comment says it exists to prevent.
func TestReadBlob_ShortReadIsNotSwallowed(t *testing.T) {
	blob, err := object.DecodeBlob(&shortObject{content: []byte("abc"), size: 64})
	if err != nil {
		t.Fatalf("DecodeBlob: %v", err)
	}
	f := object.NewFile("flow.yaml", filemode.Regular, blob)

	data, err := readBlob(f)
	if err == nil {
		t.Fatalf("readBlob returned (%q, nil) for a truncated blob: the read error was swallowed", data)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}
