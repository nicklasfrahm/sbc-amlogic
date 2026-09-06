#!/usr/bin/env python3
"""Relocate the backup GPT of a flashed Amlogic image to the end of the medium.

On Amlogic the boot ROM loads BL2 from sector 1, so the FIP image overwrites
the primary GPT header and the backup GPT becomes the only copy of the
partition table. Writing a disk image that is smaller than the target medium
leaves that backup somewhere in the middle, where neither U-Boot nor Linux
look for it, and the medium ends up with no readable partition table at all.

This rewrites the backup GPT at the end of the target so both can find it, and
fixes up the protective MBR to span the real medium. Linux rejects the whole
GPT when the protective MBR does not cover the device:

    sz = le32_to_cpu(mbr->partition_record[part].size_in_lba);
    if (sz != (uint32_t) total_sectors - 1 && sz != 0xFFFFFFFF)
            ret = 0;

so without that second fixup the partitions never show up even though the
backup GPT is perfectly valid. Neither write touches LBA 1, so the bootloader
stays intact.
"""
import struct
import sys
import zlib

SIG = b"EFI PART"
SECTOR = 512


def read_header(f, lba):
    f.seek(lba * SECTOR)
    hdr = f.read(SECTOR)
    if hdr[:8] != SIG:
        raise SystemExit(f"no GPT header at LBA {lba}")
    return bytearray(hdr)


def main():
    if len(sys.argv) != 3:
        raise SystemExit(f"usage: {sys.argv[0]} <source-image> <target-device>")

    image, target = sys.argv[1], sys.argv[2]

    with open(image, "rb") as f:
        f.seek(0, 2)
        img_last = f.tell() // SECTOR - 1
        hdr = read_header(f, img_last)

        hdr_size = struct.unpack_from("<I", hdr, 12)[0]
        entries_lba = struct.unpack_from("<Q", hdr, 72)[0]
        num_entries = struct.unpack_from("<I", hdr, 80)[0]
        entry_size = struct.unpack_from("<I", hdr, 84)[0]

        f.seek(entries_lba * SECTOR)
        entries = f.read(num_entries * entry_size)

    entries_sectors = (len(entries) + SECTOR - 1) // SECTOR

    with open(target, "r+b") as f:
        f.seek(0, 2)
        tgt_last = f.tell() // SECTOR - 1

        if tgt_last == img_last:
            print("target is the same size as the image, nothing to do")
            return

        new_entries_lba = tgt_last - entries_sectors

        struct.pack_into("<Q", hdr, 24, tgt_last)            # my LBA
        struct.pack_into("<Q", hdr, 32, 1)                   # alternate LBA
        struct.pack_into("<Q", hdr, 48, new_entries_lba - 1)  # last usable LBA
        struct.pack_into("<Q", hdr, 72, new_entries_lba)     # entries LBA
        struct.pack_into("<I", hdr, 88, zlib.crc32(entries))  # entries CRC

        struct.pack_into("<I", hdr, 16, 0)
        crc = zlib.crc32(bytes(hdr[:hdr_size]))
        struct.pack_into("<I", hdr, 16, crc)

        f.seek(new_entries_lba * SECTOR)
        f.write(entries)
        f.seek(tgt_last * SECTOR)
        f.write(bytes(hdr))

        # Point the protective MBR at the whole medium, otherwise Linux throws
        # the GPT away before it ever looks at the backup header.
        f.seek(0)
        mbr = bytearray(f.read(SECTOR))
        span = min(tgt_last, 0xFFFFFFFF)
        patched = False

        for i in range(4):
            rec = 446 + i * 16
            if mbr[rec + 4] == 0xEE:
                struct.pack_into("<I", mbr, rec + 12, span)
                patched = True

        if not patched:
            raise SystemExit("no protective MBR entry found, refusing to guess")

        f.seek(0)
        f.write(bytes(mbr))
        f.flush()

    print(f"backup GPT moved from LBA {img_last} to {tgt_last}")
    print(f"partition entries written at LBA {new_entries_lba}")
    print(f"protective MBR now spans {span} sectors")


if __name__ == "__main__":
    main()
