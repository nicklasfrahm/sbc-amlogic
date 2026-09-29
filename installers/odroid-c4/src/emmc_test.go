// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

// The ioctl number encodes the size of the argument struct, so a layout that
// does not match struct mmc_ioc_cmd from linux/mmc/ioctl.h would be rejected
// by the kernel with EINVAL, or worse, misread.
func TestMMCIocCmdMatchesKernelLayout(t *testing.T) {
	if got := unsafe.Sizeof(mmcIocCmd{}); got != mmcIocCmdStructBytes {
		t.Fatalf("struct mmc_ioc_cmd is %d bytes, kernel expects %d", got, mmcIocCmdStructBytes)
	}
}

// _IOWR(MMC_BLOCK_MAJOR, 0, struct mmc_ioc_cmd) worked out by hand:
//
//	dir  = _IOC_READ|_IOC_WRITE = 3 -> 0xC0000000
//	size = 72                       -> 0x00480000
//	type = 179                      -> 0x0000B300
//	nr   = 0
func TestIoctlNumber(t *testing.T) {
	const expected = 0xC048B300

	if ioctlMMCCmd != expected {
		t.Fatalf("ioctl number is %#x, expected %#x", ioctlMMCCmd, expected)
	}
}

// The SWITCH argument must match what `mmc bootpart enable 1 1 <dev>` sends,
// i.e. write PART_CONFIG (179) = 0x48: BOOT_ACK plus boot0 enabled.
func TestSwitchArgument(t *testing.T) {
	arg := uint32(mmcSwitchModeWrite<<24 |
		extCSDPartConfig<<16 |
		partConfigBoot0<<8 |
		extCSDCmdSetNormal)

	const expected = 0x03B34801

	if arg != expected {
		t.Fatalf("switch arg is %#x, expected %#x", arg, expected)
	}

	// boot0 selected via BOOT_PARTITION_ENABLE, with BOOT_ACK set
	if partConfigBoot0 != (1<<6)|(1<<3) {
		t.Fatalf("PART_CONFIG is %#x, expected BOOT_ACK|boot0", partConfigBoot0)
	}
}

// Anything without a boot0 sibling, which covers SD cards and the loopback
// device used when building an image, must take the user area path.
func TestBootPartitionAbsentForNonEMMC(t *testing.T) {
	for _, disk := range []string{
		filepath.Join(t.TempDir(), "loop0"),
		"/dev/definitely-not-a-disk",
	} {
		if got := bootPartition(disk); got != "" {
			t.Errorf("bootPartition(%q) = %q, expected no boot partition", disk, got)
		}
	}
}

// A boot0 node is only usable if the kernel also exposes force_ro for it;
// without that we cannot lift the read-only guard, so we must not claim it.
func TestBootPartitionRequiresForceRO(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "mmcblk0")

	if err := os.WriteFile(disk+"boot0", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// /sys/block/<name>/force_ro will not exist for a temp file
	if got := bootPartition(disk); got != "" {
		t.Errorf("bootPartition(%q) = %q, expected none without force_ro", disk, got)
	}
}
