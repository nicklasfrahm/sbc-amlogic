# sbc-amlogic

This repo provides the overlay for Amlogic based Talos images.

## Supported Overlay

| Overlay Name | Board        | SoC                    | Description             |
| ------------ | ------------ | ---------------------- | ----------------------- |
| odroid-c4    | ODROID-C4    | Amlogic S905X3 (SM1)   | Overlay for ODROID-C4   |

## Building an image

```bash
docker run --rm -t -v ./_out:/out -v /dev:/dev --privileged \
    ghcr.io/siderolabs/imager:v1.11.5 \
    odroid-c4 --arch arm64 \
    --overlay-name=odroid-c4 \
    --overlay-image=ghcr.io/siderolabs/sbc-amlogic:<tag>
```

## Boot chain

Amlogic SoCs do not boot mainline U-Boot directly. The boot ROM expects a
signed FIP image that bundles the vendor BL2, BL30 and BL31 blobs together
with the DDR training firmware, with mainline U-Boot embedded as BL33.

Because Amlogic does not publish sources for those blobs or for the tool that
packs them, they are consumed from [LibreELEC/amlogic-boot-fip][fip], which is
the upstream Armbian and the U-Boot documentation both point at.

The build therefore has two artifact stages:

1. `u-boot-odroid-c4` builds mainline U-Boot with `odroid-c4_defconfig` on
   `linux/arm64` and emits `u-boot.bin`, which becomes BL33.
2. `fip-odroid-c4` packs and signs that into the final image. It is pinned to
   `linux/amd64` because `aml_encrypt_g12a` is a statically linked x86-64
   binary. The resulting blob is architecture independent.

[fip]: https://github.com/LibreELEC/amlogic-boot-fip

## Partition layout

The S905X3 boot ROM reads BL2 from **sector 1**, so the installer writes the
~1.5 MiB FIP image at a fixed offset of 512 bytes. This is unusual compared to
the other SBC families: Rockchip loads from sector 64 and Allwinner from
sector 16, both of which leave the start of the disk untouched.

Two consequences follow from that:

- The overlay sets a partition offset of 8192 LBAs (4 MiB) so that the GPT
  partition entry array and all Talos partitions are placed past the
  bootloader.
- The GPT **header** is fixed at LBA 1 by the GPT specification and is
  therefore overwritten by the bootloader. U-Boot and Linux both fall back to
  the backup GPT at the end of the disk, so the board boots and runs, but the
  primary GPT is not intact. Other GPT based distributions hit the same wall:
  Home Assistant OS ships this board with an MBR partition table instead.

  Anything that rewrites the primary GPT in place would overwrite BL2 and
  render the board unbootable until the bootloader is written again.

### Flashing

Because the primary GPT header is gone, the backup GPT at the end of the disk
is the **only** copy of the partition table. A raw image is smaller than the
medium it is written to, which leaves that backup stranded in the middle of
the card where neither U-Boot nor Linux look for it. The result is a medium
with no readable partition table at all: U-Boot skips it, falls through to
`distro_bootcmd`'s network target and the board never boots.

So after writing the image the backup GPT has to be moved to the end of the
medium:

```bash
sudo dd if=_out/odroid-c4-metal-arm64.raw of=/dev/sdX bs=4M conv=fsync status=progress
sudo ./hack/fix-backup-gpt.py _out/odroid-c4-metal-arm64.raw /dev/sdX
```

`hack/fix-backup-gpt.py` only writes the last few sectors, so the bootloader
at sector 1 is left alone. This is not needed when the image and the medium
happen to be the same size.

### Runtime consequences

- With the backup GPT in the right place the Linux kernel falls back to it and
  the partitions enumerate normally.
- `go-blockdevice` only falls back to the backup header when the primary one
  is **zeroed**. Here LBA 1 holds BL2 code, so `gpt.Read` returns an error
  instead. That stops Talos from rewriting the partition table, which is what
  keeps the bootloader intact, but it also means the `EPHEMERAL` partition
  will not auto-grow to fill the medium.
- Do not run `sgdisk`/`gdisk` repair on the image or the installed medium.
  "Repairing" the primary GPT writes over BL2 and the board stops booting.

The clean way out for eMMC is to write the FIP to the eMMC boot partition
(`/dev/mmcblk0boot0`), which is a separate area from the user data where the
GPT lives. That is not available for SD cards and is not implemented here.

## Comparison with Armbian

Armbian builds images for this board with an **MBR** partition table
(`IMAGE_PARTITION_TABLE="msdos"`, first partition at 4 MiB) and so never runs
into any of this. With MBR the partition table lives in the tail of sector 0,
bytes 446 to 509, which is exactly the range the Amlogic FIP leaves as a hole:
that is why Armbian's `write_uboot_platform` writes only the first 442 bytes
at offset 0. The Amlogic boot image format was designed to coexist with MBR.
GPT keeps its header in sector 1, which the FIP has to own, so the two cannot
share a disk. Talos only supports GPT, hence the backup GPT dance above.

## Boot order

The boot order is fixed in the SoC and cannot be changed from software. eMMC
is scanned before the SD card, so a board with a bootloader on eMMC will not
boot from SD. Remove the eMMC module, or erase its bootloader, when testing an
SD card image.

## Reproducible builds

`aml_encrypt_g12a` seeds a 16 byte nonce in front of each `@AML` header from a
coarse timestamp, so two builds of the same source produce images that differ
in 32 bytes. The images are functionally identical, but
`make reproducibility-test-local-sbc-amlogic` will report a difference.

[gxlimg][gxlimg] is a BSD licensed reimplementation that supports a
`REPRODUCIBLE=1` mode and runs on any architecture, which would remove both
this caveat and the `linux/amd64` pin. Its G12A support is documented as a
work in progress and its output diverges structurally from the vendor tool, so
it is not used here.

[gxlimg]: https://github.com/repk/gxlimg
