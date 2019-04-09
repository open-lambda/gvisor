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
	//"path"
	//"syscall"

	//"gvisor.googlesource.com/gvisor/pkg/fdnotifier"
	//"gvisor.googlesource.com/gvisor/pkg/log"
	"gvisor.googlesource.com/gvisor/pkg/waiter"
)

// descriptor wraps a host fd.
//
// +stateify savable
type descriptor struct {
	mappedArea []byte

	offsetBegin int64

	offsetEnd int64

	// value is the wrapped host fd. It is never saved or restored
	// directly. How it is restored depends on whether it was
	// donated and the fs.MountSource it was originally
	// opened/created from.
	value int `state:"nosave"`
}

// newDescriptor returns a wrapped host file descriptor. On success,
// the descriptor is registered for event notifications with queue.
func newDescriptor(begin int64, end int64) *descriptor {
	return &descriptor{
		offsetBegin: begin,
		offsetEnd: end,
		value:      -1,
	}
}

// initAfterLoad initializes the value of the descriptor after Load.
func (d *descriptor) initAfterLoad(mo *superOperations, id uint64, queue *waiter.Queue) error {
	return nil
}

// Release releases all resources held by descriptor.
func (d *descriptor) Release() {}
