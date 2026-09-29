// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The boot ROM reads the FIP from sector 1 of the user area, and only falls
// back to the eMMC boot partitions when it does not find one there:
//
//	On GXL and newer boards it expects to find the FIP binary in sector 1,
//	512 bytes offset from the start. If not found it checks the boot0
//	partition, then the boot1 partition.
//
// Putting the bootloader in boot0 therefore keeps it out of the user area
// entirely, which is the only way this SoC can carry an intact GPT: the
// primary header lives at LBA 1, exactly where the FIP would otherwise go.
//
// SD cards have no boot partitions, so they always take the user area path.

const (
	// EXT_CSD register holding the boot configuration.
	extCSDPartConfig = 179

	// BOOT_ACK enabled, BOOT_PARTITION_ENABLE set to boot0, no partition
	// access override. Equivalent to `mmc bootpart enable 1 1 <device>`.
	partConfigBoot0 = 0x48

	mmcSwitch            = 6
	mmcSwitchModeWrite   = 0x03
	extCSDCmdSetNormal   = 0x01
	mmcRSPSPIR1B         = 0x480
	mmcRSPR1B            = 0x1D
	mmcCmdAC             = 0x00
	mmcBlockIOCTLMagic   = 179
	mmcIocCmdStructBytes = 72
)

// mmcIocCmd mirrors struct mmc_ioc_cmd from linux/mmc/ioctl.h.
type mmcIocCmd struct {
	writeFlag      int32
	isAcmd         int32
	opcode         uint32
	arg            uint32
	response       [4]uint32
	flags          uint32
	blksz          uint32
	blocks         uint32
	postsleepMinUs uint32
	postsleepMaxUs uint32
	dataTimeoutNs  uint32
	cmdTimeoutMs   uint32
	pad            uint32
	dataPtr        uint64
}

// ioctlMMCCmd is _IOWR(MMC_BLOCK_MAJOR, 0, struct mmc_ioc_cmd).
const ioctlMMCCmd = (3 << 30) | (mmcIocCmdStructBytes << 16) | (mmcBlockIOCTLMagic << 8) | 0

// bootPartition returns the boot0 device belonging to installDisk, if the disk
// is an eMMC that has one. Loopback devices and SD cards have none, so image
// builds and SD installs fall through to the user area path.
func bootPartition(installDisk string) string {
	boot0 := installDisk + "boot0"

	if _, err := os.Stat(boot0); err != nil {
		return ""
	}

	// force_ro tells us the kernel really does treat this as a boot area.
	if _, err := os.Stat(forceROPath(boot0)); err != nil {
		return ""
	}

	return boot0
}

func forceROPath(bootDev string) string {
	return filepath.Join("/sys/block", filepath.Base(bootDev), "force_ro")
}

// setForceRO flips the read-only guard the kernel puts on eMMC boot areas.
func setForceRO(bootDev string, ro bool) error {
	value := []byte("0\n")
	if ro {
		value = []byte("1\n")
	}

	return os.WriteFile(forceROPath(bootDev), value, 0o644)
}

// writeBootPartition writes the FIP into an eMMC boot partition, leaving the
// user area untouched so that the GPT stays intact.
func writeBootPartition(bootDev string, fip []byte) error {
	if err := setForceRO(bootDev, false); err != nil {
		return fmt.Errorf("failed to clear force_ro on %s: %w", bootDev, err)
	}

	// Always restore the guard, even if the write fails.
	defer setForceRO(bootDev, true) //nolint:errcheck

	f, err := os.OpenFile(bootDev, os.O_RDWR|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", bootDev, err)
	}

	defer f.Close() //nolint:errcheck

	if _, err = f.WriteAt(fip, off); err != nil {
		return fmt.Errorf("failed to write bootloader to %s: %w", bootDev, err)
	}

	if err = f.Sync(); err != nil {
		return fmt.Errorf("failed to sync %s: %w", bootDev, err)
	}

	return nil
}

// enableBootPartition points the eMMC's boot configuration at boot0.
//
// The boot ROM is documented to check boot0 on its own, so this is belt and
// braces rather than a hard requirement, and callers treat a failure as
// non-fatal.
func enableBootPartition(installDisk string) error {
	f, err := os.OpenFile(installDisk, os.O_RDWR|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", installDisk, err)
	}

	defer f.Close() //nolint:errcheck

	cmd := mmcIocCmd{
		writeFlag: 1,
		opcode:    mmcSwitch,
		arg: mmcSwitchModeWrite<<24 |
			extCSDPartConfig<<16 |
			partConfigBoot0<<8 |
			extCSDCmdSetNormal,
		flags:        mmcRSPSPIR1B | mmcRSPR1B | mmcCmdAC,
		cmdTimeoutMs: 1000,
	}

	if _, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		f.Fd(),
		uintptr(ioctlMMCCmd),
		uintptr(unsafe.Pointer(&cmd)), //nolint:gosec
	); errno != 0 {
		return fmt.Errorf("failed to set boot partition config on %s: %w", installDisk, errno)
	}

	return nil
}
