- **`SwapPartition`** — a swap partition, found by its GPT name (also its swap label and id), created
  in free space only (after the last partition, or at `start`) and never by moving, resizing or
  deleting another one. It refuses a disk that is not already GPT, any overlap, and a partition
  with that name but another size, place or type; it runs `mkswap` only on a partition with no
  signature at all. It partitions under the disk's lock against the table it planned with, tells
  the kernel about that one partition (`partx`), swaps at `priority` (with `discard` if given), and
  is enabled at boot by a systemd swap unit of its own rather than an fstab line. Read reports the
  size, signature, whether it swaps and at what priority; an import adopts an existing partition
  with no change, and `persistent: false` adopts one that something else still enables at boot.
  Delete turns it off (refusing when its pages would not fit in available memory) and removes the
  unit (only its own, marked; another at that path is left with a warning), and keeps the partition unless `deletePartition` is set. Priority, discard and persistence
  update in place; another size, disk or start is refused unless `deletePartition` is in the state.
