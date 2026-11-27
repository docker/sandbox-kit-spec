#!/usr/bin/env python3
"""Print the kernel driver behind a DRM node, asked through the node itself."""
import ctypes
import fcntl
import os
import sys


class DRMVersion(ctypes.Structure):
    _fields_ = [
        ("major", ctypes.c_int),
        ("minor", ctypes.c_int),
        ("patchlevel", ctypes.c_int),
        ("name_len", ctypes.c_size_t),
        ("name", ctypes.c_void_p),
        ("date_len", ctypes.c_size_t),
        ("date", ctypes.c_void_p),
        ("desc_len", ctypes.c_size_t),
        ("desc", ctypes.c_void_p),
    ]


# _IOWR('d', 0x00, struct drm_version): read-write, the struct's size, type 'd'.
DRM_IOCTL_VERSION = (3 << 30) | (ctypes.sizeof(DRMVersion) << 16) | (ord("d") << 8)


def driver(path):
    # The node answers for its own driver, so sysfs need not be visible:
    # a sandbox may mask /sys and still hand over a usable render node.
    device = os.open(path, os.O_RDWR | os.O_CLOEXEC)
    try:
        version = DRMVersion()
        fcntl.ioctl(device, DRM_IOCTL_VERSION, version, True)
        name = ctypes.create_string_buffer(version.name_len + 1)
        version = DRMVersion(name_len=version.name_len, name=ctypes.addressof(name))
        fcntl.ioctl(device, DRM_IOCTL_VERSION, version, True)
        return name.value.decode()
    finally:
        os.close(device)


def main():
    path = sys.argv[1]
    try:
        print(driver(path))
    except OSError as error:
        # The probe treats only this reserved result as "not a DRM node".
        print(f"unavailable:{path}: {error}", file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main())
