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
	"sync"
	"syscall"

	//"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/fd"
	"gvisor.googlesource.com/gvisor/pkg/secio"
	"gvisor.googlesource.com/gvisor/pkg/sentry/context"
	//"gvisor.googlesource.com/gvisor/pkg/sentry/device"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs/fsutil"
	"gvisor.googlesource.com/gvisor/pkg/sentry/memmap"
	"gvisor.googlesource.com/gvisor/pkg/sentry/safemem"
	"gvisor.googlesource.com/gvisor/pkg/sentry/socket/unix/transport"
	"gvisor.googlesource.com/gvisor/pkg/syserror"
	"gvisor.googlesource.com/gvisor/pkg/waiter"
)

type ImgReader struct {
	mapArea []bytes
	offsetBegin int64
	offsetEnd int64
	currentOffset int64
}

var _ io.Reader = (*ImgReader)(nil)
var _ io.ReaderAt = (*ImgReader)(nil)

func NewImgReader(mapArea []bytes, offsetBegin int64, offsetEnd int64) *ImgReader {
	return &ImgReader{mapArea, offsetBegin, offsetEnd, int64(0)}
}

func (r *ImgReader) Read(b []byte) (int, error) {
	n, err := ReadAt(b, r.currentOffset)
	r.currentOffset += n
	return n, err
}

func (r *ImgReader) ReadAt(b []byte, off int64) (c int, err error) {
	if off >= r.offsetEnd || off < 0 {
		return 0, io.EOF
	}
	sz := cap(b)
	copyEnd := off + sz
	if copyEnd > r.offsetEnd {
		copyEnd = r.offsetEnd
	}
	n := copyEnd - off
	if n > 0 {
		copy(mapArea[off : copyEnd], b)
	}
	return n, nil
}
// inodeOperations implements fs.InodeOperations for an fs.Inodes backed
// by a host file descriptor.
//
// +stateify savable
type inodeOperations struct {
	fsutil.InodeNotVirtual           `state:"nosave"`
	fsutil.InodeNoExtendedAttributes `state:"nosave"`

	// fileState implements fs.CachedFileObject. It exists
	// to break a circular load dependency between inodeOperations
	// and cachingInodeOps (below).
	fileState *inodeFileState `state:"wait"`

	// cachedInodeOps implements memmap.Mappable.
	cachingInodeOps *fsutil.CachingInodeOperations

	// readdirMu protects the file offset on the host FD. This is needed
	// for readdir because getdents must use the kernel offset, so
	// concurrent readdirs must be exclusive.
	//
	// All read/write functions pass the offset directly to the kernel and
	// thus don't need a lock.
	readdirMu sync.Mutex `state:"nosave"`
}

// inodeFileState implements fs.CachedFileObject and otherwise fully
// encapsulates state that needs to be manually loaded on restore for
// this file object.
//
// This unfortunate structure exists because fs.CachingInodeOperations
// defines afterLoad and therefore cannot be lazily loaded (to break a
// circular load dependency between it and inodeOperations). Even with
// lazy loading, this approach defines the dependencies between objects
// and the expected load behavior more concretely.
//
// +stateify savable
type inodeFileState struct {
	// Common file system state.
	mops *superOperations `state:"wait"`

	// descriptor is the backing host FD.
	descriptor *descriptor `state:"wait"`

	// Event queue for blocking operations.
	queue waiter.Queue `state:"zerovalue"`

	// sattr is used to restore the inodeOperations.
	sattr fs.StableAttr `state:"wait"`

	// savedUAttr is only allocated during S/R. It points to the save-time
	// unstable attributes and is used to validate restore-time ones.
	//
	// Note that these unstable attributes are only used to detect cross-S/R
	// external file system metadata changes. They may differ from the
	// cached unstable attributes in cachingInodeOps, as that might differ
	// from the external file system attributes if there had been WriteOut
	// failures. S/R is transparent to Sentry and the latter will continue
	// using its cached values after restore.
	savedUAttr *fs.UnstableAttr

	children map[string]*fs.Inode

	reader ImgReader
}

// ReadToBlocksAt implements fsutil.CachedFileObject.ReadToBlocksAt.
func (i *inodeFileState) ReadToBlocksAt(ctx context.Context, dsts safemem.BlockSeq, offset uint64) (uint64, error) {
	// TODO: Using safemem.FromIOReader here is wasteful for two
	// reasons:
	//
	// - Using preadv instead of iterated preads saves on host system calls.
	//
	// - Host system calls can handle destination memory that would fault in
	// gr3 (i.e. they can accept safemem.Blocks with NeedSafecopy() == true),
	// so the buffering performed by FromIOReader is unnecessary.
	//
	// This also applies to the write path below.
	return safemem.FromIOReader{secio.NewOffsetReader(i.reader, int64(offset))}.ReadToBlocks(dsts)
}

// WriteFromBlocksAt implements fsutil.CachedFileObject.WriteFromBlocksAt.
func (i *inodeFileState) WriteFromBlocksAt(ctx context.Context, srcs safemem.BlockSeq, offset uint64) (uint64, error) {
	return 0, syserror.EPERM
}

// SetMaskedAttributes implements fsutil.CachedFileObject.SetMaskedAttributes.
func (i *inodeFileState) SetMaskedAttributes(ctx context.Context, mask fs.AttrMask, attr fs.UnstableAttr) error {
	if mask.Empty() {
		return nil
	}
	if mask.UID || mask.GID {
		return syserror.EPERM
	}
	if mask.Perms {
		if err := syscall.Fchmod(i.FD(), uint32(attr.Perms.LinuxMode())); err != nil {
			return err
		}
	}
	if mask.Size {
		if err := syscall.Ftruncate(i.FD(), attr.Size); err != nil {
			return err
		}
	}
	if mask.AccessTime || mask.ModificationTime {
		ts := fs.TimeSpec{
			ATime:     attr.AccessTime,
			ATimeOmit: !mask.AccessTime,
			MTime:     attr.ModificationTime,
			MTimeOmit: !mask.ModificationTime,
		}
		if err := setTimestamps(i.FD(), ts); err != nil {
			return err
		}
	}
	return nil
}

// Sync implements fsutil.CachedFileObject.Sync.
func (i *inodeFileState) Sync(ctx context.Context) error {
	return syscall.Fsync(i.FD())
}

// FD implements fsutil.CachedFileObject.FD.
func (i *inodeFileState) FD() int {
	return i.descriptor.value
}

func (i *inodeFileState) unstableAttr(ctx context.Context) (fs.UnstableAttr, error) {
	var s syscall.Stat_t
	if err := syscall.Fstat(i.FD(), &s); err != nil {
		return fs.UnstableAttr{}, err
	}
	return unstableAttr(i.mops, &s), nil
}

// inodeOperations implements fs.InodeOperations.
var _ fs.InodeOperations = (*inodeOperations)(nil)

// newInode returns a new fs.Inode backed by the host FD.
func newInode(ctx context.Context, msrc *fs.MountSource, fd int, saveable bool, donated bool) (*fs.Inode, error) {
	// Retrieve metadata.
	var s syscall.Stat_t
	err := syscall.Fstat(fd, &s)
	if err != nil {
		return nil, err
	}

	fileState := &inodeFileState{
		mops:  msrc.MountSourceOperations.(*superOperations),
		sattr: stableAttr(&s),
	}

	// Initialize the wrapped host file descriptor.
	fileState.descriptor, err = newDescriptor(
		fd,
		donated,
		saveable,
		wouldBlock(&s),
		&fileState.queue,
	)
	if err != nil {
		return nil, err
	}

	// Build the fs.InodeOperations.
	uattr := unstableAttr(msrc.MountSourceOperations.(*superOperations), &s)
	iops := &inodeOperations{
		fileState:       fileState,
		cachingInodeOps: fsutil.NewCachingInodeOperations(ctx, fileState, uattr, msrc.Flags.ForcePageCache),
	}

	// Return the fs.Inode.
	return fs.NewInode(iops, msrc, fileState.sattr), nil
}

// Mappable implements fs.InodeOperations.Mappable.
func (i *inodeOperations) Mappable(inode *fs.Inode) memmap.Mappable {
	if !canMap(inode) {
		return nil
	}
	return i.cachingInodeOps
}

// ReturnsWouldBlock returns true if this host FD can return EWOULDBLOCK for
// operations that would block.
func (i *inodeOperations) ReturnsWouldBlock() bool {
	return i.fileState.descriptor.wouldBlock
}

// Release implements fs.InodeOperations.Release.
func (i *inodeOperations) Release(context.Context) {
	i.fileState.descriptor.Release()
	i.cachingInodeOps.Release()
}

// Lookup implements fs.InodeOperations.Lookup.
func (i *inodeOperations) Lookup(ctx context.Context, dir *fs.Inode, name string) (*fs.Dirent, error) {
	if inode, ok := i.chilren[p]; ok {
		return inode, nil
	}
	return nil, syserror.ENOENT
}

// Create implements fs.InodeOperations.Create.
func (i *inodeOperations) Create(ctx context.Context, dir *fs.Inode, name string, flags fs.FileFlags, perm fs.FilePermissions) (*fs.File, error) {
	return nil, syserror.EPERM
}

// CreateDirectory implements fs.InodeOperations.CreateDirectory.
func (i *inodeOperations) CreateDirectory(ctx context.Context, dir *fs.Inode, name string, perm fs.FilePermissions) error {
	return syserror.EPERM
}

// CreateLink implements fs.InodeOperations.CreateLink.
func (i *inodeOperations) CreateLink(ctx context.Context, dir *fs.Inode, oldname string, newname string) error {
	return syserror.EPERM
}

// CreateHardLink implements fs.InodeOperations.CreateHardLink.
func (*inodeOperations) CreateHardLink(context.Context, *fs.Inode, *fs.Inode, string) error {
	return syserror.EPERM
}

// CreateFifo implements fs.InodeOperations.CreateFifo.
func (*inodeOperations) CreateFifo(context.Context, *fs.Inode, string, fs.FilePermissions) error {
	return syserror.EOPNOTSUPP
}

// Remove implements fs.InodeOperations.Remove.
func (i *inodeOperations) Remove(ctx context.Context, dir *fs.Inode, name string) error {
	return syserror.EPERM
}

// RemoveDirectory implements fs.InodeOperations.RemoveDirectory.
func (i *inodeOperations) RemoveDirectory(ctx context.Context, dir *fs.Inode, name string) error {
	return syserror.EPERM
}

// Rename implements fs.InodeOperations.Rename.
func (i *inodeOperations) Rename(ctx context.Context, oldParent *fs.Inode, oldName string, newParent *fs.Inode, newName string, replacement bool) error {
	return syserror.EPERM
}

// Bind implements fs.InodeOperations.Bind.
func (i *inodeOperations) Bind(ctx context.Context, dir *fs.Inode, name string, data transport.BoundEndpoint, perm fs.FilePermissions) (*fs.Dirent, error) {
	return nil, syserror.EOPNOTSUPP
}

// BoundEndpoint implements fs.InodeOperations.BoundEndpoint.
func (i *inodeOperations) BoundEndpoint(inode *fs.Inode, path string) transport.BoundEndpoint {
	return nil
}

// GetFile implements fs.InodeOperations.GetFile.
func (i *inodeOperations) GetFile(ctx context.Context, d *fs.Dirent, flags fs.FileFlags) (*fs.File, error) {
	return newFile(ctx, d, flags, i), nil
}

// canMap returns true if this fs.Inode can be memory mapped.
func canMap(inode *fs.Inode) bool {
	// FIXME: Some obscure character devices can be mapped.
	return fs.IsFile(inode.StableAttr)
}

// UnstableAttr implements fs.InodeOperations.UnstableAttr.
func (i *inodeOperations) UnstableAttr(ctx context.Context, inode *fs.Inode) (fs.UnstableAttr, error) {
	// When the kernel supports mapping host FDs, we do so to take
	// advantage of the host page cache. We forego updating fs.Inodes
	// because the host manages consistency of its own inode structures.
	//
	// For fs.Inodes that can never be mapped we take advantage of
	// synchronizing metadata updates through host caches.
	//
	// So can we use host kernel metadata caches?
	if !inode.MountSource.Flags.ForcePageCache || !canMap(inode) {
		// Then just obtain the attributes.
		return i.fileState.unstableAttr(ctx)
	}
	// No, we're maintaining consistency of metadata ourselves.
	return i.cachingInodeOps.UnstableAttr(ctx, inode)
}

// Check implements fs.InodeOperations.Check.
func (i *inodeOperations) Check(ctx context.Context, inode *fs.Inode, p fs.PermMask) bool {
	return fs.ContextCanAccessFile(ctx, inode, p)
}

// SetOwner implements fs.InodeOperations.SetOwner.
func (i *inodeOperations) SetOwner(context.Context, *fs.Inode, fs.FileOwner) error {
	return syserror.EPERM
}

// SetPermissions implements fs.InodeOperations.SetPermissions.
func (i *inodeOperations) SetPermissions(ctx context.Context, inode *fs.Inode, f fs.FilePermissions) bool {
	// Can we use host kernel metadata caches?
	if !inode.MountSource.Flags.ForcePageCache || !canMap(inode) {
		// Then just change the timestamps on the FD, the host
		// will synchronize the metadata update with any host
		// inode and page cache.
		return syscall.Fchmod(i.fileState.FD(), uint32(f.LinuxMode())) == nil
	}
	// Otherwise update our cached metadata.
	return i.cachingInodeOps.SetPermissions(ctx, inode, f)
}

// SetTimestamps implements fs.InodeOperations.SetTimestamps.
func (i *inodeOperations) SetTimestamps(ctx context.Context, inode *fs.Inode, ts fs.TimeSpec) error {
	return nil
}

// Truncate implements fs.InodeOperations.Truncate.
func (i *inodeOperations) Truncate(ctx context.Context, inode *fs.Inode, size int64) error {
	return syserror.EPERM
}

// WriteOut implements fs.InodeOperations.WriteOut.
func (i *inodeOperations) WriteOut(ctx context.Context, inode *fs.Inode) error {
	return syserror.EPERM
}

// Readlink implements fs.InodeOperations.Readlink.
func (i *inodeOperations) Readlink(ctx context.Context, inode *fs.Inode) (string, error) {
	return readLink(i.fileState.FD())
}

// Getlink implements fs.InodeOperations.Getlink.
func (i *inodeOperations) Getlink(context.Context, *fs.Inode) (*fs.Dirent, error) {
	if !fs.IsSymlink(i.fileState.sattr) {
		return nil, syserror.ENOLINK
	}
	return nil, fs.ErrResolveViaReadlink
}

// StatFS implements fs.InodeOperations.StatFS.
func (i *inodeOperations) StatFS(context.Context) (fs.Info, error) {
	return fs.Info{}, syserror.ENOSYS
}

// AddLink implements fs.InodeOperations.AddLink.
// FIXME: Remove this from InodeOperations altogether.
func (i *inodeOperations) AddLink() {}

// DropLink implements fs.InodeOperations.DropLink.
// FIXME: Remove this from InodeOperations altogether.
func (i *inodeOperations) DropLink() {}

// NotifyStatusChange implements fs.InodeOperations.NotifyStatusChange.
// FIXME: Remove this from InodeOperations altogether.
func (i *inodeOperations) NotifyStatusChange(ctx context.Context) {}
