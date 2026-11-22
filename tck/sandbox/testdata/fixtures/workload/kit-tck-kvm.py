#!/usr/bin/env python3
"""Exercise the kernel APIs an unprivileged inner VM process needs."""
import argparse
import fcntl
import os
import sys


def kvm(path):
    device = os.open(path, os.O_RDWR | os.O_CLOEXEC)
    try:
        version = fcntl.ioctl(device, 0xAE00, 0)  # KVM_GET_API_VERSION
        if version != 12:
            raise RuntimeError(f"KVM_GET_API_VERSION returned {version}, expected 12")
        vm = fcntl.ioctl(device, 0xAE01, 0)  # KVM_CREATE_VM
        os.close(vm)
    finally:
        os.close(device)


def vsock(path):
    device = os.open(path, os.O_RDWR | os.O_CLOEXEC)
    try:
        # A readable node is insufficient. VHOST_GET_FEATURES proves the
        # descriptor reaches the vhost driver without creating a listener.
        fcntl.ioctl(device, 0x8008AF00, bytearray(8), True)
    finally:
        os.close(device)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("kvm", "vsock", "all"))
    # Overrides let the suite prove ordinary readable paths cannot pass the
    # real syscall probe on hosts without virtualization devices.
    parser.add_argument("--kvm", default="/dev/kvm")
    parser.add_argument("--vsock", default="/dev/vhost-vsock")
    args = parser.parse_args()
    for name, probe, path in (("kvm", kvm, args.kvm), ("vsock", vsock, args.vsock)):
        if args.mode in (name, "all"):
            try:
                probe(path)
            except (OSError, RuntimeError) as error:
                # The suite accepts only this reserved result as evidence of
                # unavailable hardware. Import errors and crashes keep their
                # ordinary exit status and cannot pass an absence check.
                print(f"unavailable:{name}:{path}: {error}", file=sys.stderr)
                return 3
    print("usable")
    return 0


if __name__ == "__main__":
    sys.exit(main())
