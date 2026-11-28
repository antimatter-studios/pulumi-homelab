# A disposable Debian 13 for SwapPartition's integration test, unprivileged: the disk is an image
# file, and the real gdisk, fdisk and util-linux partition it, probe it and format it. What needs
# a kernel stands in as scripts in /usr/local/sbin, ahead of /usr/sbin on sudo's path: partx copies
# a partition out of the image into a file next to it (its "device node") and links it under
# /dev/disk/by-partlabel as udev would; swapon and swapoff keep a table in /run/swaps; systemctl
# enables, starts and stops swap units from their What= and Priority=. Nothing here can touch the
# host's disks or its swap.
FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends sudo gdisk fdisk util-linux e2fsprogs \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash chris \
 && echo 'chris ALL=(root) NOPASSWD: ALL' > /etc/sudoers.d/chris \
 && mkdir -p /etc/systemd/system
COPY partx swapon swapoff systemctl /usr/local/sbin/
CMD ["sleep", "infinity"]
