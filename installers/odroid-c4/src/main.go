// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/siderolabs/go-copy/copy"
	"github.com/siderolabs/talos/pkg/machinery/overlay"
	"github.com/siderolabs/talos/pkg/machinery/overlay/adapter"
	"golang.org/x/sys/unix"
)

const (
	// The Amlogic boot ROM loads BL2 from sector 1 of the boot medium, so the
	// signed FIP image is written at a fixed 512 byte offset. This matches
	// what Armbian and the U-Boot documentation do for this board.
	off int64 = 512
	dtb       = "amlogic/meson-sm1-odroid-c4.dtb"
)

func main() {
	adapter.Execute(&OdroidC4Installer{})
}

// References:
//   - https://www.hardkernel.com/shop/odroid-c4/
//   - https://github.com/u-boot/u-boot/blob/master/doc/board/amlogic/odroid-c4.rst
type OdroidC4Installer struct{}

type odroidC4ExtraOptions struct{}

func (i *OdroidC4Installer) GetOptions(extra odroidC4ExtraOptions) (overlay.Options, error) {
	return overlay.Options{
		Name: "odroid-c4",
		KernelArgs: []string{
			"console=tty0",
			"console=ttyAML0,115200",
			// Prints from the AO UART before the real serial driver is up,
			// which is the only way to see an early kernel hang.
			"earlycon=meson,0xff803000",
			"sysctl.kernel.kexec_load_disabled=1",
		},
		PartitionOptions: overlay.PartitionOptions{
			// The signed FIP image is roughly 1.5 MiB and starts at sector 1,
			// so the GPT partition entry array has to be pushed past it.
			// 4 MiB leaves room for the bootloader to grow.
			Offset: 8192,
		},
	}, nil
}

func (i *OdroidC4Installer) Install(options overlay.InstallOptions[odroidC4ExtraOptions]) error {
	uboot, err := os.ReadFile(filepath.Join(options.ArtifactsPath, "arm64/u-boot/odroid-c4/u-boot.bin"))
	if err != nil {
		return err
	}

	if err = installBootloader(options.InstallDisk, uboot); err != nil {
		return err
	}

	src := filepath.Join(options.ArtifactsPath, "arm64/dtb", dtb)
	dst := filepath.Join(options.MountPrefix, "/boot/EFI/dtb", dtb)

	err = os.MkdirAll(filepath.Dir(dst), 0o700)
	if err != nil {
		return err
	}

	return copy.File(src, dst)
}

// installBootloader puts the signed FIP where the boot ROM will find it.
//
// On eMMC that is the boot0 hardware partition, which leaves the user area
// alone and lets the disk carry a normal GPT. Everywhere else, including SD
// cards and the loopback device used when building an image, it goes to
// sector 1 of the disk itself, on top of the primary GPT header.
func installBootloader(installDisk string, fip []byte) error {
	if bootDev := bootPartition(installDisk); bootDev != "" {
		err := writeBootPartition(bootDev, fip)
		if err == nil {
			// Not fatal: the boot ROM checks boot0 by itself, this only makes
			// the eMMC's own boot configuration agree with it.
			if err = enableBootPartition(installDisk); err != nil {
				fmt.Fprintf(os.Stderr, "odroid-c4: %s\n", err)
			}

			return nil
		}

		// Fall back to the user area rather than leave the board unbootable.
		fmt.Fprintf(os.Stderr, "odroid-c4: %s, falling back to the user area\n", err)
	}

	f, err := os.OpenFile(installDisk, os.O_RDWR|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", installDisk, err)
	}

	defer f.Close() //nolint:errcheck

	if _, err = f.WriteAt(fip, off); err != nil {
		return err
	}

	// NB: In the case that the block device is a loopback device, we sync here
	// to esure that the file is written before the loopback device is
	// unmounted.
	return f.Sync()
}
