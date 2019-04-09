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

// Package host implements an fs.Filesystem for files backed by host
// file descriptors.
package imgfs

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"strconv"
	"syscall"

	// "gvisor.googlesource.com/gvisor/pkg/log"
	"gvisor.googlesource.com/gvisor/pkg/sentry/context"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs"
	"gvisor.googlesource.com/gvisor/pkg/sentry/fs/ramfs"
	"gvisor.googlesource.com/gvisor/pkg/log"
	"gvisor.googlesource.com/gvisor/pkg/sentry/usermem"
)

// FilesystemName is the name under which Filesystem is registered.
const FilesystemName = "imgfs"

const (
	// packageFDKey is the mount option containing an int of package FD
	packageFDKey = "packageFD"
)

// Filesystem is a pseudo file system that is only available during the setup
// to lock down the configurations. This filesystem should only be mounted at root.
//
// Think twice before exposing this to applications.
//
// +stateify savable
type Filesystem struct {
	// whitelist is a set of host paths to whitelist.
	packageFD int
}

type fileMetadata struct {
	Begin int64
	End int64
	Name string
}

var _ fs.Filesystem = (*Filesystem)(nil)

// Name is the identifier of this file system.
func (*Filesystem) Name() string {
	return FilesystemName
}

// AllowUserMount prohibits users from using mount(2) with this file system.
func (*Filesystem) AllowUserMount() bool {
	return false
}

// AllowUserList allows this filesystem to be listed in /proc/filesystems.
func (*Filesystem) AllowUserList() bool {
	return true
}

// Flags returns that there is nothing special about this file system.
func (*Filesystem) Flags() fs.FilesystemFlags {
	return 0
}

// Mount returns an fs.Inode exposing the host file system.  It is intended to be locked
// down in PreExec below.
func (f *Filesystem) Mount(ctx context.Context, _ string, flags fs.MountSourceFlags, data string, _ interface{}) (*fs.Inode, error) {
	// Parse generic comma-separated key=value options.
	options := fs.GenericMountSourceOptions(data)

	// Grab the packageFD if one was specified.
	if packageFD, ok := options[packageFDKey]; ok {
		v, err := strconv.ParseInt(packageFD, 10, 32)
		if (err != nil) {
			return nil, fmt.Errorf("cannot convert packageFD id to int: %v", err)
		}
		f.packageFD = int(v)
		delete(options, packageFDKey)
	}

	if (f.packageFD <= 0) {
		return nil, fmt.Errorf("invalid packageFD when mounting imgfs: %v", f.packageFD)
	}

	log.Infof("imgfs.packageFD: %v", f.packageFD)

	// Fail if the caller passed us more options than we know about.
	if len(options) > 0 {
		return nil, fmt.Errorf("unsupported mount options: %v", options)
	}

	// Construct img file system mount and inode.
	msrc := newMountSource(ctx, fs.RootOwner, f, flags)

	var s syscall.Stat_t
	err := syscall.Fstat(int(f.packageFD), &s)
	if err != nil {
		return nil, fmt.Errorf("unable to stat package file: %v", err)
	}
	log.Infof("stat package file size: %v", s.Size)
	length := int(s.Size)
	mmap, err := syscall.Mmap(int(f.packageFD), 0, length, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("can't mmap the package image file, err: %v", err)
	}
	headerLoc := mmap[length - 10 : length]
	headerReader := bytes.NewReader(headerLoc)
	n, err := binary.ReadVarint(headerReader)
	if err != nil {
		return nil, fmt.Errorf("can't read header location, err: %v", err)
	}
	header := mmap[int(n) : length - 10]
	metadataReader := bytes.NewReader(header)
	var metadata []fileMetadata
	dec := gob.NewDecoder(metadataReader)
	errDec := dec.Decode(&metadata)
	if errDec != nil {
		  return nil, fmt.Errorf("can't decode metadata data, err: %v", errDec)
	}

	contents := map[string]*fs.Inode{}
	for _, v := range metadata {
		fileBytes := mmap[v.Begin : v.End]
		fileString := string(fileBytes)
		log.Infof("file: %v, data: %v\n", v.Name, fileString) // TODO: comment it
		inode, err := newInode(ctx, msrc, v.Begin, v.End, mmap)
		contents[v.Name] = inode
		if err != nil {
			return nil, fmt.Errorf("can't create inode for %v, err: %v", v.Name, err)
		}
	}


	d := ramfs.NewDir(ctx, contents, fs.RootOwner, fs.FilePermsFromMode(0555))

	newinode := fs.NewInode(d, msrc, fs.StableAttr{
		DeviceID:  imgfsFileDevice.DeviceID(),
		InodeID:   imgfsFileDevice.NextIno(),
		BlockSize: usermem.PageSize,
		Type:      fs.Directory,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot create new inode for imgfs root")
	}
	return newinode, nil
}

// newMountSource constructs a new host fs.MountSource
// relative to a root path. The root should match the mount point.

func newMountSource(ctx context.Context, mounter fs.FileOwner, filesystem fs.Filesystem, flags fs.MountSourceFlags) *fs.MountSource {
	return fs.NewMountSource(&superOperations{
		inodeMappings:          make(map[uint64]string),
		mounter:                mounter,
	}, filesystem, flags)
}

// superOperations implements fs.MountSourceOperations.
//
// +stateify savable
type superOperations struct {
	fs.SimpleMountSourceOperations

	// inodeMappings contains mappings of fs.Inodes associated
	// with this MountSource to paths under root.
	inodeMappings map[uint64]string

	// mounter is the cached EUID/EGID that mounted this file system.
	mounter fs.FileOwner
}

var _ fs.MountSourceOperations = (*superOperations)(nil)

// ResetInodeMappings implements fs.MountSourceOperations.ResetInodeMappings.
func (m *superOperations) ResetInodeMappings() {
	m.inodeMappings = make(map[uint64]string)
}

// SaveInodeMapping implements fs.MountSourceOperations.SaveInodeMapping.
func (m *superOperations) SaveInodeMapping(inode *fs.Inode, path string) {
	// This is very unintuitive. We *CANNOT* trust the inode's StableAttrs,
	// because overlay copyUp may have changed them out from under us.
	// So much for "immutable".
	sattr := inode.InodeOperations.(*inodeOperations).fileState.sattr
	m.inodeMappings[sattr.InodeID] = path
}

// Keep implements fs.MountSourceOperations.Keep.
//
// TODO: It is possible to change the permissions on a
// host file while it is in the dirent cache (say from RO to RW), but it is not
// possible to re-open the file with more relaxed permissions, since the host
// FD is already open and stored in the inode.
//
// Using the dirent LRU cache increases the odds that this bug is encountered.
// Since host file access is relatively fast anyways, we disable the LRU cache
// for host fs files.  Once we can properly deal with permissions changes and
// re-opening host files, we should revisit whether or not to make use of the
// LRU cache.
func (*superOperations) Keep(*fs.Dirent) bool {
	return false
}

func init() {
	fs.RegisterFilesystem(&Filesystem{})
}
