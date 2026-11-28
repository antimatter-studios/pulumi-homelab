- **`SshdConfig` can no longer lock a machine out.** Create, update and delete check the whole
  configuration with the change in place (a copy of `/etc/ssh` with its `Include`s rewritten) before
  touching the live tree, install by an atomic rename, reload sshd rather than restart it, and then
  require sshd to be active, listening on its ports and answering with an `SSH-2.0-` banner. Any
  failure after the install restores the previous drop-in (or removes the new one), reloads sshd
  (starting it if the change killed it) and fails with what went wrong and how the rollback went.
  The provider now also refuses to change sshd on a machine whose sshd is not healthy beforehand.
