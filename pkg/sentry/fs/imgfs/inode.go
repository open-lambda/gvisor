// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package imgfs

import (
	"fmt"
	"io"
	"sync"
	"time"

	"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/metric"
	"gvisor.googlesource.com/gvisor/pkg/sentry/context"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs/fsutil"
	"gvisor.googlesource.com/gvisor/pkg/sentry/kernel"
	ktime "gvisor.googlesource.com/gvisor/pkg/sentry/kernel/time"
	"gvisor.googlesource.com/gvisor/pkg/sentry/memmap"
	"gvisor.googlesource.com/gvisor/pkg/sentry/safemem"
	"gvisor.googlesource.com/gvisor/pkg/sentry/usage"
	"gvisor.googlesource.com/gvisor/pkg/sentry/usermem"
	"gvisor.googlesource.com/gvisor/pkg/syserror"
)
// inodeOperations implements fs.InodeOperations for an fs.Inodes backed
// by a host file descriptor.
//
// +stateify savable
type fileInodeOperations struct {
	fsutil.InodeGenericChecker `state:"nosave"`
	fsutil.InodeNoopWriteOut   `state:"nosave"`
	fsutil.InodeNotDirectory   `state:"nosave"`
	fsutil.InodeNotSocket      `state:"nosave"`
	fsutil.InodeNotSymlink     `state:"nosave"`

	fsutil.InodeSimpleExtendedAttributes

	mapArea []byte
	offsetBegin int64
	offsetEnd int64
}

type ImgReader struct {
	f *fileInodeOperations
	offset int64
}

func NewImgReader(f *fileInodeOperations, offset int64) *ImgReader {
	return &ImgReader{f, offset}
}

func (r ImgReader) ReadToBlocks(dsts safemem.BlockSeq) (uint64, error) {
	if r.offset >= r.f.attr.Size {
		return 0, io.EOF
	}
	end := fs.ReadEndOffset(r.offset, int64(dsts.NumBytes()), r.f.attr.Size)
	if end == r.offset {
		return 0, nil
	}
	src := safemem.BlockSeqOf(safemem.BlockFromSafeSlice([r.f.offsetBegin + r.offset:r.f.offsetEnd]))
	n, err := safemem.CopySeq(dsts, ims)
	return n, err
}

func (f *fileInodeOperations) Release(context.Context) {}

// Mappable implements fs.InodeOperations.Mappable.
func (f *fileInodeOperations) Mappable(*fs.Inode) memmap.Mappable {
	return f
}

// Rename implements fs.InodeOperations.Rename.
func (*fileInodeOperations) Rename(ctx context.Context, oldParent *fs.Inode, oldName string, newParent *fs.Inode, newName string, replacement bool) error {
	return syserror.EPERM
}

// GetFile implements fs.InodeOperations.GetFile.
func (f *fileInodeOperations) GetFile(ctx context.Context, d *fs.Dirent, flags fs.FileFlags) (*fs.File, error) {
	flags.Pread = true
	flags.Pwrite = true
	return fs.NewFile(ctx, d, flags, &regularFileOperations{iops: f}), nil
}

// UnstableAttr returns unstable attributes of this tmpfs file.
// TODO: fix this
func (f *fileInodeOperations) UnstableAttr(ctx context.Context, inode *fs.Inode) (fs.UnstableAttr, error) {
	f.attrMu.Lock()
	f.dataMu.RLock()
	attr := f.attr
	attr.Usage = int64(f.data.Span())
	f.dataMu.RUnlock()
	f.attrMu.Unlock()
	return attr, nil
}

// Check implements fs.InodeOperations.Check.
func (f *fileInodeOperations) Check(ctx context.Context, inode *fs.Inode, p fs.PermMask) bool {
	return fs.ContextCanAccessFile(ctx, inode, p)
}

// SetPermissions implements fs.InodeOperations.SetPermissions.
func (f *fileInodeOperations) SetPermissions(ctx context.Context, _ *fs.Inode, p fs.FilePermissions) bool {
	return false
}

// SetTimestamps implements fs.InodeOperations.SetTimestamps.
func (f *fileInodeOperations) SetTimestamps(ctx context.Context, _ *fs.Inode, ts fs.TimeSpec) error {
	return syserror.EPERM
}

// SetOwner implements fs.InodeOperations.SetOwner.
func (f *fileInodeOperations) SetOwner(ctx context.Context, _ *fs.Inode, owner fs.FileOwner) error {
	return syserror.EPERM
}

func (f *fileInodeOperations) Truncate(ctx context.Context, _ *fs.Inode, size int64) error {
	return syserror.EPERM
}

// AddLink implements fs.InodeOperations.AddLink.
func (f *fileInodeOperations) AddLink() {}

// DropLink implements fs.InodeOperations.DropLink.
func (f *fileInodeOperations) DropLink() {}

// NotifyStatusChange implements fs.InodeOperations.NotifyStatusChange.
func (f *fileInodeOperations) NotifyStatusChange(ctx context.Context) {}

// IsVirtual implements fs.InodeOperations.IsVirtual.
func (*fileInodeOperations) IsVirtual() bool {
	return true
}

// StatFS implements fs.InodeOperations.StatFS.
func (*fileInodeOperations) StatFS(context.Context) (fs.Info, error) {
	return fsInfo, nil
}

// TODO: write this
func (f *fileInodeOperations) read(ctx context.Context, file *fs.File, dst usermem.IOSequence, offset int64) (int64, error) {
	if dst.NumBytes() == 0 {
		return 0, nil
	}
	f.dataMu.RLock()
	size := f.attr.Size
	f.dataMu.RUnlock()

	if offset >= size {
		return 0, io.EOF
	}

	n, err := dst.CopyOutFrom(ctx, &fileReader{f, offset})
	return n, err
}

// AddMapping implements memmap.Mappable.AddMapping.
// TODO: add mapping support
func (f *fileInodeOperations) AddMapping(ctx context.Context, ms memmap.MappingSpace, ar usermem.AddrRange, offset uint64, writable bool) error {
	return syserror.EPERM
}

// RemoveMapping implements memmap.Mappable.RemoveMapping.
func (f *fileInodeOperations) RemoveMapping(ctx context.Context, ms memmap.MappingSpace, ar usermem.AddrRange, offset uint64, writable bool) {
	return syserror.EPERM
}

// CopyMapping implements memmap.Mappable.CopyMapping.
func (f *fileInodeOperations) CopyMapping(ctx context.Context, ms memmap.MappingSpace, srcAR, dstAR usermem.AddrRange, offset uint64, writable bool) error {
	return syserror.EPERM
}

// Translate implements memmap.Mappable.Translate.
func (f *fileInodeOperations) Translate(ctx context.Context, required, optional memmap.MappableRange, at usermem.AccessType) ([]memmap.Translation, error) {
	return nil, syserror.EPERM
}

// InvalidateUnsavable implements memmap.Mappable.InvalidateUnsavable.
func (f *fileInodeOperations) InvalidateUnsavable(ctx context.Context) error {
	return nil
}

// newInode returns a new fs.Inode
func newInode(ctx context.Context, msrc *fs.MountSource, begin int64, end int64, m []byte) (*fs.Inode, error) {
	sattr := stableAttr()
	uattr := unstableAttr(ctx, begin, end)
	iops := &fileInodeOperations{
		attr:     uattr,
		mapArea:	m,
		offsetBegin:	begin,
		offsetEnd:		end,
	}
	return fs.NewInode(iops, msrc, sattr), nil
}
