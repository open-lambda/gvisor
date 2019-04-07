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
	//"fmt"
	//"syscall"

	"gvisor.googlesource.com/gvisor/pkg/fd"
	"gvisor.googlesource.com/gvisor/pkg/fdnotifier"
	//"gvisor.googlesource.com/gvisor/pkg/log"
	"gvisor.googlesource.com/gvisor/pkg/secio"
	"gvisor.googlesource.com/gvisor/pkg/sentry/context"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs/fsutil"
	"gvisor.googlesource.com/gvisor/pkg/sentry/memmap"
	"gvisor.googlesource.com/gvisor/pkg/sentry/safemem"
	"gvisor.googlesource.com/gvisor/pkg/sentry/usermem"
	"gvisor.googlesource.com/gvisor/pkg/syserror"
	"gvisor.googlesource.com/gvisor/pkg/waiter"
)

// fileOperations implements fs.FileOperations for a host file descriptor.
//
// +stateify savable
type fileOperations struct {
	fsutil.FileNoIoctl     `state:"nosave"`
	fsutil.FileNoopRelease `state:"nosave"`

	// iops are the Inode operations for this file.
	iops *inodeOperations `state:"wait"`

	// a scratch buffer for reading directory entries.
	dirinfo *dirInfo `state:"nosave"`

	// dirCursor is the directory cursor.
	dirCursor string
}

/*
type imgFileOperations struct {
	waiter.AlwaysReady   `state:"nosave"`
	fsutil.NoopRelease   `state:"nosave"`
	fsutil.GenericSeek   `state:"nosave"`
	fsutil.NotDirReaddir `state:"nosave"`
	fsutil.NoopFsync     `state:"nosave"`
	fsutil.NoopFlush     `state:"nosave"`
	fsutil.NoIoctl       `state:"nosave"`

	// iops is the InodeOperations of a regular tmpfs file. It is
	// guaranteed to be the same as file.Dirent.Inode.InodeOperations,
	// see operations that take fs.File below.
	iops *fileInodeOperations
}
*/

// fileOperations implements fs.FileOperations.
var _ fs.FileOperations = (*fileOperations)(nil)

// NewFile creates a new File backed by the provided host file descriptor. If
// NewFile succeeds, ownership of the FD is transferred to the returned File.
//
// The returned File cannot be saved, since there is no guarantee that the same
// FD will exist or represent the same file at time of restore. If such a
// guarantee does exist, use ImportFile instead.
/*
func NewFile(ctx context.Context, fd int, mounter fs.FileOwner) (*fs.File, error) {
	return newFileFromDonatedFD(ctx, fd, mounter, false, false)
}

// newFileFromDonatedFD returns an fs.File from a donated FD. If the FD is
// saveable, then saveable is true.
func newFileFromDonatedFD(ctx context.Context, donated int, mounter fs.FileOwner, saveable, isTTY bool) (*fs.File, error) {
	var s syscall.Stat_t
	if err := syscall.Fstat(donated, &s); err != nil {
		return nil, err
	}
	flags, err := fileFlagsFromDonatedFD(donated)
	if err != nil {
		return nil, err
	}

	msrc := newMountSource(ctx, "/", mounter, &Filesystem{}, fs.MountSourceFlags{}, false)
	inode, err := newInode(ctx, msrc, donated, false, true)
	if err != nil {
		return nil, err
	}
	iops := inode.InodeOperations.(*inodeOperations)

	name := fmt.Sprintf("host:[%d]", inode.StableAttr.InodeID)
	dirent := fs.NewDirent(inode, name)
	defer dirent.DecRef()
	return newFile(ctx, dirent, flags, iops), nil
}


func fileFlagsFromDonatedFD(donated int) (fs.FileFlags, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(donated), syscall.F_GETFL, 0)
	if errno != 0 {
		log.Warningf("Failed to get file flags for donated FD %d (errno=%d)", donated, errno)
		return fs.FileFlags{}, syscall.EIO
	}
	accmode := flags & syscall.O_ACCMODE
	return fs.FileFlags{
		Direct:      flags&syscall.O_DIRECT != 0,
		NonBlocking: flags&syscall.O_NONBLOCK != 0,
		Sync:        flags&syscall.O_SYNC != 0,
		Append:      flags&syscall.O_APPEND != 0,
		Read:        accmode == syscall.O_RDONLY || accmode == syscall.O_RDWR,
		Write:       accmode == syscall.O_WRONLY || accmode == syscall.O_RDWR,
	}, nil
}
*/

// newFile returns a new fs.File.
func newFile(ctx context.Context, dirent *fs.Dirent, flags fs.FileFlags, iops *inodeOperations) *fs.File {
	if !iops.ReturnsWouldBlock() {
		// Allow reading/writing at an arbitrary offset for files that support it.
		flags.Pread = true
		flags.Pwrite = true
	}
	return fs.NewFile(ctx, dirent, flags, &fileOperations{iops: iops})
}

// EventRegister implements waiter.Waitable.EventRegister.
func (f *fileOperations) EventRegister(e *waiter.Entry, mask waiter.EventMask) {}

// EventUnregister implements waiter.Waitable.EventUnregister.
func (f *fileOperations) EventUnregister(e *waiter.Entry) {}


// Readiness uses the poll() syscall to check the status of the underlying FD.
func (f *fileOperations) Readiness(mask waiter.EventMask) waiter.EventMask {
	return fdnotifier.NonBlockingPoll(int32(f.iops.fileState.FD()), mask)
}

// Readdir implements fs.FileOperations.Readdir.
func (f *fileOperations) Readdir(ctx context.Context, file *fs.File, serializer fs.DentrySerializer) (int64, error) {
	root := fs.RootFromContext(ctx)
	defer root.DecRef()
	dirCtx := &fs.DirCtx{
		Serializer: serializer,
		DirCursor:  &f.dirCursor,
	}
	return fs.DirentReaddir(ctx, file.Dirent, f, root, dirCtx, file.Offset())
}

func (f *fileOperations) IterateDir(ctx context.Context, dirCtx *fs.DirCtx, offset int) (int, error) {
	return offset, nil
}

// Write implements fs.FileOperations.Write.
func (f *fileOperations) Write(ctx context.Context, file *fs.File, src usermem.IOSequence, offset int64) (int64, error) {
	// Write is not allowed in imgfs
	return 0, syserror.EPERM
}

// Read implements fs.FileOperations.Read.
func (f *fileOperations) Read(ctx context.Context, file *fs.File, dst usermem.IOSequence, offset int64) (int64, error) {
	// Would this file block?
	if f.iops.ReturnsWouldBlock() {
		// These files can't be memory mapped, assert this. This also
		// means that reads do not need to synchronize with memory
		// mappings nor metadata cached by this file's fs.Inode.
		if canMap(file.Dirent.Inode) {
			panic("files that can return EWOULDBLOCK cannot be memory mapped")
		}
		// Ignore the offset, these files don't support reading at
		// an arbitrary offset.
		reader := fd.NewReadWriter(f.iops.fileState.FD())
		n, err := dst.CopyOutFrom(ctx, safemem.FromIOReader{reader})
		if isBlockError(err) {
			// If we got any data at all, return it as a "completed" partial read
			// rather than retrying until complete.
			if n != 0 {
				err = nil
			} else {
				err = syserror.ErrWouldBlock
			}
		}
		return n, err
	}
	if !file.Dirent.Inode.MountSource.Flags.ForcePageCache {
		reader := secio.NewOffsetReader(fd.NewReadWriter(f.iops.fileState.FD()), offset)
		return dst.CopyOutFrom(ctx, safemem.FromIOReader{reader})
	}
	return f.iops.cachingInodeOps.Read(ctx, file, dst, offset)
}

// Fsync implements fs.FileOperations.Fsync.
func (f *fileOperations) Fsync(ctx context.Context, file *fs.File, start int64, end int64, syncType fs.SyncType) error {
	// Fsync is not allowed in imgfs
	return nil
}

// Flush implements fs.FileOperations.Flush.
func (f *fileOperations) Flush(context.Context, *fs.File) error {
	// This is a no-op because flushing the resource backing this
	// file would mean closing it. We can't do that because other
	// open files may depend on the backing host FD.
	return nil
}

// ConfigureMMap implements fs.FileOperations.ConfigureMMap.
func (f *fileOperations) ConfigureMMap(ctx context.Context, file *fs.File, opts *memmap.MMapOpts) error {
	if !canMap(file.Dirent.Inode) {
		return syserror.ENODEV
	}
	return fsutil.GenericConfigureMMap(file, f.iops.cachingInodeOps, opts)
}

// Seek implements fs.FileOperations.Seek.
func (f *fileOperations) Seek(ctx context.Context, file *fs.File, whence fs.SeekWhence, offset int64) (int64, error) {
	return fsutil.SeekWithDirCursor(ctx, file, whence, offset, &f.dirCursor)
}
